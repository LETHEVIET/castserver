// Package stream provides a broadcast hub used to fan out JPEG frames from
// the active stream session to one or more web-client WebSocket subscribers.
//
// There is one hub per server. The active session (URL ingest or cast)
// publishes frames to it; /ws/web subscribers attach and receive copies.
// Slow subscribers get their oldest queued frame dropped — we never block the
// publisher, since that would back-pressure into the encoder.
package stream

import "sync"

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
