package ingest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strconv"

	"3dsstreaming/internal/stream"
)

// RunMJPEG pulls sourceURL through ffmpeg with -c:v mjpeg, splits the
// output stream into individual JPEG frames at SOI/EOI markers, and
// publishes each one to hub. Returns when ctx is cancelled, ffmpeg exits,
// or a fatal read/parse error occurs.
//
// quality is ffmpeg's -q:v (2..31, lower = sharper); 0 picks a sane default.
func RunMJPEG(ctx context.Context, sourceURL string, width, height, fps, quality int, hub *stream.Hub) error {
	if quality < 2 || quality > 31 {
		quality = 8
	}

	scale := fmt.Sprintf("scale=%d:%d:flags=fast_bilinear,format=yuvj420p", width, height)

	probeArgs := []string{
		"-fflags", "nobuffer", "-flags", "low_delay",
		"-probesize", "32", "-analyzeduration", "0",
	}
	if len(sourceURL) >= 7 && sourceURL[:7] == "file://" {
		// Local files: pace via -re instead of cutting probe.
		probeArgs = []string{"-re"}
	}

	args := append([]string{"-hide_banner", "-loglevel", "warning"}, probeArgs...)
	args = append(args,
		"-i", sourceURL,
		"-an", "-sn", "-dn",
		"-vf", scale,
		"-r", strconv.Itoa(fps),
		"-c:v", "mjpeg",
		"-q:v", strconv.Itoa(quality),
		"-f", "mjpeg",
		"pipe:1",
	)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	go drainFFmpegStderr(stderr, "ingest-mjpeg")

	log.Printf("Started ffmpeg (mjpeg) PID %d for %s (%dx%d@%d q=%d)", cmd.Process.Pid, sourceURL, width, height, fps, quality)

	pumpErr := PumpJPEGsToHub(stdout, hub)
	waitErr := cmd.Wait()

	if pumpErr != nil && pumpErr != io.EOF {
		return fmt.Errorf("pump: %w", pumpErr)
	}
	if waitErr != nil && ctx.Err() == nil {
		return fmt.Errorf("ffmpeg exit: %w", waitErr)
	}
	return nil
}

// PumpJPEGsToHub reads an MJPEG byte stream from r and publishes each
// JPEG frame to hub. Exported so cast.go can reuse it.
func PumpJPEGsToHub(r io.Reader, hub *stream.Hub) error {
	const (
		readChunk = 16 * 1024
		maxFrame  = 2 * 1024 * 1024
	)
	soi := []byte{0xFF, 0xD8}
	eoi := []byte{0xFF, 0xD9}

	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, readChunk)

	for {
		n, err := r.Read(tmp)
		if n > 0 {
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
				buf = append(buf[:0], buf[frameEnd:]...)
			}
		}
		if err != nil {
			return err
		}
	}
}

func drainFFmpegStderr(r io.Reader, tag string) {
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			log.Printf("[%s] %s", tag, string(buf[:n]))
		}
		if err != nil {
			return
		}
	}
}
