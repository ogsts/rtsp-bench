package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
	"github.com/deepch/vdk/av"
	"github.com/deepch/vdk/codec/h264parser"
	"github.com/deepch/vdk/format/rtsp"
	"github.com/pion/randutil"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/gorilla/websocket"
)

type Msg struct {
	Type* string
	Sdp* string
	Candidate* string
	SdpMid* string
	SdpMLineIndex* uint16
	UsernameFragment* string
}
type Candidate struct {
	Candidate *webrtc.ICECandidate  `json:candidate`
}

var (
	ws *websocket.Conn
	peerConnection *webrtc.PeerConnection = nil
	tracks [32]*webrtc.TrackLocalStaticSample
	rtpSender [32]*webrtc.RTPSender
	trackCnt int = 0
	rtspUrls [3]string
	urlCnt int = 0
)

func addTrack(w http.ResponseWriter, r *http.Request) {
	
	log.Println("#-> addTrack")
	var err error
	
	if (trackCnt > 31) {
		log.Println("<-# addTrack() Too many tracks ")
		return
	}
	
	//videoRTCPFeedback := []webrtc.RTCPFeedback{{"goog-remb", ""}, {"ccm", "fir"}, {"nack", ""}, {"nack", "pli"}}
	//tracks[trackCnt], err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{webrtc.MimeTypeH264, 90000, 0, "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f", videoRTCPFeedback}, 
	tracks[trackCnt], err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: "video/h264",}, 
		fmt.Sprintf("video-%d", randutil.NewMathRandomGenerator().Uint32()),
		fmt.Sprintf("video-%d", randutil.NewMathRandomGenerator().Uint32()),)
	if err != nil {
		panic(err)
	}

	if tracks[trackCnt] == nil {
		panic(err)
	}
	
	rtpSender[trackCnt], err = pc().AddTrack(tracks[trackCnt]); 
	if err != nil {
		panic(err)
	}

	go rtspConsumer(rtspUrls[urlCnt], tracks[trackCnt], rtpSender[trackCnt] )

	trackCnt++
	urlCnt = ( urlCnt + 1 ) % 3

	response := []byte(`{"result":"ok"}`)
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(response); err != nil {
		panic(err)
	}
	log.Println("<-# addTrack ", trackCnt)
}


// Connect to an RTSP URL and pull media.
// Convert H264 to Annex-B, then write to outboundVideoTrack which sends to all PeerConnections
func rtspConsumer(url string, track *webrtc.TrackLocalStaticSample, rtpSender *webrtc.RTPSender) {
	
	log.Println("#-> rtspConsumer() ", url)

	annexbNALUStartCode := func() []byte { return []byte{0x00, 0x00, 0x00, 0x01} }

	go func() {
			for {
					rtcpBuf := make([]byte, 1500)
					n, _, err := rtpSender.Read(rtcpBuf)
					if err != nil {
						log.Print(err)
						return
					}
					log.Printf("RTCP packet received: %s", hex.Dump(rtcpBuf[:n]))
			}
	}()
	
	for {
		session, err := rtsp.Dial(url)
		if err != nil {
			panic(err)
		}
		session.RtpKeepAliveTimeout = 10 * time.Second

		codecs, err := session.Streams()
		if err != nil {
			panic(err)
		}
		for i, t := range codecs {
			log.Println("Stream", i, "is of type", t.Type().String())
		}
		if codecs[1].Type() != av.H264 {
			panic("RTSP feed must begin with a H264 codec")
		}
		if len(codecs) != 1 {
			log.Println("Ignoring all but the first stream.")
		}

		var previousTime time.Duration
		for {
			pkt, err := session.ReadPacket()
			if err != nil {
				break
			}

			if pkt.Idx != 1 {
				//audio or other stream, skip it
				continue
			}
			if(0>1){
				log.Printf("PACKET: \n%s", hex.Dump(pkt.Data))
			}
					
			pkt.Data = pkt.Data[4:]

			// For every key-frame pre-pend the SPS and PPS

			if pkt.IsKeyFrame {
				pkt.Data = append(annexbNALUStartCode(), pkt.Data...)
				pkt.Data = append(codecs[1].(h264parser.CodecData).PPS(), pkt.Data...)
				pkt.Data = append(annexbNALUStartCode(), pkt.Data...)
				pkt.Data = append(codecs[1].(h264parser.CodecData).SPS(), pkt.Data...)
				pkt.Data = append(annexbNALUStartCode(), pkt.Data...)
			}

			bufferDuration := pkt.Time - previousTime
			previousTime = pkt.Time
			if err = track.WriteSample(media.Sample{Data: pkt.Data, Duration: bufferDuration}); err != nil && err != io.ErrClosedPipe {
				panic(err)
			}
		}
		log.Println("	rtspConsumer() closing... ")
		if err = session.Close(); err != nil {
			log.Println("session Close error", err)
		}

		time.Sleep(5 * time.Second)
	}
	log.Println("<-# rtspConsumer")
}

