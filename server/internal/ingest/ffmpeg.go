package ingest

import (
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strconv"
)

func Run(ctx context.Context, sourceURL string, width, height, fps int, out chan<- []byte) error {
	frameBytes := width * height * 3 / 2
	scaleFilter := fmt.Sprintf("scale=%d:%d:flags=fast_bilinear,format=yuv420p", width, height)

	probeArgs := []string{"-fflags", "nobuffer", "-flags", "low_delay",
		"-probesize", "32", "-analyzeduration", "0"}
	if len(sourceURL) >= 7 && sourceURL[:7] == "file://" {
		probeArgs = nil
	}

	base := []string{"-hide_banner", "-loglevel", "warning", "-re"}
	args := append(base, probeArgs...)
	args = append(args,
		"-i", sourceURL,
		"-an", "-sn", "-dn",
		"-vf", scaleFilter,
		"-f", "rawvideo", "-pix_fmt", "yuv420p",
		"-r", strconv.Itoa(fps),
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

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stderr.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				log.Printf("[ffmpeg] %s", string(buf[:n]))
			}
		}
	}()

	log.Printf("Started ffmpeg with PID %d for source %s (%dx%d@%d)", cmd.Process.Pid, sourceURL, width, height, fps)

	if err := readFrames(stdout, out, frameBytes); err != nil {
		return fmt.Errorf("read frames: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg exit: %w", err)
	}

	return nil
}

func readFrames(r io.Reader, out chan<- []byte, frameBytes int) error {
	frame := make([]byte, frameBytes)
	for {
		n, err := io.ReadFull(r, frame)
		if err == io.EOF {
			return nil
		}
		if err == io.ErrUnexpectedEOF {
			// partial frame at end of stream — drop it
			return nil
		}
		if err != nil {
			return err
		}
		if n != frameBytes {
			return fmt.Errorf("short read: %d < %d", n, frameBytes)
		}

		// copy because the channel consumer may hold onto it
		frameCopy := make([]byte, frameBytes)
		copy(frameCopy, frame)
		out <- frameCopy
	}
}