package cast

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"

	"3dsstreaming/internal/control"
	"3dsstreaming/internal/transport"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true // allow all origins for local network use
	},
}

// Config is the first (text) message sent by the browser.
type Config struct {
	ClientAddr string `json:"client_addr"`
}

// Handler returns an http.HandlerFunc that upgrades HTTP to WebSocket
// and runs the screen-cast pipeline.
func Handler(ctrl *control.Handler, localAddr string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("cast: websocket upgrade failed: %v", err)
			return
		}
		defer ws.Close()

		// 1. Read JSON config (text message).
		msgType, data, err := ws.ReadMessage()
		if err != nil {
			log.Printf("cast: read config failed: %v", err)
			return
		}
		if msgType != websocket.TextMessage {
			log.Printf("cast: expected text config, got type %d", msgType)
			return
		}
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			log.Printf("cast: invalid config JSON: %v", err)
			return
		}
		if cfg.ClientAddr == "" {
			log.Printf("cast: client_addr missing in config")
			return
		}

		log.Printf("cast: starting stream to %s", cfg.ClientAddr)

		// 2. Create UDP sender.
		sender, err := transport.NewSender(cfg.ClientAddr, localAddr)
		if err != nil {
			log.Printf("cast: sender error: %v", err)
			return
		}
		defer sender.Close()

		ctrl.SetClientAddr(cfg.ClientAddr)
		defer ctrl.SetClientAddr("")

		// 3. Create a pipe: websocket writer → ffmpeg stdin.
		pr, pw := io.Pipe()
		defer pr.Close()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// 4. Start ffmpeg reading webm from stdin, outputting raw YUV.
		csw, csh, cfps := ctrl.GetStreamConfig()
		if csw <= 0 || csh <= 0 {
			csw, csh, cfps = 256, 192, 15
		}
		scaleFilter := fmt.Sprintf("scale=%d:%d:flags=fast_bilinear,format=yuv420p", csw, csh)
		cmd := exec.CommandContext(ctx, "ffmpeg",
			"-hide_banner", "-loglevel", "warning",
			"-fflags", "nobuffer",
			"-flags", "low_delay",
			"-probesize", "32",
			"-analyzeduration", "0",
			"-i", "pipe:0",
			"-an", "-sn", "-dn",
			"-vf", scaleFilter,
			"-f", "rawvideo",
			"-pix_fmt", "yuv420p",
			"-r", fmt.Sprintf("%d", cfps),
			"pipe:1",
		)
		cmd.Stdin = pr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			log.Printf("cast: stdout pipe error: %v", err)
			return
		}
		cmd.Stderr = nil

		if err := cmd.Start(); err != nil {
			log.Printf("cast: ffmpeg start error: %v", err)
			return
		}

		// Reap ffmpeg when it exits.
		go func() {
			if err := cmd.Wait(); err != nil {
				log.Printf("cast: ffmpeg exited: %v", err)
			}
		}()

		// 5. Goroutine: pump websocket binary messages into the pipe.
		go func() {
			defer pw.Close()
			for {
				mt, payload, err := ws.ReadMessage()
				if err != nil {
					log.Printf("cast: websocket read error: %v", err)
					return
				}
				if mt != websocket.BinaryMessage {
					continue // ignore non-binary frames
				}
				if _, err := pw.Write(payload); err != nil {
					log.Printf("cast: pipe write error: %v", err)
					return
				}
			}
		}()

		// 6. Read YUV frames from ffmpeg stdout and send via UDP.
		sw, sh, _ := ctrl.GetStreamConfig()
		if sw <= 0 || sh <= 0 {
			sw, sh = 256, 192
		}
		frameBytes := sw * sh * 3 / 2
		frame := make([]byte, frameBytes)

		for {
			_, err := io.ReadFull(stdout, frame)
			if err == io.ErrUnexpectedEOF || err == io.EOF {
				log.Printf("cast: stream ended")
				break
			}
			if err != nil {
				log.Printf("cast: read frame error: %v", err)
				break
			}
			if err := sender.SendFrame(frame, uint16(sw), uint16(sh)); err != nil {
				log.Printf("cast: send frame error: %v", err)
				break
			}
			ctrl.IncrementFrames(1)
			ctrl.IncrementNALs(1)
		}

		log.Printf("cast: stopped")
	}
}