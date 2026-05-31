// Package webclient serves a low-latency MJPEG stream over WebSocket so that
// any browser (including old iOS Safari, e.g. iPhone 5c) can display whatever
// the active stream session is publishing.
//
// The handler is a pure subscriber: it attaches to control.Handler.Hub() and
// forwards each JPEG frame as a binary WebSocket message. Sessions are started
// elsewhere — POST /play with target=web (URL/file/YouTube source) or /ws/cast
// with target=web (browser screen-cast source).
package webclient

import (
	"log"
	"net/http"
	"time"

	"3dsstreaming/internal/control"

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

		// Tell the page whether a session is currently publishing.
		if ctrl.SessionTarget() == control.TargetWeb {
			_ = ws.WriteMessage(websocket.TextMessage, []byte(`{"status":"streaming"}`))
		} else {
			_ = ws.WriteMessage(websocket.TextMessage, []byte(`{"status":"idle"}`))
		}

		// Detect WS close so we can stop forwarding.
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				if _, _, err := ws.ReadMessage(); err != nil {
					return
				}
			}
		}()

		writeDeadline := 2 * time.Second
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
				_ = ws.SetWriteDeadline(time.Now().Add(writeDeadline))
				if err := ws.WriteMessage(websocket.BinaryMessage, frame); err != nil {
					log.Printf("web: write failed (subscriber dropping): %v", err)
					return
				}
			}
		}
	}
}
