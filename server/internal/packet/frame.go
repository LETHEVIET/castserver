package packet

import "encoding/binary"

const Magic uint32 = 0x33445331

const MaxPayload = 1400

// Header is the 16-byte UDP datagram prefix.
// All fields are big-endian.
type Header struct {
	Magic       uint32
	FrameID     uint32
	ChunkID     uint16
	TotalChunks uint16
	Width       uint16 // stream width in pixels
	Height      uint16 // stream height in pixels
}

func (h *Header) Encode(buf []byte) {
	binary.BigEndian.PutUint32(buf[0:4], h.Magic)
	binary.BigEndian.PutUint32(buf[4:8], h.FrameID)
	binary.BigEndian.PutUint16(buf[8:10], h.ChunkID)
	binary.BigEndian.PutUint16(buf[10:12], h.TotalChunks)
	binary.BigEndian.PutUint16(buf[12:14], h.Width)
	binary.BigEndian.PutUint16(buf[14:16], h.Height)
}

func (h *Header) Decode(buf []byte) {
	h.Magic = binary.BigEndian.Uint32(buf[0:4])
	h.FrameID = binary.BigEndian.Uint32(buf[4:8])
	h.ChunkID = binary.BigEndian.Uint16(buf[8:10])
	h.TotalChunks = binary.BigEndian.Uint16(buf[10:12])
	h.Width = binary.BigEndian.Uint16(buf[12:14])
	h.Height = binary.BigEndian.Uint16(buf[14:16])
}