// We'll need to define an Upgrader
// this will require a Read and Write buffer size
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func pc() (*webrtc.PeerConnection) {
	log.Println("#-> pc()")
	if ( peerConnection == nil ) {
		var err error
		
		
		// Prepare the configuration
		config := webrtc.Configuration{ ICEServers: []webrtc.ICEServer{ {
					URLs: []string{"stun:stun.l.google.com:19302"},
			},},
		}
		
		peerConnection, err = webrtc.NewPeerConnection(config)
		if err != nil {
			panic(err)
		}
	
		peerConnection.OnNegotiationNeeded(func() {
			log.Println("#-> OnNegotiationNeeded")
			offer, err := peerConnection.CreateOffer(nil)
			if err != nil {
				panic(err)
			}
			if (peerConnection.SignalingState().String() != "stable") {
				log.Println("<-# OnNegotiationNeeded() Cant negotiate in this state: ",peerConnection.SignalingState().String())
				return;
			}
			
			err = peerConnection.SetLocalDescription(offer)
			if err != nil {
				panic(err)
			}
			log.Println("	OnNegotiationNeeded() SetLocalDescription OK")
			response, err := json.Marshal(*peerConnection.LocalDescription())
			if err != nil {
				panic(err)
			}
			log.Println("	OnNegotiationNeeded() marshal OK")
			if err = ws.WriteMessage(1, response); err != nil {
				log.Println(err)
				panic(err)
			}
			log.Println("<-# OnNegotiationNeeded")
		})
	
		peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
			if connectionState == webrtc.ICEConnectionStateDisconnected {
				log.Println("~~~~~~~~~~~~~~~ DISCONNECTED ~~~~~~~~~~~~~~~~~~")
				if err := peerConnection.Close(); err != nil {
					panic(err)
				}
				peerConnection = nil
			} else if connectionState == webrtc.ICEConnectionStateConnected {
				log.Println("~~~~~~~~~~~~~~~ CONNECTED ~~~~~~~~~~~~~~~~~~")
			}
		})
		
		peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
			log.Println("#-> peerConnection.OnICECandidate()")
			if candidate == nil {
				return
			}
			
			request, err := json.Marshal(&Candidate{Candidate: candidate})
			if err != nil {
				panic(err)
			}
			log.Println("	peerConnection.OnICECandidate() marshal OK", string(request))
			if err = ws.WriteMessage(1, request); err != nil {
				log.Println(err)
				panic(err)
			}
			log.Println("	peerConnection.OnICECandidate() write OK")
			log.Println("<-# peerConnection.OnICECandidate()")
		})
		
		
		log.Println("<-# pc() new")
	} else {
		log.Println("<-# pc() existing")
	}
	return peerConnection
}

