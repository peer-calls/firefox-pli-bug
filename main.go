package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi"
	"github.com/gobuffalo/packr"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v2"
	"nhooyr.io/websocket"
)

func main() {
	pliInterval := flag.Int64("pli-interval", 0, "If set to X, enables sending on PLI packet every X seconds")
	flag.Parse()

	router := chi.NewRouter()

	h := NewMainHandler(*pliInterval)
	router.Mount("/ws", h)
	router.Handle("/*", static("/", packr.NewBox("./static")))

	port := "3000"
	l, err := net.Listen("tcp", net.JoinHostPort("", port))
	addr := l.Addr().(*net.TCPAddr)
	log.Printf("Listening on: %s", addr.String())
	server := &http.Server{Handler: router}
	err = server.Serve(l)
	chkErr(err)
}

func static(prefix string, box packr.Box) http.Handler {
	fileServer := http.FileServer(http.FileSystem(box))
	return http.StripPrefix(prefix, fileServer)
}

func chkErr(err error) {
	if err != nil {
		panic(err)
	}
}

type MainHandler struct {
	pliInterval int64
	peerIds     uint32
	api         *webrtc.API
	peers       map[uint32]*Peer
}

type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type Peer struct {
	id          uint32
	pc          *webrtc.PeerConnection
	localTracks []*webrtc.Track
	negotiate   func()
	mu          sync.Mutex
}

func NewPeer(id uint32, pc *webrtc.PeerConnection, negotiate func()) *Peer {
	return &Peer{
		id:          id,
		pc:          pc,
		negotiate:   negotiate,
		localTracks: make([]*webrtc.Track, 0),
	}
}

type TrackReceiver struct {
	track      *webrtc.Track
	sourcePeer Peer
}

func NewMainHandler(pliInterval int64) *MainHandler {
	var mediaEngine webrtc.MediaEngine
	var settingEngine webrtc.SettingEngine
	settingEngine.SetTrickle(false)
	mediaEngine.RegisterCodec(webrtc.NewRTPOpusCodec(webrtc.DefaultPayloadTypeOpus, 48000))

	rtcpfb := []webrtc.RTCPFeedback{
		webrtc.RTCPFeedback{
			Type: webrtc.TypeRTCPFBNACK,
		},
		webrtc.RTCPFeedback{
			Type:      webrtc.TypeRTCPFBNACK,
			Parameter: "pli",
		},
	}
	mediaEngine.RegisterCodec(webrtc.NewRTPVP8CodecExt(webrtc.DefaultPayloadTypeVP8, 90000, rtcpfb, ""))
	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithSettingEngine(settingEngine),
	)

	return &MainHandler{
		api:         api,
		peers:       map[uint32]*Peer{},
		pliInterval: pliInterval,
	}
}

func deserialize(b []byte) (m Message) {
	err := json.Unmarshal(b, &m)
	chkErr(err)
	return
}

func serializeMessage(typ string, payload interface{}) []byte {
	b, err := json.Marshal(payload)
	chkErr(err)
	m := Message{typ, b}
	return serialize(m)
}

func serialize(m Message) []byte {
	b, err := json.Marshal(m)
	chkErr(err)
	return b
}

