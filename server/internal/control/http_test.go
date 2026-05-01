package control

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlePlayValidJSON(t *testing.T) {
	h := &Handler{}
	body := `{"source":"rtsp://localhost:8554/test","client_addr":"192.168.1.42:8001"}`
	req := httptest.NewRequest(http.MethodPost, "/play", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandlePlay(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp PlayResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Status != "playing" {
		t.Errorf("expected status=playing, got %s", resp.Status)
	}
}

func TestHandlePlayMissingSource(t *testing.T) {
	h := &Handler{}
	body := `{"client_addr":"192.168.1.42:8001"}`
	req := httptest.NewRequest(http.MethodPost, "/play", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandlePlay(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandlePlayUnknownField(t *testing.T) {
	h := &Handler{}
	body := `{"source":"rtsp://x","client_addr":"a:1","typo_field":123}`
	req := httptest.NewRequest(http.MethodPost, "/play", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.HandlePlay(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("DisallowUnknownFields should reject unknown fields, got %d", rec.Code)
	}
}
func TestHandleStats(t *testing.T) {
	h := &Handler{}
	h.IncrementFrames(10)
	h.IncrementNALs(42)

	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	rec := httptest.NewRecorder()

	h.HandleStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var resp StatsResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if resp.FramesSent != 10 {
		t.Errorf("frames_sent: expected 10, got %d", resp.FramesSent)
	}
	if resp.NALsSent != 42 {
		t.Errorf("nals_sent: expected 42, got %d", resp.NALsSent)
	}
}
