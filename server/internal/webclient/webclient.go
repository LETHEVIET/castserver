package webclient

import (
	"encoding/binary"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"castserver/internal/control"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 64 * 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Handler returns an http.HandlerFunc that subscribes the connecting browser
// to the JPEG broadcast hub.
func Handler(ctrl *control.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("web: websocket upgrade failed: %v", err)
			return
		}
		defer ws.Close()

		hub := ctrl.Hub()
		ch := hub.Subscribe()
		defer hub.Unsubscribe(ch)

		if ctrl.IsSessionActive() {
			_ = ws.WriteMessage(websocket.TextMessage, []byte(`{"status":"streaming"}`))
		} else {
			_ = ws.WriteMessage(websocket.TextMessage, []byte(`{"status":"idle"}`))
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				if _, _, err := ws.ReadMessage(); err != nil {
					return
				}
			}
		}()

		// Parse latency query parameter
		latencyStr := r.URL.Query().Get("latency")
		var latency time.Duration
		if latencyStr != "" {
			if ms, err := strconv.Atoi(latencyStr); err == nil && ms > 0 {
				latency = time.Duration(ms) * time.Millisecond
			}
		}

		writeDeadline := 2 * time.Second

		if latency <= 0 {
			// No artificial delay: write directly
			for {
				select {
				case <-done:
					return
				case <-r.Context().Done():
					return
				case frame, ok := <-ch:
					if !ok {
						return
					}
					preWrite := time.Now()
					if ctrl.Latency().IsEnabled() && len(frame) >= 24 {
						binary.BigEndian.PutUint64(frame[16:24], uint64(preWrite.UnixMilli()))
					}

					_ = ws.SetWriteDeadline(time.Now().Add(writeDeadline))
					if err := ws.WriteMessage(websocket.BinaryMessage, frame); err != nil {
						log.Printf("web: write failed (subscriber dropping): %v", err)
						return
					}
					dur := time.Since(preWrite)
					ctrl.Latency().RecordDelivery(dur)
					if dur > 5*time.Millisecond {
						log.Printf("web: slow delivery: %v (%d bytes)", dur, len(frame))
					}
				}
			}
		}

		// Latency is configured: buffer frames and send after delay has elapsed
		type delayedFrame struct {
			payload  []byte
			sendTime time.Time
		}

		var queue []delayedFrame
		var queueMu sync.Mutex
		signal := make(chan struct{}, 1)

		// Reader goroutine: drains ch immediately to prevent hub channel overflow (frame drops)
		go func() {
			for {
				select {
				case <-done:
					return
				case <-r.Context().Done():
					return
				case frame, ok := <-ch:
					if !ok {
						return
					}
					queueMu.Lock()
					queue = append(queue, delayedFrame{
						payload:  frame,
						sendTime: time.Now().Add(latency),
					})
					queueMu.Unlock()

					// Notify writer loop
					select {
					case signal <- struct{}{}:
					default:
					}
				}
			}
		}()

		// Writer loop: waits until scheduled send time and writes to WebSocket
		for {
			queueMu.Lock()
			var nextPayload []byte
			var waitDur time.Duration
			hasFrame := false

			if len(queue) > 0 {
				head := queue[0]
				now := time.Now()
				if now.After(head.sendTime) || now.Equal(head.sendTime) {
					nextPayload = head.payload
					queue = queue[1:]
					hasFrame = true
				} else {
					waitDur = head.sendTime.Sub(now)
				}
			}
			queueMu.Unlock()

			if hasFrame {
				preWrite := time.Now()
				if ctrl.Latency().IsEnabled() && len(nextPayload) >= 24 {
					binary.BigEndian.PutUint64(nextPayload[16:24], uint64(preWrite.UnixMilli()))
				}
				_ = ws.SetWriteDeadline(time.Now().Add(writeDeadline))
				if err := ws.WriteMessage(websocket.BinaryMessage, nextPayload); err != nil {
					log.Printf("web: delayed write failed: %v", err)
					return
				}
				ctrl.Latency().RecordDelivery(time.Since(preWrite))
				continue
			}

			var timerChan <-chan time.Time
			var timer *time.Timer
			if waitDur > 0 {
				timer = time.NewTimer(waitDur)
				timerChan = timer.C
			}

			select {
			case <-done:
				if timer != nil {
					timer.Stop()
				}
				return
			case <-r.Context().Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case <-signal:
				if timer != nil {
					timer.Stop()
				}
				// New frame was added: re-evaluate queue
			case <-timerChan:
				// Timer expired: send the head frame
			}
		}
	}
}
