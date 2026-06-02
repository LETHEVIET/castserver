package sfu

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for local casting
	},
}

type SignalingMessage struct {
	Type      string                    `json:"type"`                // "offer", "answer", "candidate", "error"
	SDP       string                    `json:"sdp,omitempty"`
	Candidate *webrtc.ICECandidateInit `json:"candidate,omitempty"`
}

func (m *Manager) HandlePublish(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("sfu: publish ws upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	log.Printf("sfu: publisher ws connected")

	m.mu.Lock()
	m.closePubLocked()

	pc, err := m.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		m.mu.Unlock()
		log.Printf("sfu: create publisher pc: %v", err)
		return
	}
	m.pubPC = pc
	m.pubActive = true
	m.mu.Unlock()

	if m.onPubChange != nil {
		m.onPubChange(true)
	}

	defer func() {
		m.mu.Lock()
		if m.pubPC == pc {
			m.closePubLocked()
			if m.onPubChange != nil {
				m.onPubChange(false)
			}
		}
		m.mu.Unlock()
		log.Printf("sfu: publisher ws disconnected")
	}()

	// Send gathered candidates to the client
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		candidate := c.ToJSON()
		msg := SignalingMessage{
			Type:      "candidate",
			Candidate: &candidate,
		}
		payload, err := json.Marshal(msg)
		if err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, payload)
	})

	pc.OnTrack(func(remote *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		codec := remote.Codec()
		log.Printf("sfu: publisher track: %s codec=%s", remote.ID(), codec.MimeType)

		m.mu.Lock()
		m.rtpCodec = codec.RTPCodecCapability
		local, err := webrtc.NewTrackLocalStaticRTP(codec.RTPCodecCapability, remote.ID(), remote.StreamID())
		if err != nil {
			m.mu.Unlock()
			log.Printf("sfu: create local track: %v", err)
			return
		}
		m.localTrack = local
		m.mu.Unlock()

		// Drain RTCP
		go func() {
			buf := make([]byte, 1500)
			for {
				if _, _, err := receiver.Read(buf); err != nil {
					return
				}
			}
		}()

		done := make(chan struct{})
		defer close(done)

		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					if m.SubscriberCount() > 0 {
						_ = pc.WriteRTCP([]rtcp.Packet{
							&rtcp.PictureLossIndication{
								MediaSSRC: uint32(remote.SSRC()),
							},
						})
					}
				}
			}
		}()

		for {
			pkt, _, err := remote.ReadRTP()
			if err != nil {
				log.Printf("sfu: publisher RTP read done: %v", err)
				return
			}
			if werr := local.WriteRTP(pkt); werr != nil {
				log.Printf("sfu: local track write: %v", werr)
			}
		}
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("sfu: publisher conn state=%s", state)
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			conn.Close()
		}
	})

	// Message loop
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var msg SignalingMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "offer":
			if err := pc.SetRemoteDescription(webrtc.SessionDescription{
				Type: webrtc.SDPTypeOffer,
				SDP:  msg.SDP,
			}); err != nil {
				log.Printf("sfu: publisher set remote desc: %v", err)
				return
			}

			answer, err := pc.CreateAnswer(nil)
			if err != nil {
				log.Printf("sfu: publisher create answer: %v", err)
				return
			}

			if err := pc.SetLocalDescription(answer); err != nil {
				log.Printf("sfu: publisher set local desc: %v", err)
				return
			}

			ansMsg := SignalingMessage{
				Type: "answer",
				SDP:  answer.SDP,
			}
			ansPayload, _ := json.Marshal(ansMsg)
			_ = conn.WriteMessage(websocket.TextMessage, ansPayload)

		case "candidate":
			if msg.Candidate != nil {
				if err := pc.AddICECandidate(*msg.Candidate); err != nil {
					log.Printf("sfu: publisher add candidate: %v", err)
				}
			}
		}
	}
}

func (m *Manager) HandleSubscribe(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("sfu: subscribe ws upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	id := r.RemoteAddr
	log.Printf("sfu: subscriber[%s] ws connected", id)

	m.mu.Lock()
	if !m.pubActive || m.localTrack == nil {
		m.mu.Unlock()
		log.Printf("sfu: subscribe[%s] rejected: no active stream", id)
		msg := SignalingMessage{Type: "error", SDP: "no active stream"}
		payload, _ := json.Marshal(msg)
		_ = conn.WriteMessage(websocket.TextMessage, payload)
		return
	}

	pc, err := m.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		m.mu.Unlock()
		log.Printf("sfu: subscriber[%s] create pc: %v", id, err)
		return
	}

	sender, err := pc.AddTrack(m.localTrack)
	if err != nil {
		pc.Close()
		m.mu.Unlock()
		log.Printf("sfu: subscriber[%s] add track: %v", id, err)
		return
	}

	m.subPCs[id] = pc
	m.subCount.Add(1)
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		if m.subPCs[id] == pc {
			delete(m.subPCs, id)
			m.subCount.Add(-1)
		}
		m.mu.Unlock()
		pc.Close()
		log.Printf("sfu: subscriber[%s] ws disconnected (total=%d)", id, m.subCount.Load())
	}()

	// Drain RTCP from subscriber
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()

	// Send gathered candidates to the client
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		candidate := c.ToJSON()
		msg := SignalingMessage{
			Type:      "candidate",
			Candidate: &candidate,
		}
		payload, err := json.Marshal(msg)
		if err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, payload)
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("sfu: subscriber[%s] conn state=%s", id, state)
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			conn.Close()
		}
	})

	// Message loop
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var msg SignalingMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "offer":
			if err := pc.SetRemoteDescription(webrtc.SessionDescription{
				Type: webrtc.SDPTypeOffer,
				SDP:  msg.SDP,
			}); err != nil {
				log.Printf("sfu: subscriber[%s] set remote desc: %v", id, err)
				return
			}

			answer, err := pc.CreateAnswer(nil)
			if err != nil {
				log.Printf("sfu: subscriber[%s] create answer: %v", id, err)
				return
			}

			if err := pc.SetLocalDescription(answer); err != nil {
				log.Printf("sfu: subscriber[%s] set local desc: %v", id, err)
				return
			}

			ansMsg := SignalingMessage{
				Type: "answer",
				SDP:  answer.SDP,
			}
			ansPayload, _ := json.Marshal(ansMsg)
			_ = conn.WriteMessage(websocket.TextMessage, ansPayload)

		case "candidate":
			if msg.Candidate != nil {
				if err := pc.AddICECandidate(*msg.Candidate); err != nil {
					log.Printf("sfu: subscriber[%s] add candidate: %v", id, err)
				}
			}
		}
	}
}

func HandleStop(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		m.Stop()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}
