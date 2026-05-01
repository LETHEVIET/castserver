package transport

import (
	"3dsstreaming/internal/packet"
	"fmt"
	"log"
	"net"
	"sync/atomic"
)

type Sender struct {
	conn *net.UDPConn
	dest *net.UDPAddr
	frameID atomic.Uint32
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


func (s *Sender) SendFrame(data []byte) error {
	frameID := s.nextFrameID()
	total := (len(data) + packet.MaxPayload - 1) / packet.MaxPayload
	if total == 0 {
		total = 1
	}

	buf := make([]byte, 12 + packet.MaxPayload)

	for i := 0; i < total; i++ {
		start := i * packet.MaxPayload
		end := start + packet.MaxPayload
		if end > len(data) {
			end = len(data)
		}

		chunkLen := end - start

		hdr := packet.Header{
			Magic: packet.Magic,
			FrameID: frameID,
			ChunkID: uint16(i),
			TotalChunks: uint16(total),
		}

		hdr.Encode(buf[:12])

		copy(buf[12:], data[start:end])
		datagram := buf[:12+chunkLen]

		if _, err := s.conn.WriteToUDP(datagram, s.dest); err != nil {
			return fmt.Errorf("send frame %d chunk %d/%d: %w", frameID, i+1, total, err)
		}
	}

	return nil
}


func (s *Sender) Close() error {
	return s.conn.Close()
}
