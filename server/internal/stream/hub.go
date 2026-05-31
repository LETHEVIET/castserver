// Package stream provides a broadcast hub used to fan out JPEG frames from
// the active cast session to one or more viewer WebSocket subscribers.
//
// There is one hub per server. The cast session publishes frames to it;
// /ws/web subscribers attach and receive copies. Slow subscribers get their
// oldest queued frame dropped — we never block the publisher, since that
// would back-pressure into the encoder.
package stream

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"time"
)

// Hub fans out JPEG frames to N subscribers.
type Hub struct {
	mu     sync.Mutex
	subs   map[chan []byte]struct{}
	bufLen int
}

// NewHub returns a Hub. perSubBuffer is the per-subscriber channel size;
// 4–8 is plenty since we drop on overflow.
func NewHub(perSubBuffer int) *Hub {
	if perSubBuffer < 1 {
		perSubBuffer = 4
	}
	return &Hub{
		subs:   make(map[chan []byte]struct{}),
		bufLen: perSubBuffer,
	}
}

// Subscribe registers a new subscriber and returns its frame channel.
func (h *Hub) Subscribe() chan []byte {
	ch := make(chan []byte, h.bufLen)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// Publish sends frame to every subscriber. Frames are not copied — callers
// must not mutate frame after Publish returns. If a subscriber's channel is
// full, its oldest frame is dropped to make room for the new one.
func (h *Hub) Publish(frame []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- frame:
		default:
			// Drop oldest, then push new. Best-effort; if even that fails
			// (subscriber blocked between the two ops), just drop new.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- frame:
			default:
			}
		}
	}
}

// HasSubscribers reports whether anyone is currently listening.
func (h *Hub) HasSubscribers() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs) > 0
}

// SubscriberCount returns the number of active subscribers.
func (h *Hub) SubscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// PumpJPEGsToHub reads an MJPEG byte stream from r and publishes each
// complete JPEG frame to hub. Returns when r reaches EOF or a parse
// error occurs.
//
// If onFrame is non-nil, it is called after every published frame with the
// frame size in bytes, the timestamp when raw bytes arrived from ffmpeg,
// and the timestamp after the frame was parsed and published. This lets
// callers record transcode and pump latency without the pump package
// needing to import telemetry.
func PumpJPEGsToHub(r io.Reader, hub *Hub, onFrame func(frameBytes int, transcodeAt, pumpDoneAt time.Time)) error {
	const (
		readChunk = 64 * 1024
		maxFrame  = 2 * 1024 * 1024
	)
	soi := []byte{0xFF, 0xD8}
	eoi := []byte{0xFF, 0xD9}

	buf := make([]byte, 0, 128*1024)
	tmp := make([]byte, readChunk)

	for {
		n, err := r.Read(tmp)
		if n > 0 {
			readAt := time.Now()
			buf = append(buf, tmp[:n]...)
			for {
				start := bytes.Index(buf, soi)
				if start < 0 {
					if len(buf) > 1 {
						buf = append(buf[:0], buf[len(buf)-1:]...)
					}
					break
				}
				end := bytes.Index(buf[start+2:], eoi)
				if end < 0 {
					if start > 0 {
						buf = append(buf[:0], buf[start:]...)
					}
					if len(buf) > maxFrame {
						return fmt.Errorf("jpeg frame exceeds %d bytes — stream desynced", maxFrame)
					}
					break
				}
				frameEnd := start + 2 + end + 2
				frame := make([]byte, frameEnd-start)
				copy(frame, buf[start:frameEnd])
				hub.Publish(frame)
				pumpDone := time.Now()
				if onFrame != nil {
					onFrame(len(frame), readAt, pumpDone)
				}
				buf = append(buf[:0], buf[frameEnd:]...)
			}
		}
		if err != nil {
			return err
		}
	}
}
