package slogate

import (
	"math"
	"sync"
	"time"
)

// Region classifies where a sample or quantile estimate lies.
type Region int

const (
	RegionUnderflow Region = -1
	RegionInRange   Region = 0
	RegionOverflow  Region = 1
)

// SourceID identifies a single accumulating origin. Epoch changes whenever
// the origin starts a fresh, non-restored accumulator; merging treats a
// new epoch as a distinct source, so a reset process can never be mistaken
// for continuation of its old counters.
type SourceID struct {
	ID    string
	Epoch uint64
}

// NewEpoch returns a process-local epoch that is effectively unique across
// plain restarts. It is based on wall-clock nanoseconds; callers that need
// reproducible processes may supply their own value instead.
func NewEpoch() uint64 { return uint64(time.Now().UnixNano()) }

// Histogram is a fixed-memory, thread-safe latency histogram. Its memory
// consumption depends only on HistogramConfig (a few hundred 64-bit
// counters by default), never on the number of observed samples.
type Histogram struct {
	mu        sync.Mutex
	cfg       HistogramConfig
	source    SourceID
	seq       uint64
	createdAt time.Time

	counts []uint64
	under  uint64
	over   uint64
	errors uint64
	total  uint64
	sum    float64
	underN float64 // minimum observed underflow value
	overX  float64 // maximum observed overflow value
}

// Snapshot is an immutable point-in-time copy of a Histogram.
type Snapshot struct {
	Config    HistogramConfig
	Source    SourceID
	Seq       uint64
	TakenAt   time.Time
	CreatedAt time.Time

	// Counts holds per-bucket sample counts, ordered by value.
	Counts []uint64
	// Underflow counts samples strictly below MinValue.
	Underflow uint64
	// Overflow counts samples at/above the last bucket upper boundary.
	Overflow uint64
	// Errors counts samples marked as failed outcomes.
	Errors uint64
	// Total equals Underflow + sum(Counts) + Overflow.
	Total uint64
	// Sum is the arithmetic sum of every finite observed value.
	Sum float64
	// UnderflowMin is the minimum observed underflow value (0 if none).
	UnderflowMin float64
	// OverflowMax is the maximum observed overflow value (0 if none).
	OverflowMax float64
}

// New creates an empty histogram with an anonymous source.
func New(cfg HistogramConfig) (*Histogram, error) {
	return NewWithSource(cfg, SourceID{ID: "", Epoch: 0})
}

// NewWithSource creates an empty histogram tagged with source.
func NewWithSource(cfg HistogramConfig, source SourceID) (*Histogram, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Histogram{
		cfg:       cfg,
		source:    source,
		createdAt: time.Now(),
		counts:    make([]uint64, cfg.BucketCount()),
	}, nil
}

// Restore rebuilds a histogram from a previously persisted Snapshot.
// Internal consistency is verified; inconsistent state is rejected.
func Restore(s Snapshot) (*Histogram, error) {
	if err := s.Config.Validate(); err != nil {
		return nil, err
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	h, err := NewWithSource(s.Config, s.Source)
	if err != nil {
		return nil, err
	}
	h.seq = s.Seq
	h.createdAt = s.CreatedAt
	h.counts = append([]uint64(nil), s.Counts...)
	h.under, h.over, h.errors, h.total, h.sum = s.Underflow, s.Overflow, s.Errors, s.Total, s.Sum
	h.underN, h.overX = s.UnderflowMin, s.OverflowMax
	return h, nil
}

func (s Snapshot) validate() error {
	n := s.Config.BucketCount()
	if len(s.Counts) != n {
		return ErrCorruptState
	}
	var in uint64
	for _, c := range s.Counts {
		in += c
	}
	if s.Underflow+in+s.Overflow != s.Total {
		return ErrCorruptState
	}
	if s.Errors > s.Total {
		return ErrCorruptState
	}
	if s.Underflow > 0 && !(s.UnderflowMin > 0 && s.UnderflowMin < s.Config.MinValue) {
		return ErrCorruptState
	}
	if s.Overflow > 0 && s.OverflowMax < s.Config.bucketUpper(n-1) {
		return ErrCorruptState
	}
	return nil
}

// Source returns the histogram's source identity.
func (h *Histogram) Source() SourceID {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.source
}

// Observe records one latency sample. Non-finite values are ignored.
func (h *Histogram) Observe(value float64) {
	h.ObserveOutcome(value, false)
}

// ObserveOutcome records one latency sample, marking error outcomes.
func (h *Histogram) ObserveOutcome(value float64, isError bool) {
	if value != value || math.IsInf(value, 0) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch i := h.cfg.bucketIndex(value); {
	case i < 0:
		h.under++
		if h.underN == 0 || value < h.underN {
			h.underN = value
		}
	case i >= len(h.counts):
		h.over++
		if value > h.overX {
			h.overX = value
		}
	default:
		h.counts[i]++
	}
	h.total++
	h.sum += value
	if isError {
		h.errors++
	}
}

// Snapshot returns a consistent point-in-time copy. Snapshots are
// non-destructive: observing concurrently can neither lose nor double
// count samples, and every snapshot reflects some definite instant.
func (h *Histogram) Snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	return Snapshot{
		Config:       h.cfg,
		Source:       h.source,
		Seq:          h.seq,
		TakenAt:      time.Now(),
		CreatedAt:    h.createdAt,
		Counts:       append([]uint64(nil), h.counts...),
		Underflow:    h.under,
		Overflow:     h.over,
		Errors:       h.errors,
		Total:        h.total,
		Sum:          h.sum,
		UnderflowMin: h.underN,
		OverflowMax:  h.overX,
	}
}

