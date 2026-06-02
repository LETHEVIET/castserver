package sfu

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

type Manager struct {
	mu    sync.Mutex
	api   *webrtc.API

	pubPC      *webrtc.PeerConnection
	localTrack *webrtc.TrackLocalStaticRTP
	rtpCodec   webrtc.RTPCodecCapability
	pubActive  bool

	subPCs      map[string]*webrtc.PeerConnection
	subCount    atomic.Int32

	onPubChange func(active bool)
}

func NewManager() *Manager {
	s := webrtc.SettingEngine{}

	s.SetNetworkTypes([]webrtc.NetworkType{
		webrtc.NetworkTypeUDP4,
		webrtc.NetworkTypeUDP6,
	})

	s.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)

	s.SetHostAcceptanceMinWait(0)
	s.SetSrflxAcceptanceMinWait(0)
	s.SetPrflxAcceptanceMinWait(0)
	s.SetRelayAcceptanceMinWait(0)

	// Let pion use standard, robust default timeouts for keepalive and connection status checks

	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		log.Printf("sfu: register default codecs: %v", err)
	}

	// Create and register the default interceptor registry (enables NACK packet cache, PLI, and bandwidth stats)
	i := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		log.Printf("sfu: register default interceptors: %v", err)
	}

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(m),
		webrtc.WithSettingEngine(s),
		webrtc.WithInterceptorRegistry(i),
	)

	return &Manager{
		api:     api,
		subPCs:  make(map[string]*webrtc.PeerConnection),
	}
}

func (m *Manager) SetPubChangeCallback(fn func(active bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onPubChange = fn
}

func (m *Manager) IsActive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pubActive
}

func (m *Manager) SubscriberCount() int {
	return int(m.subCount.Load())
}

func (m *Manager) OnPublishOffer(offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.closePubLocked()

	pc, err := m.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("new publisher pc: %w", err)
	}

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

		// Dynamic on-the-fly track replacement for all current subscribers!
		for id, subPC := range m.subPCs {
			for _, sender := range subPC.GetSenders() {
				if sender.Track() != nil {
					log.Printf("sfu: dynamically replacing stream track for subscriber[%s]", id)
					if err := sender.ReplaceTrack(local); err != nil {
						log.Printf("sfu: failed to replace stream track for subscriber[%s]: %v", id, err)
					}
				}
			}
		}
		m.mu.Unlock()

		// Read incoming RTCP packets to drain the buffer and allow interceptors (NACK, PLI) to work
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

		// Ask the publisher for keyframe periodically so new subscribers get a keyframe quickly.
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
		if state == webrtc.PeerConnectionStateFailed ||
			state == webrtc.PeerConnectionStateClosed {
			m.mu.Lock()
			if m.pubPC == pc {
				m.closePubLocked()
				if m.onPubChange != nil {
					m.onPubChange(false)
				}
			}
			m.mu.Unlock()
		}
	})

	if err := pc.SetRemoteDescription(offer); err != nil {
		pc.Close()
		return webrtc.SessionDescription{}, fmt.Errorf("set remote desc: %w", err)
	}

	log.Printf("sfu: creating answer (ICE gathering may take a moment)...")
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return webrtc.SessionDescription{}, fmt.Errorf("create answer: %w", err)
	}

	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		return webrtc.SessionDescription{}, fmt.Errorf("set local desc: %w", err)
	}

	// Wait for ICE gathering to complete so the returned SDP contains all host candidates
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	<-gatherComplete
	log.Printf("sfu: answer created, local candidates gathered")

	m.pubPC = pc
	m.pubActive = true
	if m.onPubChange != nil {
		m.onPubChange(true)
	}

	log.Printf("sfu: publisher connected")
	return *pc.LocalDescription(), nil
}

func (m *Manager) OnSubscribeOffer(id string, offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.pubActive || m.localTrack == nil {
		return webrtc.SessionDescription{}, fmt.Errorf("no active stream")
	}

	if old, ok := m.subPCs[id]; ok {
		old.Close()
	}

	pc, err := m.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("new subscriber pc: %w", err)
	}

	sender, err := pc.AddTrack(m.localTrack)
	if err != nil {
		pc.Close()
		return webrtc.SessionDescription{}, fmt.Errorf("add track: %w", err)
	}

	// Read incoming RTCP packets from the subscriber (NACK, PLI, etc.) so interceptors can process them
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()

	if err := pc.SetRemoteDescription(offer); err != nil {
		pc.Close()
		return webrtc.SessionDescription{}, fmt.Errorf("set remote desc: %w", err)
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		pc.Close()
		return webrtc.SessionDescription{}, fmt.Errorf("create answer: %w", err)
	}

	if err := pc.SetLocalDescription(answer); err != nil {
		pc.Close()
		return webrtc.SessionDescription{}, fmt.Errorf("set local desc: %w", err)
	}

	// Wait for ICE gathering to complete so the returned SDP contains all host candidates
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	<-gatherComplete

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("sfu: subscriber[%s] conn state=%s", id, state)
		if state == webrtc.PeerConnectionStateFailed ||
			state == webrtc.PeerConnectionStateClosed {
			m.mu.Lock()
			if m.subPCs[id] == pc {
				delete(m.subPCs, id)
				m.subCount.Add(-1)
			}
			m.mu.Unlock()
			pc.Close()
		}
	})

	m.subPCs[id] = pc
	m.subCount.Add(1)

	log.Printf("sfu: subscriber[%s] connected (total=%d)", id, m.subCount.Load())
	return *pc.LocalDescription(), nil
}

func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closePubLocked()
	for id, pc := range m.subPCs {
		pc.Close()
		delete(m.subPCs, id)
		m.subCount.Add(-1)
	}
}

func (m *Manager) closePubLocked() {
	if m.pubPC != nil {
		m.pubPC.Close()
		m.pubPC = nil
	}
	m.localTrack = nil
	m.pubActive = false
}

func (m *Manager) SubscribeDisconnect(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pc, ok := m.subPCs[id]; ok {
		pc.Close()
		delete(m.subPCs, id)
		m.subCount.Add(-1)
	}
}
