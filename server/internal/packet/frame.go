package packet

import (
	"encoding/binary"
)

const Magic uint32 = 0x33445331

const MaxPayload = 1400

type Header struct {
	Magic       uint32
	FrameID     uint32
	ChunkID     uint16
	TotalChunks uint16
}

func (h *Header) Encode(buf []byte) {
	binary.BigEndian.PutUint32(buf[0:4], h.Magic)
	binary.BigEndian.PutUint32(buf[4:8], h.FrameID)
	binary.BigEndian.PutUint16(buf[8:10], h.ChunkID)
	binary.BigEndian.PutUint16(buf[10:12], h.TotalChunks)
}

func (h *Header) Decode(buf []byte) {
	h.Magic = binary.BigEndian.Uint32(buf[0:4])
	h.FrameID = binary.BigEndian.Uint32(buf[4:8])
	h.ChunkID = binary.BigEndian.Uint16(buf[8:10])
	h.TotalChunks = binary.BigEndian.Uint16(buf[10:12])
}
