package ingest

import (
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
)

// FrameBytes is the size of one 400×240 YUV420p frame.
const FrameBytes = 400 * 240 * 3 / 2 // 144000

func Run(ctx context.Context, sourceURL string, out chan<- []byte) error {
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
		"-vf", "scale=400:240:flags=fast_bilinear,format=yuv420p",
		"-f", "rawvideo", "-pix_fmt", "yuv420p",
		"-r", "30",
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

	log.Printf("Started ffmpeg with PID %d for source %s", cmd.Process.Pid, sourceURL)

	if err := readFrames(stdout, out); err != nil {
		return fmt.Errorf("read frames: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg exit: %w", err)
	}

	return nil
}

func readFrames(r io.Reader, out chan<- []byte) error {
	frame := make([]byte, FrameBytes)
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
		if n != FrameBytes {
			return fmt.Errorf("short read: %d < %d", n, FrameBytes)
		}

		// copy because the channel consumer may hold onto it
		frameCopy := make([]byte, FrameBytes)
		copy(frameCopy, frame)
		out <- frameCopy
	}
}
