package transport

import (
	"3dsstreaming/internal/packet"
	"fmt"
	"log"
	"net"
	"sync/atomic"

	lz4 "github.com/pierrec/lz4/v4"
)

type Sender struct {
	conn *net.UDPConn
	dest *net.UDPAddr
	frameID atomic.Uint32
	lz4c lz4.Compressor
	lz4buf []byte
}

func NewSender(clientAddr, localAddr string) (*Sender, error){
	dest, err := net.ResolveUDPAddr("udp", clientAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve client %s: %w", clientAddr, err)
	}


	local, err := net.ResolveUDPAddr("udp", localAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve local %s: %w", localAddr, err)
	}

	conn, err := net.ListenUDP("udp", local)
	if err != nil {
		return nil, fmt.Errorf("listen udp %s: %w", localAddr, err)
	}

	log.Printf("transport: sending to %s from %s", dest, conn.LocalAddr())

	return &Sender{
		conn: conn,
		dest: dest,
		frameID: atomic.Uint32{},
	}, nil
}


func (s *Sender) nextFrameID() uint32 {
	return s.frameID.Add(1) -1
}


func (s *Sender) SendFrame(data []byte, width, height uint16) error {
	// LZ4-compress the raw YUV frame before chunking. The client derives
	// the uncompressed size from width*height*3/2 in the header.
	bound := lz4.CompressBlockBound(len(data))
	if cap(s.lz4buf) < bound {
		s.lz4buf = make([]byte, bound)
	}
	s.lz4buf = s.lz4buf[:bound]
	n, err := s.lz4c.CompressBlock(data, s.lz4buf)
	if err != nil {
		return fmt.Errorf("lz4 compress: %w", err)
	}
	if n == 0 {
		// Should be unreachable: dst is sized to CompressBlockBound, so
		// the encoder always produces a valid block.
		return fmt.Errorf("lz4 compress: produced empty block for %d-byte input", len(data))
	}
	payload := s.lz4buf[:n]

	frameID := s.nextFrameID()
	total := (len(payload) + packet.MaxPayload - 1) / packet.MaxPayload
	if total == 0 {
		total = 1
	}

	buf := make([]byte, 16 + packet.MaxPayload)

	for i := 0; i < total; i++ {
		start := i * packet.MaxPayload
		end := start + packet.MaxPayload
		if end > len(payload) {
			end = len(payload)
		}

		chunkLen := end - start

		hdr := packet.Header{
			Magic:       packet.Magic,
			FrameID:     frameID,
			ChunkID:     uint16(i),
			TotalChunks: uint16(total),
			Width:       width,
			Height:      height,
		}

		hdr.Encode(buf[:16])

		copy(buf[16:], payload[start:end])
		datagram := buf[:16+chunkLen]

		if _, err := s.conn.WriteToUDP(datagram, s.dest); err != nil {
			return fmt.Errorf("send frame %d chunk %d/%d: %w", frameID, i+1, total, err)
		}

	}

	return nil
}


func (s *Sender) Close() error {
	return s.conn.Close()
}
