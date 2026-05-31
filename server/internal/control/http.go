package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"

	"3dsstreaming/internal/stream"
)

// PlayRequest is the JSON body for POST /play.
// Source is optional when the server is started with a -source flag.
// Target selects the output sink: "udp" (3DS) or "web" (browser via /ws/web).
// Defaults to "udp" when omitted, for back-compat with the 3DS client.
type PlayRequest struct {
	Source     string `json:"source"`
	Target     string `json:"target"`
	ClientAddr string `json:"client_addr"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	FPS        int    `json:"fps"`
	Quality    int    `json:"quality"` // ffmpeg -q:v for target=web (2..31; default 8)
}

// Target values accepted by /play and /ws/cast.
const (
	TargetUDP = "udp"
	TargetWeb = "web"
)

// PlayResponse is returned after a successful /play.
type PlayResponse struct {
	Status string `json:"status"`
}

// StatsResponse is returned by GET /stats.
type StatsResponse struct {
	FramesSent uint64 `json:"frames_sent"`
	NALsSent   uint64 `json:"nals_sent"`
	ClientAddr string `json:"client_addr"`
}

// ExtractRequest asks the server to resolve a YouTube (or other service)
// URL into a direct video stream URL via yt-dlp.
type ExtractRequest struct {
	URL string `json:"url"`
}

// ExtractResponse returns the resolved direct stream URL.
type ExtractResponse struct {
	DirectURL string `json:"direct_url"`
	Error     string `json:"error,omitempty"`
}

// Handler manages the HTTP control API. It holds the mutable state
// that is shared between endpoints (stats counters, current ingest context,
// and the broadcast hub fed to web-client subscribers).
type Handler struct {
	framesSent atomic.Uint64
	nalsSent   atomic.Uint64
	mu         sync.RWMutex
	clientAddr string
	sourceURL  string // server-wide default source (from -source flag)
	width      int
	height     int
	fps        int

	// Active session — only one at a time.
	sessionMu     sync.Mutex
	sessionCancel context.CancelFunc
	sessionTarget string // "udp" | "web" | ""

	hub *stream.Hub
}

// NewHandler returns a fresh Handler with an initialized Hub.
func NewHandler() *Handler {
	return &Handler{hub: stream.NewHub(8)}
}

// Hub returns the broadcast hub used to fan out JPEG frames to /ws/web subs.
func (h *Handler) Hub() *stream.Hub { return h.hub }

// ErrSessionActive is returned by AcquireSession when a session is running.
var ErrSessionActive = errors.New("a stream session is already active — POST /stop first")

// AcquireSession claims the single active-session slot. Returns a context
// whose cancel function tears the session down. Caller must call
// ReleaseSession on its own goroutine when the pipeline ends.
func (h *Handler) AcquireSession(target string) (context.Context, context.CancelFunc, error) {
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()
	if h.sessionCancel != nil {
		return nil, nil, ErrSessionActive
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.sessionCancel = cancel
	h.sessionTarget = target
	return ctx, cancel, nil
}

// ReleaseSession clears the active-session slot. Safe to call multiple times.
func (h *Handler) ReleaseSession() {
	h.sessionMu.Lock()
	if h.sessionCancel != nil {
		h.sessionCancel = nil
		h.sessionTarget = ""
	}
	h.sessionMu.Unlock()
}

// StopSession cancels and clears the active session, if any.
func (h *Handler) StopSession() bool {
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()
	if h.sessionCancel == nil {
		return false
	}
	h.sessionCancel()
	h.sessionCancel = nil
	h.sessionTarget = ""
	return true
}

// SessionTarget returns the active session's target ("" if none).
func (h *Handler) SessionTarget() string {
	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()
	return h.sessionTarget
}

// SetClientAddr sets the current client address (empty string when idle).
func (h *Handler) SetClientAddr(addr string) {
	h.mu.Lock()
	h.clientAddr = addr
	h.mu.Unlock()
}

// GetClientAddr returns the current client address.
func (h *Handler) GetClientAddr() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.clientAddr
}

// SetSourceURL sets the server-wide default source URL.
func (h *Handler) SetSourceURL(url string) {
	h.mu.Lock()
	h.sourceURL = url
	h.mu.Unlock()
}

// GetSourceURL returns the server-wide default source URL.
func (h *Handler) GetSourceURL() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sourceURL
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

func (h *Handler) HandlePlay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req PlayRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if req.ClientAddr == "" {
		http.Error(w, "client_addr is required", http.StatusBadRequest)
		return
	}

	// If the request doesn't specify a source, fall back to the server's default.
	source := req.Source
	if source == "" {
		source = h.GetSourceURL()
		if source == "" {
			http.Error(w, "source is required (no server default configured)", http.StatusBadRequest)
			return
		}
	}

	// TODO in main.go wiring: create context, sender, channel, start gorountine

	// Default resolution / fps if not specified.
	width, height, fps := req.Width, req.Height, req.FPS
	if width <= 0 || height <= 0 {
		width, height = 256, 192
	}
	if fps <= 0 {
		fps = 15
	}
	h.SetStreamConfig(width, height, fps)

	log.Printf("control: play requested - source=%s client=%s %dx%d@%d", source, req.ClientAddr, width, height, fps)
	h.SetClientAddr(req.ClientAddr)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PlayResponse{Status: "playing"})
}

func (h *Handler) HandleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// TODO in main.go wiring: cancel ingest context

	addr := h.GetClientAddr()
	log.Printf("control: stop requested - client=%s", addr)
	h.SetClientAddr("")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PlayResponse{Status: "stopped"})
}


// HandleStats reports the current ingest statistics.
func (h *Handler) HandleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	resp := StatsResponse{
		FramesSent: h.framesSent.Load(),
		NALsSent:   h.nalsSent.Load(),
		ClientAddr: h.GetClientAddr(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandleKeyframe requests an immediate IDR frame from the encoder.
func (h *Handler) HandleKeyframe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// TODO in main.go wiring: signal ffmpeg to produce a keyframe
	// The ingest module will provide a force_keyframe channel or function

	log.Printf("control: keyframe requested")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PlayResponse{Status: "keyframe requested"})
}

// IncrementFrames adds n to the frames_sent counter.
func (h *Handler) IncrementFrames(n uint64) {
	h.framesSent.Add(n)
}

// IncrementNALs adds n to the nals_sent counter.
func (h *Handler) IncrementNALs(n uint64) {
	h.nalsSent.Add(n)
}

// HandleExtract resolves a service URL (YouTube, etc.) into a direct
// stream URL using yt-dlp, falling back to youtube-dl.
func (h *Handler) HandleExtract(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ExtractRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if req.URL == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}

	// Pick the best available extractor.
	extractor := "yt-dlp"
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		if _, err := exec.LookPath("youtube-dl"); err != nil {
			http.Error(w, "yt-dlp or youtube-dl not found in PATH", http.StatusServiceUnavailable)
			return
		}
		extractor = "youtube-dl"
	}

	// Use best[height<=480] to get a lower-res stream that ffmpeg can
	// downscale to 256x192 smoothly without burning CPU.
	cmd := exec.CommandContext(r.Context(), extractor,
		"-f", "best[height<=480]",
		"-g",
		req.URL,
	)
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		log.Printf("extract: %s failed: %v", extractor, err)
		http.Error(w, fmt.Sprintf("Extraction failed: %v", err), http.StatusBadGateway)
		return
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		http.Error(w, "No stream URL found", http.StatusBadGateway)
		return
	}

	directURL := lines[0]
	log.Printf("extract: resolved %s -> %s", req.URL, directURL)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ExtractResponse{DirectURL: directURL})
}
