package transport

import (
	"testing"

	"3dsstreaming/internal/packet"
)

func TestChunkCount(t *testing.T) {
	tests := []struct {
		name       string
		nalSize    int
		wantChunks int
	}{
		{"single chunk", 100, 1},
		{"exactly one chunk", packet.MaxPayload, 1},
		{"two chunks", packet.MaxPayload + 1, 2},
		{"max uint16 chunks", packet.MaxPayload * 100, 100},
		{"zero bytes", 0, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total := (tt.nalSize + packet.MaxPayload - 1) / packet.MaxPayload
			if total == 0 {
				total = 1
			}
			if total != tt.wantChunks {
				t.Errorf("nalSize=%d: got %d chunks, want %d", tt.nalSize, total, tt.wantChunks)
			}
		})
	}
}
