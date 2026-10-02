package latslo

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// Config controls the range and precision of a Histogram.
//
// The histogram uses a base-2 logarithmic bucket layout with M = 10^p
// linear sub-buckets per magnitude bucket, where p is SignificantDigits.
// Percentile estimates are reported using the sub-bucket upper edge, giving
// a relative error bound of 1/M = 10^-p (p=2 -> 1%).
type Config struct {
	// MinTrackableValue is the lowest latency the histogram tracks
	// precisely. Samples below it are counted as underflow.
	MinTrackableValue time.Duration
	// MaxTrackableValue is the highest latency the histogram tracks
	// precisely. Samples above it are counted as overflow.
	MaxTrackableValue time.Duration
	// SignificantDigits is the number of significant decimal digits of
	// precision (1..3). Higher values use more memory.
	SignificantDigits int
}

// DefaultConfig returns a configuration tracking 1ns to 1h with 2
// significant digits (1% relative error).
func DefaultConfig() Config {
	return Config{
		MinTrackableValue: 1 * time.Nanosecond,
		MaxTrackableValue: 1 * time.Hour,
		SignificantDigits: 2,
	}
}

// validate checks the configuration for internal consistency.
func (c Config) validate() error {
	if c.SignificantDigits < 1 || c.SignificantDigits > 3 {
		return fmt.Errorf("latslo: SignificantDigits must be in 1..3, got %d", c.SignificantDigits)
	}
	if c.MinTrackableValue < 0 {
		return fmt.Errorf("latslo: MinTrackableValue must be >= 0, got %s", c.MinTrackableValue)
	}
	if c.MaxTrackableValue <= c.MinTrackableValue {
		return fmt.Errorf("latslo: MaxTrackableValue (%s) must be > MinTrackableValue (%s)",
			c.MaxTrackableValue, c.MinTrackableValue)
	}
	return nil
}

// subBucketCount returns the number of linear sub-buckets per magnitude
// bucket (M = 10^p).
func (c Config) subBucketCount() int {
	return int(math.Pow(10, float64(c.SignificantDigits)))
}

// bucket0SubCount returns the number of width-1 sub-buckets in the first
// magnitude bucket (2M).
func (c Config) bucket0SubCount() int {
	return 2 * c.subBucketCount()
}

// magnitudeBucketCount returns the number of magnitude buckets above bucket
// 0. Bucket k covers [2^k * M, 2^(k+1) * M) with M sub-buckets of width 2^k.
func (c Config) magnitudeBucketCount() int {
	M := c.subBucketCount()
	if c.MaxTrackableValue < time.Duration(2*M) {
		return 0
	}
	ratio := float64(c.MaxTrackableValue) / float64(M)
	K := int(math.Floor(math.Log2(ratio)))
	if K < 1 {
		return 0
	}
	return K
}

// bucketCount returns the total number of buckets in the flat array.
func (c Config) bucketCount() int {
	M := c.subBucketCount()
	return c.bucket0SubCount() + c.magnitudeBucketCount()*M
}

// indexForValue maps a latency to its bucket index. The second return value
// is false when the value is outside [MinTrackableValue, MaxTrackableValue]
// (underflow or overflow).
func (c Config) indexForValue(v time.Duration) (int, bool) {
	if v < c.MinTrackableValue || v > c.MaxTrackableValue {
		return 0, false
	}
	M := c.subBucketCount()
	if v < time.Duration(2*M) {
		// Bucket 0: width-1 sub-buckets covering [0, 2M).
		return int(v), true
	}
	// Magnitude bucket k >= 1: covers [2^k * M, 2^(k+1) * M).
	ratio := float64(v) / float64(M)
	k := int(math.Floor(math.Log2(ratio)))
	if k < 1 {
		k = 1
	}
	base := (int64(1) << uint(k)) * int64(M)
	width := int64(1) << uint(k)
	sub := (int64(v) - base) / width
	if sub >= int64(M) {
		sub = int64(M - 1)
	}
	idx := c.bucket0SubCount() + (k-1)*M + int(sub)
	if idx >= c.bucketCount() {
		return c.bucketCount() - 1, true
	}
	return idx, true
}

// upperValueForIndex returns the upper edge of the bucket at index i.
func (c Config) upperValueForIndex(i int) time.Duration {
	M := c.subBucketCount()
	if i < c.bucket0SubCount() {
		return time.Duration(i + 1)
	}
	k := (i-c.bucket0SubCount())/M + 1
	sub := (i - c.bucket0SubCount()) % M
	base := (int64(1) << uint(k)) * int64(M)
	width := int64(1) << uint(k)
	return time.Duration(base + int64(sub+1)*width)
}

// lowerValueForIndex returns the lower edge of the bucket at index i.
func (c Config) lowerValueForIndex(i int) time.Duration {
	M := c.subBucketCount()
	if i < c.bucket0SubCount() {
		return time.Duration(i)
	}
	k := (i-c.bucket0SubCount())/M + 1
	sub := (i - c.bucket0SubCount()) % M
	base := (int64(1) << uint(k)) * int64(M)
	width := int64(1) << uint(k)
	return time.Duration(base + int64(sub)*width)
}

// sourceCount holds one source's grow-only bucket vector.
type sourceCount struct {
	buckets   []int64
	underflow int64
	overflow  int64
	total     int64
	clock     int64
}

