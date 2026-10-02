package slogate

import (
	"sync"
	"time"
)

// WindowConfig configures a rolling evaluation window.
type WindowConfig struct {
	// SliceWidth is the duration covered by one histogram slice.
	SliceWidth time.Duration
	// SliceCount is the number of slices retained. The window covers
	// SliceWidth*SliceCount of wall-clock history; memory is fixed at
	// SliceCount histograms regardless of sample volume.
	SliceCount int
	// Clock returns the current time. When nil, time.Now is used.
	Clock func() time.Time
}

func (c WindowConfig) validate() error {
	if c.SliceWidth <= 0 || c.SliceCount <= 0 {
		return ErrInvalidConfig
	}
	return nil
}

// Window is a rolling time window. Samples land in fixed-width slices
// stored in a ring buffer; advancing the window resets expired slices, so
// neither counts nor memory grow without bound.
type Window struct {
	mu   sync.Mutex
	hcfg HistogramConfig
	cfg  WindowConfig

	// start is the wall-clock start of logical slice 0 (slices[head]).
	start  time.Time
	head   int // ring position holding logical slice 0
	slices []*Histogram
}

// NewWindow creates a rolling window anchored at the current clock time.
func NewWindow(hcfg HistogramConfig, cfg WindowConfig) (*Window, error) {
	if err := hcfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	w := &Window{
		hcfg:   hcfg,
		cfg:    cfg,
		start:  cfg.Clock().Truncate(cfg.SliceWidth),
		slices: make([]*Histogram, cfg.SliceCount),
	}
	for i := range w.slices {
		h, err := New(hcfg)
		if err != nil {
			return nil, err
		}
		w.slices[i] = h
	}
	return w, nil
}

// offset returns the logical slice offset of ts relative to start.
func (w *Window) offset(ts time.Time) int {
	cur := ts.Truncate(w.cfg.SliceWidth)
	return int(cur.Sub(w.start) / w.cfg.SliceWidth)
}

func (w *Window) resetAt(ringIdx int) {
	h, _ := New(w.hcfg)
	w.slices[ringIdx] = h
}

// advance ensures the logical slice at off is within the retained range.
// Caller holds mu.
func (w *Window) advance(off int) {
	n := w.cfg.SliceCount
	if off < n {
		return
	}
	steps := off - (n - 1) // number of logical slices the head moves
	if steps >= n {
		for i := range w.slices {
			w.resetAt(i)
		}
		w.head = 0
		w.start = w.start.Add(time.Duration(off-(n-1)) * w.cfg.SliceWidth)
		return
	}
	for s := 0; s < steps; s++ {
		w.resetAt((w.head + s) % n)
	}
	w.head = (w.head + steps) % n
	w.start = w.start.Add(time.Duration(steps) * w.cfg.SliceWidth)
}

// Observe records a sample at timestamp ts. Samples older than the
// retained window are discarded; samples in the future advance it.
func (w *Window) Observe(ts time.Time, value float64, isError bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	off := w.offset(ts)
	if off < 0 {
		return
	}
	if off >= w.cfg.SliceCount {
		w.advance(off)
		off = w.offset(ts)
		if off < 0 || off >= w.cfg.SliceCount {
			return
		}
	}
	w.slices[(w.head+off)%w.cfg.SliceCount].ObserveOutcome(value, isError)
}

// Snapshot returns the histogram accumulated over all retained slices.
func (w *Window) Snapshot() Snapshot {
	return w.SnapshotAt(w.cfg.Clock())
}

// SnapshotAt returns the contents of slices not newer than now.
func (w *Window) SnapshotAt(now time.Time) Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Lazy roll-forward: time may have advanced without new samples, so
	// expire slices that have aged out before aggregating.
	if off := w.offset(now); off >= w.cfg.SliceCount {
		w.advance(off)
	}
	n := w.cfg.SliceCount
	limit := n
	if off := w.offset(now); off >= 0 && off < n {
		limit = off + 1
	}
	out := Snapshot{
		Config: w.hcfg,
		Counts: make([]uint64, w.hcfg.BucketCount()),
		Source: SourceID{ID: "window", Epoch: 0},
	}
	var underMin, overMax float64
	for i := 0; i < limit; i++ {
		s := w.slices[(w.head+i)%n].Snapshot()
		if s.Total == 0 {
			continue
		}
		for j, c := range s.Counts {
			out.Counts[j] += c
		}
		out.Underflow += s.Underflow
		out.Overflow += s.Overflow
		out.Errors += s.Errors
		out.Total += s.Total
		out.Sum += s.Sum
		if s.Underflow > 0 && (underMin == 0 || s.UnderflowMin < underMin) {
			underMin = s.UnderflowMin
		}
		if s.OverflowMax > overMax {
			overMax = s.OverflowMax
		}
	}
	out.UnderflowMin = underMin
	out.OverflowMax = overMax
	return out
}
