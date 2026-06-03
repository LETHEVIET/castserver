package sfu

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

const (
	pingPeriod   = 30 * time.Second
	pongWait     = 60 * time.Second
	wsMaxMsgSize = 32768
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type SignalingMessage struct {
	Type      string                   `json:"type"`
	SDP       string                   `json:"sdp,omitempty"`
	Candidate *webrtc.ICECandidateInit `json:"candidate,omitempty"`
}

func (m *Manager) newPCConfig() webrtc.Configuration {
	cfg := webrtc.Configuration{}
	if len(m.iceServers) > 0 {
		cfg.ICEServers = m.iceServers
	}
	return cfg
}

func (m *Manager) startPing(conn *websocket.Conn) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

func (m *Manager) HandlePublish(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("publish ws upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	conn.SetReadLimit(wsMaxMsgSize)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	stopPing := m.startPing(conn)
	defer stopPing()

	slog.Info("publisher ws connected")

	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "realtime"
	}

	m.mu.Lock()
	m.closePubLocked()
	m.streamMode = mode

	pc, err := m.api.NewPeerConnection(m.newPCConfig())
	if err != nil {
		m.mu.Unlock()
		slog.Error("create publisher pc", "error", err)
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
		slog.Info("publisher ws disconnected")
	}()

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
		slog.Info("publisher track received",
			"track_id", remote.ID(),
			"codec", codec.MimeType,
			"kind", remote.Kind().String(),
		)

		m.mu.Lock()
		m.rtpCodec = codec.RTPCodecCapability
		local, err := webrtc.NewTrackLocalStaticRTP(codec.RTPCodecCapability, remote.ID(), remote.StreamID())
		if err != nil {
			m.mu.Unlock()
			slog.Error("create local track", "error", err)
			return
		}

		if remote.Kind() == webrtc.RTPCodecTypeVideo {
			m.localVideoTrack = local
		} else if remote.Kind() == webrtc.RTPCodecTypeAudio {
			m.localAudioTrack = local
		}

		for id, subPC := range m.subPCs {
			for _, sender := range subPC.GetSenders() {
				if sender.Track() != nil && sender.Track().Kind() == remote.Kind() {
					slog.Info("replacing track for subscriber",
						"kind", remote.Kind().String(),
						"subscriber", id,
					)
					if err := sender.ReplaceTrack(local); err != nil {
						slog.Error("failed to replace track for subscriber",
							"kind", remote.Kind().String(),
							"subscriber", id,
							"error", err,
						)
					}
				}
			}
		}
		m.mu.Unlock()

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
				slog.Info("publisher RTP read done", "error", err)
				return
			}
			if werr := local.WriteRTP(pkt); werr != nil {
				slog.Error("local track write", "error", werr)
			}
		}
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		slog.Info("publisher conn state", "state", state.String())
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			conn.Close()
		}
	})

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
				slog.Error("publisher set remote desc", "error", err)
				return
			}

			answer, err := pc.CreateAnswer(nil)
			if err != nil {
				slog.Error("publisher create answer", "error", err)
				return
			}

			if err := pc.SetLocalDescription(answer); err != nil {
				slog.Error("publisher set local desc", "error", err)
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
					slog.Error("publisher add candidate", "error", err)
				}
			}
		}
	}
}

func (m *Manager) HandleSubscribe(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("subscribe ws upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	conn.SetReadLimit(wsMaxMsgSize)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	stopPing := m.startPing(conn)
	defer stopPing()

	id := uuid.NewString()
	slog.Info("subscriber ws connected", "subscriber", id)

	m.mu.Lock()
	if !m.pubActive || (m.localVideoTrack == nil && m.localAudioTrack == nil) {
		m.mu.Unlock()
		slog.Info("subscriber rejected: no active stream", "subscriber", id)
		msg := SignalingMessage{Type: "error", SDP: "no active stream"}
		payload, _ := json.Marshal(msg)
		_ = conn.WriteMessage(websocket.TextMessage, payload)
		return
	}

	pc, err := m.api.NewPeerConnection(m.newPCConfig())
	if err != nil {
		m.mu.Unlock()
		slog.Error("create subscriber pc", "subscriber", id, "error", err)
		return
	}

	var addedTrack bool
	if m.localVideoTrack != nil {
		sender, err := pc.AddTrack(m.localVideoTrack)
		if err != nil {
			pc.Close()
			m.mu.Unlock()
			slog.Error("add video track for subscriber", "subscriber", id, "error", err)
			return
		}
		addedTrack = true
		go func() {
			buf := make([]byte, 1500)
			for {
				if _, _, err := sender.Read(buf); err != nil {
					return
				}
			}
		}()
	}

	if m.localAudioTrack != nil {
		sender, err := pc.AddTrack(m.localAudioTrack)
		if err != nil {
			pc.Close()
			m.mu.Unlock()
			slog.Error("add audio track for subscriber", "subscriber", id, "error", err)
			return
		}
		addedTrack = true
		go func() {
			buf := make([]byte, 1500)
			for {
				if _, _, err := sender.Read(buf); err != nil {
					return
				}
			}
		}()
	}

	if !addedTrack {
		pc.Close()
		m.mu.Unlock()
		slog.Info("subscriber rejected: no tracks", "subscriber", id)
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
		slog.Info("subscriber ws disconnected", "subscriber", id, "total", m.subCount.Load())
	}()

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
		slog.Info("subscriber conn state", "subscriber", id, "state", state.String())
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			conn.Close()
		}
	})

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
				slog.Error("subscriber set remote desc", "subscriber", id, "error", err)
				return
			}

			answer, err := pc.CreateAnswer(nil)
			if err != nil {
				slog.Error("subscriber create answer", "subscriber", id, "error", err)
				return
			}

			if err := pc.SetLocalDescription(answer); err != nil {
				slog.Error("subscriber set local desc", "subscriber", id, "error", err)
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
					slog.Error("subscriber add candidate", "subscriber", id, "error", err)
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
