package latslo

import (
	"fmt"
	"sync"
	"time"
)

// Window maintains a rolling time window of histograms. Samples are recorded
// into the bucket covering the current time; Snapshot sums the buckets whose
// timestamps fall within the window.
type Window struct {
	mu         sync.RWMutex
	config     Config
	sourceID   uint64
	windowSize time.Duration
	bucketSize time.Duration
	numBuckets int
	buckets    []windowBucket
}

type windowBucket struct {
	start int64 // unix nanoseconds of bucket start; 0 means empty
	hist  *Histogram
}

// NewWindow creates a rolling window covering windowSize with buckets of
// bucketSize. bucketSize must evenly divide windowSize.
func NewWindow(cfg Config, sourceID uint64, windowSize, bucketSize time.Duration) (*Window, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if windowSize <= 0 || bucketSize <= 0 {
		return nil, fmt.Errorf("latslo: windowSize and bucketSize must be positive, got %s / %s", windowSize, bucketSize)
	}
	if windowSize%bucketSize != 0 {
		return nil, fmt.Errorf("latslo: bucketSize %s must evenly divide windowSize %s", bucketSize, windowSize)
	}
	numBuckets := int(windowSize / bucketSize)
	w := &Window{
		config:     cfg,
		sourceID:   sourceID,
		windowSize: windowSize,
		bucketSize: bucketSize,
		numBuckets: numBuckets,
		buckets:    make([]windowBucket, numBuckets),
	}
	for i := range w.buckets {
		h, _ := NewHistogram(cfg, sourceID)
		w.buckets[i].hist = h
	}
	return w, nil
}

// Record records a sample at time now into the appropriate bucket.
func (w *Window) Record(now time.Time, v time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	bs := int64(w.bucketSize)
	bucketStart := (now.UnixNano() / bs) * bs
	idx := int((now.UnixNano() / bs) % int64(w.numBuckets))
	b := &w.buckets[idx]
	if b.start != bucketStart {
		// Stale bucket from a previous window cycle; reset.
		b.hist, _ = NewHistogram(w.config, w.sourceID)
		b.start = bucketStart
	}
	b.hist.Record(v)
}

// RecordNow records a sample at the current time.
func (w *Window) RecordNow(v time.Duration) {
	w.Record(time.Now(), v)
}

// Snapshot returns a consistent view of the samples recorded within the
// window ending at now.
func (w *Window) Snapshot(now time.Time) Snapshot {
	w.mu.RLock()
	defer w.mu.RUnlock()
	snap := Snapshot{
		config:  w.config,
		buckets: make([]int64, w.config.bucketCount()),
	}
	cutoff := now.Add(-w.windowSize).UnixNano()
	for i := range w.buckets {
		b := &w.buckets[i]
		if b.start == 0 || b.start < cutoff {
			continue
		}
		bs := b.hist.Snapshot()
		for j := range snap.buckets {
			snap.buckets[j] += bs.buckets[j]
		}
		snap.underflow += bs.underflow
		snap.overflow += bs.overflow
		snap.total += bs.total
	}
	return snap
}

// Advance rolls the window forward, resetting buckets older than the window.
func (w *Window) Advance(now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cutoff := now.Add(-w.windowSize).UnixNano()
	for i := range w.buckets {
		b := &w.buckets[i]
		if b.start != 0 && b.start < cutoff {
			b.hist, _ = NewHistogram(w.config, w.sourceID)
			b.start = 0
		}
	}
}

// WindowSize returns the window duration.
func (w *Window) WindowSize() time.Duration { return w.windowSize }

// BucketSize returns the bucket duration.
func (w *Window) BucketSize() time.Duration { return w.bucketSize }
