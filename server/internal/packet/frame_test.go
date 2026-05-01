package packet

import (
	"bytes"
	"testing"
)

func TestHeaderRoundTrip(t *testing.T) {
	h := Header{
		Magic:       Magic,
		FrameID:     42,
		ChunkID:     3,
		TotalChunks: 10,
	}

	var buf [12]byte
	h.Encode(buf[:])

	var decoded Header
	decoded.Decode(buf[:])

	if decoded.Magic != h.Magic {
		t.Errorf("Magic: got 0x%08x, want 0x%08x", decoded.Magic, h.Magic)
	}
	if decoded.FrameID != h.FrameID {
		t.Errorf("FrameID: got %d, want %d", decoded.FrameID, h.FrameID)
	}
	if decoded.ChunkID != h.ChunkID {
		t.Errorf("ChunkID: got %d, want %d", decoded.ChunkID, h.ChunkID)
	}
	if decoded.TotalChunks != h.TotalChunks {
		t.Errorf("TotalChunks: got %d, want %d", decoded.TotalChunks, h.TotalChunks)
	}
}

func TestHeaderEncodeLength(t *testing.T) {
	h := Header{Magic: Magic, FrameID: 1, ChunkID: 0, TotalChunks: 1}
	buf := make([]byte, 12)
	h.Encode(buf)

	// Verify magic bytes in big-endian: 0x33 0x44 0x53 0x31
	want := []byte{0x33, 0x44, 0x53, 0x31, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01}
	if !bytes.Equal(buf, want) {
		t.Errorf("encoded bytes mismatch:\n  got:  %x\n  want: %x", buf, want)
	}
}
