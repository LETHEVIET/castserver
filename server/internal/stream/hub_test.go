package stream

import (
	"sync"
	"testing"
)

func TestHubSubscribeUnsubscribe(t *testing.T) {
	h := NewHub(4)
	if h.SubscriberCount() != 0 {
		t.Error("expected 0 subscribers")
	}

	ch := h.Subscribe()
	if h.SubscriberCount() != 1 {
		t.Error("expected 1 subscriber")
	}

	h.Unsubscribe(ch)
	if h.SubscriberCount() != 0 {
		t.Error("expected 0 subscribers after unsubscribe")
	}
}

func TestHubPublish(t *testing.T) {
	h := NewHub(4)
	ch := h.Subscribe()
	defer h.Unsubscribe(ch)

	frame := []byte{0xFF, 0xD8, 0x01, 0x02, 0xFF, 0xD9}
	h.Publish(frame)

	select {
	case got := <-ch:
		if len(got) != len(frame) {
			t.Errorf("expected %d bytes, got %d", len(frame), len(got))
		}
	default:
		t.Error("expected frame on channel")
	}
}

func TestHubPublishMultipleSubscribers(t *testing.T) {
	h := NewHub(4)
	ch1 := h.Subscribe()
	ch2 := h.Subscribe()
	defer h.Unsubscribe(ch1)
	defer h.Unsubscribe(ch2)

	frame := []byte{0xFF, 0xD8, 0xAA, 0xFF, 0xD9}
	h.Publish(frame)

	for _, ch := range []chan []byte{ch1, ch2} {
		select {
		case got := <-ch:
			if len(got) != len(frame) {
				t.Errorf("expected %d bytes, got %d", len(frame), len(got))
			}
		default:
			t.Error("expected frame on channel")
		}
	}
}

func TestHubDropOldestOnOverflow(t *testing.T) {
	h := NewHub(1) // tiny buffer so overflow happens immediately
	ch := h.Subscribe()
	defer h.Unsubscribe(ch)

	// Fill the buffer.
	first := []byte{1}
	h.Publish(first)

	// Publish second — should drop first and push second.
	second := []byte{2}
	h.Publish(second)

	// Must receive second, not first.
	select {
	case got := <-ch:
		if got[0] != 2 {
			t.Errorf("expected frame [2], got [%d]", got[0])
		}
	default:
		t.Error("expected a frame on channel")
	}
}

func TestHubHasSubscribers(t *testing.T) {
	h := NewHub(4)
	if h.HasSubscribers() {
		t.Error("expected no subscribers")
	}

	ch := h.Subscribe()
	if !h.HasSubscribers() {
		t.Error("expected subscribers after subscribe")
	}

	h.Unsubscribe(ch)
	if h.HasSubscribers() {
		t.Error("expected no subscribers after unsubscribe")
	}
}

func TestHubPublishNoSubscribers(t *testing.T) {
	h := NewHub(4)
	// Should not panic with zero subscribers.
	h.Publish([]byte{0xFF, 0xD8, 0xFF, 0xD9})
}

func TestHubMultipleUnsubscribe(t *testing.T) {
	h := NewHub(4)
	ch := h.Subscribe()
	h.Unsubscribe(ch)
	// Second unsubscribe should be safe (no-op).
	h.Unsubscribe(ch)
}

func TestHubConcurrentPublishSubscribe(t *testing.T) {
	h := NewHub(16)
	var wg sync.WaitGroup

	// Publish continuously in the background.
	stop := make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				h.Publish([]byte{0xFF, 0xD8, byte(i), 0xFF, 0xD9})
			}
		}
	}()

	// Spawn subscribers that each consume one frame then leave.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch := h.Subscribe()
			<-ch
			h.Unsubscribe(ch)
		}()
	}

	wg.Wait()
	close(stop)
}
