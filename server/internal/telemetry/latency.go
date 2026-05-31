// Package telemetry provides per-stage latency tracking for the cast pipeline.
//
// The cast pipeline has five measurable server-side stages:
//
//	Browser → [Ingest] → [PipeWrite] → [Transcode] → [Pump] → [Delivery] → Viewer
//
// Each stage records a timestamp. The Tracker maintains a ring buffer of
// recent snapshots and exponentially-weighted moving averages (EMA, α ≈ 0.1)
// for each stage duration.
package telemetry

import (
	"sync"
	"sync/atomic"
	"time"
)

// Snapshot holds timing for one frame's journey through the server pipeline.
type Snapshot struct {
	FrameSeq    uint64    // monotonic frame counter
	IngestAt    time.Time // when WebM chunk was read from cast WS
	PipeWriteAt time.Time // when chunk was written to ffmpeg stdin
	TranscodeAt time.Time // when JPEG bytes appeared on ffmpeg stdout
	PumpDoneAt  time.Time // when JPEG SOI/EOI parse + Publish completed
	FrameBytes  int       // JPEG frame size
}

// Stats returns current averaged stage durations in microseconds.
type Stats struct {
	AvgPipeWriteUs int64  `json:"avg_pipe_write_us"`
	AvgTranscodeUs int64  `json:"avg_transcode_us"`
	AvgPumpUs      int64  `json:"avg_pump_us"`
	AvgTotalUs     int64  `json:"avg_total_us"`
	FrameSeq       uint64 `json:"frame_seq"`
}

// Tracker maintains a rolling window of frame timing snapshots.
type Tracker struct {
	mu   sync.RWMutex
	ring []Snapshot
	pos  int
	seq  atomic.Uint64

	// Running EMA averages in microseconds.
	avgPipeWrite atomic.Int64
	avgTranscode atomic.Int64
	avgPump      atomic.Int64
	avgTotal     atomic.Int64

	// Latest ingest timestamps, written by the ingest goroutine and read
	// by the pump goroutine to correlate stdin writes with stdout frames.
	ingestMu      sync.Mutex
	lastIngestAt  time.Time
	lastPipeWrite time.Time
}

// NewTracker returns a Tracker with the given ring buffer size.
func NewTracker(windowSize int) *Tracker {
	if windowSize < 16 {
		windowSize = 64
	}
	return &Tracker{ring: make([]Snapshot, windowSize)}
}

// NextSeq allocates a frame sequence number.
func (t *Tracker) NextSeq() uint64 { return t.seq.Add(1) }

// RecordIngest stores the latest ingest and pipe-write timestamps.
// Called from the cast WebM reader goroutine on every chunk.
func (t *Tracker) RecordIngest(ingestAt, pipeWriteAt time.Time) {
	t.ingestMu.Lock()
	t.lastIngestAt = ingestAt
	t.lastPipeWrite = pipeWriteAt
	t.ingestMu.Unlock()
}

// RecordFrame stores a completed frame snapshot using the most recent ingest
// timestamps and the provided transcode/pump timestamps. Called from the
// PumpJPEGsToHub callback.
func (t *Tracker) RecordFrame(frameBytes int, transcodeAt, pumpDoneAt time.Time) {
	t.ingestMu.Lock()
	ingestAt := t.lastIngestAt
	pipeWriteAt := t.lastPipeWrite
	t.ingestMu.Unlock()

	seq := t.NextSeq()
	s := Snapshot{
		FrameSeq:    seq,
		IngestAt:    ingestAt,
		PipeWriteAt: pipeWriteAt,
		TranscodeAt: transcodeAt,
		PumpDoneAt:  pumpDoneAt,
		FrameBytes:  frameBytes,
	}

	t.mu.Lock()
	t.ring[t.pos%len(t.ring)] = s
	t.pos++
	t.mu.Unlock()

	// Update EMAs only when we have valid ingest timestamps.
	if !ingestAt.IsZero() && !pipeWriteAt.IsZero() {
		ema(&t.avgPipeWrite, pipeWriteAt.Sub(ingestAt))
		ema(&t.avgTranscode, transcodeAt.Sub(pipeWriteAt))
		ema(&t.avgPump, pumpDoneAt.Sub(transcodeAt))
		ema(&t.avgTotal, pumpDoneAt.Sub(ingestAt))
	}
}

// ema updates an exponential moving average (α ≈ 0.1).
func ema(avg *atomic.Int64, sample time.Duration) {
	us := sample.Microseconds()
	if us < 0 {
		return // skip nonsensical values
	}
	old := avg.Load()
	if old == 0 {
		avg.Store(us)
		return
	}
	// new = old*0.9 + sample*0.1
	avg.Store((old*9 + us) / 10)
}

// Stats returns current averaged stage durations in microseconds.
func (t *Tracker) Stats() Stats {
	return Stats{
		AvgPipeWriteUs: t.avgPipeWrite.Load(),
		AvgTranscodeUs: t.avgTranscode.Load(),
		AvgPumpUs:      t.avgPump.Load(),
		AvgTotalUs:     t.avgTotal.Load(),
		FrameSeq:       t.seq.Load(),
	}
}

// Recent returns the last n snapshots (most recent first).
func (t *Tracker) Recent(n int) []Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if n > len(t.ring) {
		n = len(t.ring)
	}
	if n > t.pos {
		n = t.pos
	}
	out := make([]Snapshot, n)
	for i := 0; i < n; i++ {
		idx := t.pos - 1 - i
		if idx < 0 {
			break
		}
		out[i] = t.ring[idx%len(t.ring)]
	}
	return out
}
