package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"

	"castserver/internal/stream"
	"castserver/internal/telemetry"
)

// Preset is a named set of encoding parameters.
type Preset struct {
	Name          string `json:"name"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	FPS           int    `json:"fps"`
	Bitrate       int    `json:"bitrate"`      // kbps, for MediaRecorder videoBitsPerSecond
	JPEGQuality   int    `json:"jpeg_quality"`  // ffmpeg -q:v (2..31, lower=sharper)
	Scaler        string `json:"scaler"`        // ffmpeg scale flag (lanczos, fast_bilinear, etc.)
	ChunkMS       int    `json:"chunk_ms"`      // MediaRecorder start() interval in ms
	HardwareAccel bool   `json:"hardware_accel"`
}

// Presets returns the built-in preset definitions.
func Presets() []Preset {
	return []Preset{
		{Name: "Low Latency", Width: 854, Height: 480, FPS: 20, Bitrate: 1000, JPEGQuality: 10, Scaler: "fast_bilinear", ChunkMS: 50, HardwareAccel: false},
		{Name: "Balanced", Width: 1280, Height: 720, FPS: 30, Bitrate: 2000, JPEGQuality: 6, Scaler: "lanczos", ChunkMS: 80, HardwareAccel: false},
		{Name: "High Quality", Width: 1920, Height: 1080, FPS: 60, Bitrate: 8000, JPEGQuality: 3, Scaler: "lanczos", ChunkMS: 100, HardwareAccel: false},
		{Name: "Native (No Scaling)", Width: 0, Height: 0, FPS: 60, Bitrate: 6000, JPEGQuality: 6, Scaler: "none", ChunkMS: 30, HardwareAccel: false},
		{Name: "NVIDIA Native (GPU)", Width: 0, Height: 0, FPS: 60, Bitrate: 6000, JPEGQuality: 6, Scaler: "none", ChunkMS: 30, HardwareAccel: true},
		{Name: "Custom", Width: 0, Height: 0, FPS: 0, Bitrate: 0, JPEGQuality: 0, Scaler: "lanczos", ChunkMS: 100, HardwareAccel: false},
	}
}

// StatsResponse is returned by GET /stats.
type StatsResponse struct {
	FramesPublished uint64 `json:"frames_published"`
	WebSubscribers  int    `json:"web_subscribers"`
	SessionActive   bool   `json:"session_active"`
}

// Handler manages HTTP control state shared between endpoints.
type Handler struct {
	framesPub atomic.Uint64

	mu     sync.RWMutex
	width  int
	height int
	fps    int

	sessionMu  sync.Mutex
	sessionCtx context.CancelFunc

	hub     *stream.Hub
	latency *telemetry.Tracker
}

// NewHandler returns a fresh Handler with an initialized Hub.
func NewHandler() *Handler {
	return &Handler{
		hub:     stream.NewHub(2),
		latency: telemetry.NewTracker(128),
	}
}

// Hub returns the broadcast hub used to fan out JPEG frames to /ws/web subs.
func (h *Handler) Hub() *stream.Hub { return h.hub }

// Latency returns the pipeline latency tracker.
func (h *Handler) Latency() *telemetry.Tracker { return h.latency }

// ErrSessionActive is returned by AcquireSession when a session is running.
var ErrSessionActive = errors.New("a cast session is already active — stop it first")

// AcquireSession claims the single active-session slot. Returns a context
// whose cancel function tears the session down.
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

// ReleaseSession clears the active-session slot. Safe to call multiple times.
func (h *Handler) ReleaseSession() {
	h.sessionMu.Lock()
	if h.sessionCtx != nil {
		h.sessionCtx = nil
	}
	h.sessionMu.Unlock()
}

// StopSession cancels and clears the active session, if any.
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

// IsSessionActive reports whether a cast session is currently running.
func (h *Handler) IsSessionActive() bool {
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()
	return h.sessionCtx != nil
}

// SetStreamConfig stores the current stream dimensions and frame rate.
func (h *Handler) SetStreamConfig(width, height, fps int) {
	h.mu.Lock()
	h.width = width
	h.height = height
	h.fps = fps
	h.mu.Unlock()
}

// GetStreamConfig returns the current stream dimensions and frame rate.
func (h *Handler) GetStreamConfig() (width, height, fps int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.width, h.height, h.fps
}

// IncrementFrames adds n to the frames_published counter.
func (h *Handler) IncrementFrames(n uint64) {
	h.framesPub.Add(n)
}

// HandlePresets returns the preset definitions.
func (h *Handler) HandlePresets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(Presets())
}

// HandleStats reports current session statistics.
func (h *Handler) HandleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	resp := StatsResponse{
		FramesPublished: h.framesPub.Load(),
		WebSubscribers:  h.hub.SubscriberCount(),
		SessionActive:   h.IsSessionActive(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandlePresetsLookup looks up a preset by name and returns its parameters.
// Used internally by the cast handler. Returns the preset and true, or
// the Balanced preset and false if not found.
func LookupPreset(name string) (Preset, bool) {
	for _, p := range Presets() {
		if p.Name == name {
			return p, true
		}
	}
	// Default to Native (No Scaling).
	for _, p := range Presets() {
		if p.Name == "Native (No Scaling)" {
			return p, false
		}
	}
	return Preset{Name: "Native (No Scaling)", Width: 0, Height: 0, FPS: 60, Bitrate: 6000, JPEGQuality: 6, Scaler: "none", ChunkMS: 30}, false
}