func reader(conn *websocket.Conn) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			log.Println(err)
			return
		}

		fmt.Println("Message: ", string(data))
		
		var msg Msg
		json.Unmarshal([]byte(data), &msg)

		if(msg.Type != nil){
			fmt.Println("Type: ", *msg.Type )		
		}
		if(msg.Sdp != nil){
			fmt.Println("Sdp: ", *msg.Sdp )		
		}
		if(msg.Candidate != nil){
			fmt.Println("Candidate: ", *msg.Candidate )		
		}
		if(msg.SdpMid != nil){
			fmt.Println("sdpMid: ", *msg.SdpMid )
		}
		if(msg.SdpMLineIndex != nil){
			fmt.Println("sdpMLineIndex: ", *msg.SdpMLineIndex )
		}
		if(msg.UsernameFragment != nil){
			fmt.Println("usernameFragment: ", *msg.UsernameFragment )
		}
		
		if(msg.Type != nil) {
			
			if(*msg.Type == "offer") {
				log.Println("	reader() OFFER")
				if (pc().SignalingState().String() != "stable") {
					log.Println("	reader() Cant process offer in this state: ",pc().SignalingState().String())
					continue;
				}
				var offer webrtc.SessionDescription
				offer.Type = webrtc.SDPTypeOffer
				offer.SDP = *msg.Sdp

				if err = pc().SetRemoteDescription(offer); err != nil {
					panic(err)
				}
				log.Println("	reader() SetRemoteDescription OK")
				answer, err := pc().CreateAnswer(nil)
				if err != nil {
					panic(err)
				}
				log.Println("	reader() Answer OK")
				if err = pc().SetLocalDescription(answer); err != nil {
					panic(err)
				}
				log.Println("	reader() SetLocalDescription OK")
				response, err := json.Marshal(*peerConnection.LocalDescription())
				if err != nil {
					panic(err)
				}
				log.Println("	reader() marshal OK")
				if err = ws.WriteMessage(1, response); err != nil {
					log.Println(err)
					panic(err)
				}
				log.Println("	reader() write OK")
			}
			if(*msg.Type == "answer") {
				log.Println("	reader() ANSWER")
				var answer webrtc.SessionDescription
				answer.Type = webrtc.SDPTypeAnswer
				answer.SDP = *msg.Sdp
				if err = pc().SetRemoteDescription(answer); err != nil {
					panic(err)
				}
				log.Println("	reader() SetRemoteDescription OK")
			}
		}
		if (msg.Candidate != nil) {
			log.Println("	reader() CANDIDATE")
			if err := pc().AddICECandidate(webrtc.ICECandidateInit{Candidate: string(*msg.Candidate),SDPMid: msg.SdpMid, SDPMLineIndex: msg.SdpMLineIndex, UsernameFragment: msg.UsernameFragment }); err != nil {
				//panic(err)
			}
			log.Println("	reader() AddICECandidate OK")	
		}
	}
}

func wsEndpoint(w http.ResponseWriter, r *http.Request) {
	upgrader.CheckOrigin = func(r *http.Request) bool { return true }
	var err error
	ws, err = upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
	}
	log.Println("Client Connected")
	reader(ws)
}

func main() {
	rtspUrls[0] = "rtsp://192.168.1.1:7447/qMClkZsOqWhXXAD3"
	rtspUrls[1] = "rtsp://192.168.1.1:7447/Q6QouyweGzbctqN4"
	rtspUrls[2] = "rtsp://192.168.1.1:7447/ecMYte8lT6YVzmIs"
	
	http.Handle("/", http.FileServer(http.Dir("./static")))
	http.HandleFunc("/addTrack", addTrack)
	http.HandleFunc("/ws", wsEndpoint)
	fmt.Println("Open http://:8080 to access")
	panic(http.ListenAndServeTLS(":8080", "/home/karlis/black.ogsts.eu/fullchain1.pem","/home/karlis/black.ogsts.eu/privkey1.pem", nil))
}
