package control

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
)

// PlayRequest is the JSON body for POST /play.
type PlayRequest struct {
	Source     string `json:"source"`
	ClientAddr string `json:"client_addr"`
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

// Handler manages the HTTP control API. It holds the mutable state
// that is shared between endpoints (stats counters, current ingest context).
type Handler struct {
	framesSent atomic.Uint64
	nalsSent   atomic.Uint64
	mu         sync.RWMutex
	clientAddr string
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

	if req.Source == "" {
		http.Error(w, "source is required", http.StatusBadRequest)
		return
	}

	if req.ClientAddr == "" {
		http.Error(w, "client_addr is required", http.StatusBadRequest)
		return
	}

	// TODO in main.go wiring: create context, sender, channel, start gorountine

	log.Printf("control: play requested - source=%s client=%s", req.Source, req.ClientAddr)
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
