package control

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

// PlayRequest is the JSON body for POST /play.
// Source is optional when the server is started with a -source flag.
type PlayRequest struct {
	Source     string `json:"source"`
	ClientAddr string `json:"client_addr"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	FPS        int    `json:"fps"`
}

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
// that is shared between endpoints (stats counters, current ingest context).
type Handler struct {
	framesSent atomic.Uint64
	nalsSent   atomic.Uint64
	mu         sync.RWMutex
	clientAddr string
	sourceURL  string // server-wide default source (from -source flag)
	width      int
	height     int
	fps        int
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