// Histogram is a fixed-memory, concurrency-safe latency histogram.
type Histogram struct {
	mu      sync.RWMutex
	config  Config
	id      uint64
	clock   int64
	sources map[uint64]*sourceCount
}

// NewHistogram creates a histogram with the given configuration and source
// id. Source ids must be unique per process instance.
func NewHistogram(cfg Config, id uint64) (*Histogram, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	h := &Histogram{
		config:  cfg,
		id:      id,
		sources: make(map[uint64]*sourceCount),
	}
	h.sources[id] = &sourceCount{buckets: make([]int64, cfg.bucketCount())}
	return h, nil
}

// ID returns the source id of this histogram instance.
func (h *Histogram) ID() uint64 { return h.id }

// Record records a single latency sample.
func (h *Histogram) Record(v time.Duration) {
	h.RecordN(v, 1)
}

// RecordN records n samples of the same latency.
func (h *Histogram) RecordN(v time.Duration, n int64) {
	if n <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	sc := h.sources[h.id]
	if sc == nil {
		sc = &sourceCount{buckets: make([]int64, h.config.bucketCount())}
		h.sources[h.id] = sc
	}
	idx, inRange := h.config.indexForValue(v)
	if inRange {
		sc.buckets[idx] += n
	} else if v < h.config.MinTrackableValue {
		sc.underflow += n
	} else {
		sc.overflow += n
	}
	sc.total += n
	sc.clock += n
	h.clock += n
}

// Snapshot is an immutable, consistent point-in-time view of a histogram.
type Snapshot struct {
	config    Config
	buckets   []int64
	underflow int64
	overflow  int64
	total     int64
}

// Snapshot returns a consistent point-in-time view of the histogram.
func (h *Histogram) Snapshot() Snapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	snap := Snapshot{
		config:  h.config,
		buckets: make([]int64, h.config.bucketCount()),
	}
	for _, sc := range h.sources {
		for i := range sc.buckets {
			snap.buckets[i] += sc.buckets[i]
		}
		snap.underflow += sc.underflow
		snap.overflow += sc.overflow
		snap.total += sc.total
	}
	return snap
}

// Total returns the number of samples recorded (including underflow and
// overflow).
func (s Snapshot) Total() int64 { return s.total }

// Underflow returns the number of samples below MinTrackableValue.
func (s Snapshot) Underflow() int64 { return s.underflow }

// Overflow returns the number of samples above MaxTrackableValue.
func (s Snapshot) Overflow() int64 { return s.overflow }

// InRange returns the number of samples within the tracked range.
func (s Snapshot) InRange() int64 { return s.total - s.underflow - s.overflow }

// ValueAtPercentile returns the upper-bound latency estimate at the given
// percentile p in [0, 1]. The result is the largest latency consistent with
// the histogram, so it is a conservative over-estimate.
func (s Snapshot) ValueAtPercentile(p float64) time.Duration {
	if s.total == 0 {
		return 0
	}
	if p <= 0 {
		return s.Min()
	}
	if p >= 1 {
		return s.Max()
	}
	rank := int64(math.Ceil(p * float64(s.total)))
	if rank < 1 {
		rank = 1
	}
	var cum int64
	for i, c := range s.buckets {
		cum += c
		if cum >= rank {
			return s.config.upperValueForIndex(i)
		}
	}
	return s.config.MaxTrackableValue
}

// CountAbove returns a conservative upper bound on the number of samples
// whose latency is strictly greater than threshold. A bucket is counted
// whenever its upper edge is above the threshold, so the result may
// over-estimate.
func (s Snapshot) CountAbove(threshold time.Duration) int64 {
	var count int64
	for i, c := range s.buckets {
		if s.config.upperValueForIndex(i) > threshold {
			count += c
		}
	}
	count += s.overflow
	return count
}

// Mean returns the estimated mean latency, using bucket midpoints for
// in-range samples and range bounds for underflow/overflow.
func (s Snapshot) Mean() time.Duration {
	if s.total == 0 {
		return 0
	}
	var sum float64
	for i, c := range s.buckets {
		mid := float64(s.config.lowerValueForIndex(i)+s.config.upperValueForIndex(i)) / 2.0
		sum += float64(c) * mid
	}
	sum += float64(s.underflow) * float64(s.config.MinTrackableValue)
	sum += float64(s.overflow) * float64(s.config.MaxTrackableValue)
	return time.Duration(sum / float64(s.total))
}

// Max returns the upper edge of the highest bucket that has samples, or 0
// if no samples have been recorded.
func (s Snapshot) Max() time.Duration {
	for i := len(s.buckets) - 1; i >= 0; i-- {
		if s.buckets[i] > 0 {
			return s.config.upperValueForIndex(i)
		}
	}
	if s.overflow > 0 {
		return s.config.MaxTrackableValue
	}
	return 0
}

// Min returns the lower edge of the lowest bucket that has samples, or 0 if
// no samples have been recorded.
func (s Snapshot) Min() time.Duration {
	if s.underflow > 0 {
		return s.config.MinTrackableValue
	}
	for i, c := range s.buckets {
		if c > 0 {
			return s.config.lowerValueForIndex(i)
		}
	}
	return 0
}

// Config returns the histogram configuration.
func (s Snapshot) Config() Config { return s.config }