// MergeableWith reports whether s and other share an identical layout and
// can be summed bucket-for-bucket.
func (s Snapshot) MergeableWith(other Snapshot) bool {
	return s.Config.equal(other.Config) && len(s.Counts) == len(other.Counts)
}

// Mean returns the arithmetic mean of every finite observed value.
func (s Snapshot) Mean() (float64, error) {
	if s.Total == 0 {
		return 0, ErrEmptyHistogram
	}
	return s.Sum / float64(s.Total), nil
}

// Quantile returns the estimated quantile for q in (0,1]. Outside the
// configured range the estimate is anchored to the observed extremum;
// use QuantileEx to distinguish such cases.
func (s Snapshot) Quantile(q float64) (float64, error) {
	v, _, err := s.QuantileEx(q)
	return v, err
}

// QuantileEx is Quantile plus the Region in which the estimate falls.
// In-range estimates have relative error bounded by RelativeError;
// underflow/overflow estimates use observed extrema and carry no such
// bucket-error guarantee.
func (s Snapshot) QuantileEx(q float64) (float64, Region, error) {
	if q <= 0 || q > 1 || math.IsNaN(q) {
		return 0, 0, ErrInvalidArgument
	}
	if s.Total == 0 {
		return 0, 0, ErrEmptyHistogram
	}
	n := s.Config.BucketCount()

	// Quantile estimation via a position CDF. Integer sample positions
	// 0..N-1 are assigned anchor values; a fractional position is
	// log-linearly interpolated between its two surrounding anchors.
	//
	// Anchor assignment for a populated region segment of mass m:
	//   - the first sample position gets the segment's first value: the
	//     bucket lower edge (observed extremum for under/overflow);
	//   - every other position gets the segment anchor: the bucket's
	//     geometric-midpoint representative (MinValue for underflow, the
	//     observed maximum for overflow).
	// Because each anchor is either an observed extremum or a populated
	// bucket's representative, and log-linear interpolation stays between
	// its endpoints, every in-range estimate is within RelativeError of
	// the true value. Overflow interpolations up to the observed max are
	// reported with RegionOverflow and carry no bucket-error guarantee.
	type seg struct {
		first, anchor, last float64
		mass                uint64
		region              Region
	}
	segs := make([]seg, 0, 2+n)
	add := func(first, anchor, last float64, mass uint64, region Region) {
		segs = append(segs, seg{math.Log(first), math.Log(anchor), math.Log(last), mass, region})
	}
	if s.Underflow > 0 {
		add(s.UnderflowMin, s.Config.MinValue, s.Config.MinValue, s.Underflow, RegionUnderflow)
	}
	for i, c := range s.Counts {
		if c > 0 {
			v := s.Config.bucketValue(i)
			add(s.Config.bucketLower(i), v, v, c, RegionInRange)
		}
	}
	if s.Overflow > 0 {
		u := s.Config.bucketUpper(n - 1)
		add(u, s.OverflowMax, s.OverflowMax, s.Overflow, RegionOverflow)
	}

	// Map the fractional position directly onto the segment anchor
	// sequence without materialising a per-sample array: within a segment
	// of mass m, position start maps to first and positions start+1..
	// start+m-1 map to anchor, except the final position of the whole
	// distribution maps to the last segment's observed maximum.
	target := q * float64(s.Total-1)
	if target >= float64(s.Total-1) {
		return math.Exp(segs[len(segs)-1].last), segs[len(segs)-1].region, nil
	}
	lo := math.Floor(target)
	frac := target - lo
	start := float64(0)
	for k, sg := range segs {
		end := start + float64(sg.mass)
		if lo >= start && lo < end {
			anchorAt := func(pos float64) (float64, Region) {
				switch {
				case pos == float64(s.Total-1):
					return segs[len(segs)-1].last, segs[len(segs)-1].region
				case pos <= start:
					return sg.first, sg.region
				default:
					// Crossing into the next segment's first anchor.
					if k+1 < len(segs) && pos >= end {
						return segs[k+1].first, segs[k+1].region
					}
					return sg.anchor, sg.region
				}
			}
			a, ra := anchorAt(lo)
			b, rb := anchorAt(lo + 1)
			// A fractional estimate is attributed to the higher anchor's
			// region (conservative at the in-range/overflow boundary).
			region := ra
			if frac > 0 {
				region = rb
			}
			return math.Exp(a + frac*(b-a)), region, nil
		}
		start = end
	}
	last := segs[len(segs)-1]
	return math.Exp(last.last), last.region, nil
}
