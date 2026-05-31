package cast

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strconv"

	"3dsstreaming/internal/control"
	"3dsstreaming/internal/ingest"
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

// Config is the first (text) message sent by the browser. ClientAddr is only
// required when Target is "udp".
type Config struct {
	Target     string `json:"target"`      // "udp" (default) or "web"
	ClientAddr string `json:"client_addr"` // 3DS ip:port — required for udp
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	FPS        int    `json:"fps"`
	Quality    int    `json:"quality"` // mjpeg q:v for target=web
}

// Handler returns an http.HandlerFunc that upgrades HTTP to WebSocket
// and runs the screen-cast pipeline. The browser pushes WebM chunks; the
// server pipes them through ffmpeg to either UDP (3DS) or to the JPEG hub
// (web client).
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
			writeText(ws, fmt.Sprintf(`{"error":"invalid config: %s"}`, err))
			return
		}

		target := cfg.Target
		if target == "" {
			target = control.TargetUDP
		}
		if target != control.TargetUDP && target != control.TargetWeb {
			writeText(ws, fmt.Sprintf(`{"error":"invalid target %q (want udp or web)"}`, target))
			return
		}
		if target == control.TargetUDP && cfg.ClientAddr == "" {
			writeText(ws, `{"error":"client_addr required for target=udp"}`)
			return
		}

		// Resolve stream config: prefer values sent by the browser; fall
		// back to whatever /play last set; finally to safe defaults.
		sw, sh, sfps := cfg.Width, cfg.Height, cfg.FPS
		if sw <= 0 || sh <= 0 || sfps <= 0 {
			gw, gh, gf := ctrl.GetStreamConfig()
			if sw <= 0 || sh <= 0 {
				sw, sh = gw, gh
			}
			if sfps <= 0 {
				sfps = gf
			}
		}
		if sw <= 0 || sh <= 0 {
			if target == control.TargetWeb {
				sw, sh = 480, 320
			} else {
				sw, sh = 256, 192
			}
		}
		if sfps <= 0 {
			sfps = 15
		}
		ctrl.SetStreamConfig(sw, sh, sfps)

		// 2. Claim the active-session slot.
		ctx, _, err := ctrl.AcquireSession(target)
		if err != nil {
			log.Printf("cast: %v", err)
			writeText(ws, fmt.Sprintf(`{"error":"%s"}`, err))
			return
		}
		defer ctrl.ReleaseSession()
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		// Honor request lifecycle too.
		go func() {
			select {
			case <-r.Context().Done():
				cancel()
			case <-ctx.Done():
			}
		}()

		log.Printf("cast: starting target=%s %dx%d@%d (client=%s)", target, sw, sh, sfps, cfg.ClientAddr)
		writeText(ws, fmt.Sprintf(`{"status":"streaming","target":"%s","width":%d,"height":%d,"fps":%d}`, target, sw, sh, sfps))

		// 3. Pipe: WS binary frames → ffmpeg stdin (webm).
		pr, pw := io.Pipe()
		defer pr.Close()

		// 4. Start ffmpeg with the right output format for the target.
		cmd := buildCastFFmpeg(ctx, target, sw, sh, sfps, cfg.Quality)
		cmd.Stdin = pr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			log.Printf("cast: stdout pipe: %v", err)
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			log.Printf("cast: stderr pipe: %v", err)
			return
		}

		if err := cmd.Start(); err != nil {
			log.Printf("cast: ffmpeg start: %v", err)
			writeText(ws, fmt.Sprintf(`{"error":"ffmpeg start failed: %s"}`, err))
			return
		}

		go drainStderr(stderr)
		go func() {
			if err := cmd.Wait(); err != nil && ctx.Err() == nil {
				log.Printf("cast: ffmpeg exited: %v", err)
			}
		}()

		// 5. Pump WS binary messages into ffmpeg stdin.
		go func() {
			defer pw.Close()
			for {
				mt, payload, err := ws.ReadMessage()
				if err != nil {
					return
				}
				if mt != websocket.BinaryMessage {
					continue
				}
				if _, err := pw.Write(payload); err != nil {
					return
				}
			}
		}()

		// 6. Drive output to the configured target.
		switch target {
		case control.TargetUDP:
			sender, err := transport.NewSender(cfg.ClientAddr, localAddr)
			if err != nil {
				log.Printf("cast: sender error: %v", err)
				return
			}
			defer sender.Close()
			ctrl.SetClientAddr(cfg.ClientAddr)
			defer ctrl.SetClientAddr("")

			frameBytes := sw * sh * 3 / 2
			frame := make([]byte, frameBytes)
			for {
				_, err := io.ReadFull(stdout, frame)
				if err == io.ErrUnexpectedEOF || err == io.EOF {
					break
				}
				if err != nil {
					log.Printf("cast: read frame: %v", err)
					break
				}
				if err := sender.SendFrame(frame, uint16(sw), uint16(sh)); err != nil {
					log.Printf("cast: send frame: %v", err)
					break
				}
				ctrl.IncrementFrames(1)
				ctrl.IncrementNALs(1)
			}

		case control.TargetWeb:
			if err := ingest.PumpJPEGsToHub(stdout, ctrl.Hub()); err != nil && err != io.EOF {
				log.Printf("cast: pump jpegs: %v", err)
			}
		}

		log.Printf("cast: stopped (target=%s)", target)
	}
}

func buildCastFFmpeg(ctx context.Context, target string, w, h, fps, quality int) *exec.Cmd {
	scaleYUV := fmt.Sprintf("scale=%d:%d:flags=fast_bilinear,format=yuv420p", w, h)
	scaleJPEG := fmt.Sprintf("scale=%d:%d:flags=fast_bilinear,format=yuvj420p", w, h)

	base := []string{
		"-hide_banner", "-loglevel", "warning",
		"-fflags", "nobuffer", "-flags", "low_delay",
		"-probesize", "32", "-analyzeduration", "0",
		"-i", "pipe:0",
		"-an", "-sn", "-dn",
		"-r", strconv.Itoa(fps),
	}

	switch target {
	case control.TargetWeb:
		if quality < 2 || quality > 31 {
			quality = 8
		}
		args := append(base,
			"-vf", scaleJPEG,
			"-c:v", "mjpeg",
			"-q:v", strconv.Itoa(quality),
			"-f", "mjpeg",
			"pipe:1",
		)
		return exec.CommandContext(ctx, "ffmpeg", args...)
	default: // udp
		args := append(base,
			"-vf", scaleYUV,
			"-f", "rawvideo",
			"-pix_fmt", "yuv420p",
			"pipe:1",
		)
		return exec.CommandContext(ctx, "ffmpeg", args...)
	}
}

func drainStderr(r io.Reader) {
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			log.Printf("[cast-ffmpeg] %s", string(buf[:n]))
		}
		if err != nil {
			return
		}
	}
}

func writeText(ws *websocket.Conn, s string) {
	_ = ws.WriteMessage(websocket.TextMessage, []byte(s))
}