func (m *MainHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	chkErr(err)

	id := atomic.AddUint32(&m.peerIds, 1)
	log.Printf("[%d] ws connected", id)

	defer func() {
		c.Close(websocket.StatusInternalError, "")
	}()

	webrtcConfig := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{
			URLs: []string{"stun:stun.l.google.com:19302"},
		}},
	}
	pc, err := m.api.NewPeerConnection(webrtcConfig)
	chkErr(err)
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("[%d] ice connection state change: %s", id, state)
	})

	pc.OnSignalingStateChange(func(state webrtc.SignalingState) {
		log.Printf("[%d] signaling state change: %s", id, state)
	})

	_, err = pc.CreateDataChannel("data", nil)
	chkErr(err)

	send := func(typ string, payload interface{}) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = c.Write(ctx, websocket.MessageText, serializeMessage(typ, payload))
		chkErr(err)
	}
	negotiate := func() {
		offer, err := pc.CreateOffer(nil)
		chkErr(err)
		// log.Printf("[%d] negotiate create offer: %s", id, offer.SDP)

		err = pc.SetLocalDescription(offer)
		chkErr(err)
		send("offer", offer)
	}
	peer := NewPeer(id, pc, negotiate)

	tracksChan := make(chan *webrtc.Track)
	pc.OnTrack(func(remoteTrack *webrtc.Track, receiver *webrtc.RTPReceiver) {
		log.Printf("[%d] OnTrack ssrc: %d", id, remoteTrack.SSRC())
		peer.mu.Lock()
		defer peer.mu.Unlock()

		localTrack, err := pc.NewTrack(remoteTrack.PayloadType(), remoteTrack.SSRC(), "copy:"+remoteTrack.ID(), "copy:"+remoteTrack.Label())
		peer.localTracks = append(peer.localTracks, localTrack)
		chkErr(err)

		var ticker *time.Ticker
		if m.pliInterval > 0 {
			log.Printf("[%d] Starting PLI ticker every %d seconds for track: %d", id, m.pliInterval, remoteTrack.SSRC())
			ticker = time.NewTicker(time.Duration(m.pliInterval) * time.Second)
			go func() {
				for range ticker.C {
					log.Printf("[%d] Sending PLI packet for track (interval): %d", id, remoteTrack.SSRC())
					err := pc.WriteRTCP([]rtcp.Packet{
						&rtcp.PictureLossIndication{
							MediaSSRC: remoteTrack.SSRC(),
						},
					})
					if err != nil {
						log.Printf("[%d] Error sending PLI on interval: %s", id, err)
					}
				}
			}()
		}

		go func() {
			if ticker != nil {
				defer ticker.Stop()
			}
			for {
				rtp, err := remoteTrack.ReadRTP()
				if err != nil {
					log.Printf("[%d] Error reading from remote track: %d: %s", id, remoteTrack.SSRC(), err)
					return
				}

				err = localTrack.WriteRTP(rtp)
				if err != nil && err != io.ErrClosedPipe {
					log.Printf("[%d] Error writing to local track: %d: %s", id, localTrack.SSRC(), err)
					return
				}
			}
		}()

		tracksChan <- localTrack
	})

	addTrack := func(recvPeer *Peer, srcPeer *Peer, track *webrtc.Track) {
		log.Printf("[%d] Add track ssrc %d to peer", recvPeer.id, track.SSRC())
		sender, err := recvPeer.pc.AddTrack(track)
		chkErr(err)
		go func() {
			pkts, err := sender.ReadRTCP()
			if err != nil {
				log.Printf("[%d] Error reading rtcp from sender: %d: %s", recvPeer.id, track.SSRC(), err)
				return
			}
			for _, pkt := range pkts {
				switch pkt.(type) {
				case *rtcp.PictureLossIndication:
					log.Printf("[%d] rtcp packet for track ssrc %d: %T", recvPeer.id, track.SSRC(), pkt)
					err := srcPeer.pc.WriteRTCP([]rtcp.Packet{pkt})
					if err != nil {
						log.Printf("[%d] error writing rtcp packet for track ssrc %d: %s", recvPeer.id, track.SSRC(), err)
						return
					}
				// case *rtcp.ReceiverEstimatedMaximumBitrate:
				// case *rtcp.SourceDescription:
				default:
					log.Printf("[%d] rtcp packet for track ssrc %d: %T", recvPeer.id, track.SSRC(), pkt)
				}
			}
		}()
		go recvPeer.negotiate()
	}

	msgChan := make(chan Message)
	go func() {
		for {
			select {
			case msg := <-msgChan:
				switch msg.Type {
				case "ready":
					for _, otherPeer := range m.peers {
						for _, track := range otherPeer.localTracks {
							addTrack(peer, otherPeer, track)
						}
					}
					m.peers[id] = peer
					negotiate()
				case "answer":
					var answer webrtc.SessionDescription
					err := json.Unmarshal(msg.Payload, &answer)
					chkErr(err)
					// log.Printf("[%d] Got answer: %s", id, answer.SDP)
					err = pc.SetRemoteDescription(answer)
					chkErr(err)
				case "pub":
					pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RtpTransceiverInit{
						Direction: webrtc.RTPTransceiverDirectionRecvonly,
					})
					negotiate()
				default:
					// log.Printf("[%d] Unhandled websocket message type: %s", id, msg.Type)
				}
			case track := <-tracksChan:
				for _, otherPeer := range m.peers {
					if otherPeer.pc != pc {
						addTrack(otherPeer, peer, track)
					}
				}
			}
		}
	}()

	for {
		ctx := context.Background()
		typ, m, err := c.Read(ctx)
		if err != nil {
			log.Printf("[%d] Websocket read error: %s", id, err)
			break
		}
		if typ != websocket.MessageText {
			continue
		}

		msg := deserialize(m)
		msgChan <- msg
	}
}
