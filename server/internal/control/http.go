package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"castserver/internal/sfu"
)

type Preset struct {
	Name    string `json:"name"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	FPS     int    `json:"fps"`
	Bitrate int    `json:"bitrate"`
}

func Presets() []Preset {
	return []Preset{
		{Name: "Low Latency", Width: 854, Height: 480, FPS: 30, Bitrate: 1000},
		{Name: "Balanced", Width: 1280, Height: 720, FPS: 30, Bitrate: 3000},
		{Name: "High Quality", Width: 1920, Height: 1080, FPS: 60, Bitrate: 8000},
		{Name: "Native", Width: 0, Height: 0, FPS: 60, Bitrate: 50000},
	}
}

type StatsResponse struct {
	FramesPublished uint64 `json:"frames_published"`
	WebSubscribers  int    `json:"web_subscribers"`
	SessionActive   bool   `json:"session_active"`
	StreamMode      string `json:"stream_mode"`
}

type Handler struct {
	framesPub atomic.Uint64

	mu     sync.RWMutex
	width  int
	height int
	fps    int

	sessionMu  sync.Mutex
	sessionCtx context.CancelFunc

	sfu *sfu.Manager

	cachedStats    atomic.Value
	cachedStatsKey atomic.Value
}

func NewHandler() *Handler {
	mgr := sfu.NewManager(nil)
	h := &Handler{
		sfu: mgr,
	}
	mgr.SetPubChangeCallback(func(active bool) {
		if active {
			h.sessionMu.Lock()
			if h.sessionCtx != nil {
				h.sessionCtx()
			}
			ctx, cancel := context.WithCancel(context.Background())
			h.sessionCtx = cancel
			h.sessionMu.Unlock()
			_ = ctx
		} else {
			h.sessionMu.Lock()
			if h.sessionCtx != nil {
				h.sessionCtx()
				h.sessionCtx = nil
			}
			h.sessionMu.Unlock()
		}
	})
	return h
}

func (h *Handler) SFU() *sfu.Manager { return h.sfu }

var ErrSessionActive = errors.New("a cast session is already active — stop it first")

func (h *Handler) AcquireSession() (context.Context, context.CancelFunc, error) {
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()
	if h.sessionCtx != nil {
		return nil, nil, ErrSessionActive
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.sessionCtx = cancel
	return ctx, cancel, nil
}

func (h *Handler) ReleaseSession() {
	h.sessionMu.Lock()
	if h.sessionCtx != nil {
		h.sessionCtx()
		h.sessionCtx = nil
	}
	h.sessionMu.Unlock()
}

func (h *Handler) StopSession() bool {
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()
	if h.sessionCtx == nil {
		return false
	}
	h.sessionCtx()
	h.sessionCtx = nil
	return true
}

func (h *Handler) IsSessionActive() bool {
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()
	return h.sessionCtx != nil
}

func (h *Handler) SetStreamConfig(width, height, fps int) {
	h.mu.Lock()
	h.width = width
	h.height = height
	h.fps = fps
	h.mu.Unlock()
}

func (h *Handler) GetStreamConfig() (width, height, fps int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.width, h.height, h.fps
}

func (h *Handler) IncrementFrames(n uint64) {
	h.framesPub.Add(n)
}

func (h *Handler) buildStatsResponse() []byte {
	key := fmt.Sprintf("%d-%d-%t-%s", h.framesPub.Load(), h.sfu.SubscriberCount(), h.sfu.IsActive(), h.sfu.GetMode())
	if prev := h.cachedStatsKey.Load(); prev != nil && prev.(string) == key {
		if cached := h.cachedStats.Load(); cached != nil {
			return cached.([]byte)
		}
	}
	data, _ := json.Marshal(StatsResponse{
		FramesPublished: h.framesPub.Load(),
		WebSubscribers:  h.sfu.SubscriberCount(),
		SessionActive:   h.sfu.IsActive(),
		StreamMode:      h.sfu.GetMode(),
	})
	h.cachedStatsKey.Store(key)
	h.cachedStats.Store(data)
	return data
}

func (h *Handler) HandlePresets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Presets())
}

func (h *Handler) HandleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(h.buildStatsResponse())
}

func (h *Handler) HandleStatsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	slog.Info("stats stream connected", "remote", r.RemoteAddr)
	for {
		select {
		case <-r.Context().Done():
			slog.Info("stats stream disconnected", "remote", r.RemoteAddr)
			return
		case <-ticker.C:
			data := h.buildStatsResponse()
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func LookupPreset(name string) (Preset, bool) {
	for _, p := range Presets() {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{Name: "Native", Width: 0, Height: 0, FPS: 60, Bitrate: 8000}, false
}
