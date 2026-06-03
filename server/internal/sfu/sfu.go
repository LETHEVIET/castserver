package sfu

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
)

type Manager struct {
	mu  sync.Mutex
	api *webrtc.API

	iceServers []webrtc.ICEServer

	pubPC           *webrtc.PeerConnection
	localVideoTrack *webrtc.TrackLocalStaticRTP
	localAudioTrack *webrtc.TrackLocalStaticRTP
	rtpCodec        webrtc.RTPCodecCapability
	pubActive       bool
	streamMode      string

	subPCs   map[string]*webrtc.PeerConnection
	subCount atomic.Int32

	onPubChange func(active bool)
}

func (m *Manager) GetMode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.streamMode
}

func (m *Manager) SetMode(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streamMode = mode
}

func NewManager(iceServers []webrtc.ICEServer) *Manager {
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
		slog.Warn("register default codecs", "error", err)
	}

	i := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		slog.Warn("register default interceptors", "error", err)
	}

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(m),
		webrtc.WithSettingEngine(s),
		webrtc.WithInterceptorRegistry(i),
	)

	return &Manager{
		api:        api,
		iceServers: iceServers,
		subPCs:     make(map[string]*webrtc.PeerConnection),
	}
}

func (m *Manager) SetICEServers(servers []webrtc.ICEServer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.iceServers = servers
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
	m.localVideoTrack = nil
	m.localAudioTrack = nil
	m.streamMode = ""
	m.pubActive = false
}
