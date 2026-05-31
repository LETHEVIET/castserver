package cast

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"time"

	"castserver/internal/control"
	"castserver/internal/stream"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  128 * 1024,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Config is the first (text) message sent by the browser.
// If Preset is set, all other fields are derived server-side.
// For custom mode, Width/Height/FPS/Quality/Bitrate/Scaler/ChunkMS are used directly.
type Config struct {
	Preset        string `json:"preset"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	FPS           int    `json:"fps"`
	Quality       int    `json:"quality"` // JPEG -q:v
	Bitrate       int    `json:"bitrate"` // kbps
	Scaler        string `json:"scaler"`
	ChunkMS       int    `json:"chunk_ms"`
	HardwareAccel bool   `json:"hardware_accel"`
}

// Handler returns an http.HandlerFunc that upgrades HTTP to WebSocket
// and runs the screen-cast pipeline. The browser pushes WebM chunks; the
// server pipes them through ffmpeg to the JPEG hub.
func Handler(ctrl *control.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("cast: websocket upgrade failed: %v", err)
			return
		}
		defer ws.Close()

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

		// Resolve parameters from preset or custom override.
		var sw, sh, sfps, quality, bitrate int
		var scaler string
		var chunkMS int
		var hwAccel bool

		if cfg.Preset != "" && cfg.Preset != "Custom" {
			p, _ := control.LookupPreset(cfg.Preset)
			sw, sh, sfps = p.Width, p.Height, p.FPS
			quality = p.JPEGQuality
			bitrate = p.Bitrate
			scaler = p.Scaler
			chunkMS = p.ChunkMS
			hwAccel = p.HardwareAccel
		} else {
			// Custom mode: use fields directly with sensible defaults.
			sw = cfg.Width
			sh = cfg.Height
			sfps = cfg.FPS
			quality = cfg.Quality
			bitrate = cfg.Bitrate
			scaler = cfg.Scaler
			chunkMS = cfg.ChunkMS
			hwAccel = cfg.HardwareAccel

			if sw <= 0 || sh <= 0 {
				sw, sh = 1280, 720
			}
			if sfps <= 0 {
				sfps = 30
			}
			if quality < 2 || quality > 31 {
				quality = 6
			}
			if bitrate <= 0 {
				bitrate = 2000
			}
			if scaler == "" {
				scaler = "lanczos"
			}
			if chunkMS <= 0 {
				chunkMS = 100
			}
		}

		ctrl.SetStreamConfig(sw, sh, sfps)

		ctx, _, err := ctrl.AcquireSession()
		if err != nil {
			log.Printf("cast: %v", err)
			writeText(ws, fmt.Sprintf(`{"error":"%s"}`, err))
			return
		}
		defer ctrl.ReleaseSession()
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		go func() {
			select {
			case <-r.Context().Done():
				cancel()
			case <-ctx.Done():
			}
		}()

		log.Printf("cast: starting preset=%q %dx%d@%d q=%d scaler=%s bitrate=%dkbps chunk=%dms hw_accel=%t",
			cfg.Preset, sw, sh, sfps, quality, scaler, bitrate, chunkMS, hwAccel)
		writeText(ws, fmt.Sprintf(`{"status":"streaming","width":%d,"height":%d,"fps":%d,"quality":%d,"bitrate":%d,"chunk_ms":%d}`,
			sw, sh, sfps, quality, bitrate, chunkMS))

		pr, pw := io.Pipe()
		defer pr.Close()

		cmd := buildCastFFmpeg(ctx, sw, sh, sfps, quality, scaler, hwAccel)
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

		go func() {
			defer pw.Close()
			tracker := ctrl.Latency()
			for {
				mt, payload, err := ws.ReadMessage()
				if err != nil {
					return
				}
				if mt != websocket.BinaryMessage {
					continue
				}
				ingestAt := time.Now()

				var senderAt time.Time
				var webmPayload []byte
				if len(payload) >= 8 {
					ms := int64(binary.BigEndian.Uint64(payload[:8]))
					if ms > 0 {
						senderAt = time.UnixMilli(ms)
					}
					webmPayload = payload[8:]
				} else {
					webmPayload = payload
				}

				if _, err := pw.Write(webmPayload); err != nil {
					return
				}
				pipeAt := time.Now()
				tracker.RecordIngest(senderAt, ingestAt, pipeAt)
			}
		}()

		hub := ctrl.Hub()
		tracker := ctrl.Latency()
		if err := stream.PumpJPEGsToHub(stdout, hub, func(frame []byte, transcodeAt, pumpDoneAt time.Time) {
			senderAt, ingestAt := tracker.RecordFrame(len(frame), transcodeAt, pumpDoneAt)
			ctrl.IncrementFrames(1)

			// Prepend 24-byte telemetry metadata header
			// [8-byte senderAt][8-byte ingestAt][8-byte publishAt]
			payload := make([]byte, 24+len(frame))
			if tracker.IsEnabled() && !senderAt.IsZero() && !ingestAt.IsZero() {
				binary.BigEndian.PutUint64(payload[0:8], uint64(senderAt.UnixMilli()))
				binary.BigEndian.PutUint64(payload[8:16], uint64(ingestAt.UnixMilli()))
				binary.BigEndian.PutUint64(payload[16:24], uint64(pumpDoneAt.UnixMilli()))
			}
			copy(payload[24:], frame)
			hub.Publish(payload)
		}); err != nil && err != io.EOF {
			log.Printf("cast: pump jpegs: %v", err)
		}

		log.Printf("cast: stopped")
	}
}

func buildCastFFmpeg(ctx context.Context, w, h, fps, quality int, scaler string, hwAccel bool) *exec.Cmd {
	var vf string
	if scaler == "none" || w <= 0 || h <= 0 {
		vf = "format=yuvj420p"
	} else {
		vf = fmt.Sprintf("scale=%d:%d:flags=%s,format=yuvj420p", w, h, scaler)
	}

	if quality < 2 || quality > 31 {
		quality = 6
	}

	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-avoid_negative_ts", "make_zero",
		"-fflags", "nobuffer+discardcorrupt+fastseek",
		"-flags", "low_delay",
		"-threads", "1", // Eliminate frame-threading buffering delay
	}

	if hwAccel {
		args = append(args, "-hwaccel", "cuda")
	} else {
		args = append(args, "-thread_type", "slice", "-threads", "2")
	}

	args = append(args,
		"-f", "matroska",
		"-i", "pipe:0",
		"-an", "-sn", "-dn",
		"-vf", vf,
		"-c:v", "mjpeg",
		"-huffman", "default", // Use default Huffman tables to massively speed up encoding
		"-q:v", strconv.Itoa(quality),
		"-vsync", "drop",
		"-flush_packets", "1",
		"-f", "mjpeg",
		"pipe:1",
	)

	return exec.CommandContext(ctx, "ffmpeg", args...)
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
