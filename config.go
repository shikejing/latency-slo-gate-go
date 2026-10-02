package slogate

import "math"

// HistogramConfig describes a fixed-memory log-scaled latency histogram.
//
// The histogram keeps N fixed buckets. Bucket i covers the half-open
// interval
//
//	[MinValue*g^i, MinValue*g^(i+1))
//
// where g = (1+RelativeError)^2. A sample is represented by the geometric
// midpoint MinValue*g^(i+0.5), whose relative deviation from any value in
// the bucket is at most RelativeError. Samples below MinValue land in the
// underflow region; samples at or above the upper boundary of the last
// bucket land in the overflow region. Both are counted and identifiable,
// so the total sample count is always exact even when memory is not.
type HistogramConfig struct {
	// MinValue is the smallest trackable latency (inclusive). Samples
	// strictly below it are counted in the underflow region. Must be > 0.
	MinValue float64
	// MaxValue is the largest intended trackable latency. It lies within
	// the last in-range bucket; samples at or above that bucket's upper
	// boundary are counted in the overflow region. Must be > MinValue.
	MaxValue float64
	// RelativeError bounds the relative error of per-bucket value
	// estimates. Must be in (0, 1).
	RelativeError float64
}

// DefaultConfig returns a config covering 1..1_000_000 latency units
// (e.g. nanoseconds) at approximately 1% relative bucket error.
func DefaultConfig() HistogramConfig {
	return HistogramConfig{
		MinValue:      1,
		MaxValue:      1_000_000,
		RelativeError: 0.01,
	}
}

// Validate checks the configuration.
func (c HistogramConfig) Validate() error {
	if !(c.MinValue > 0) || !(c.MinValue == c.MinValue) || math.IsInf(c.MinValue, 0) {
		return ErrInvalidConfig
	}
	if !(c.MaxValue > c.MinValue) || !(c.MaxValue == c.MaxValue) || math.IsInf(c.MaxValue, 0) {
		return ErrInvalidConfig
	}
	if c.RelativeError <= 0 || c.RelativeError >= 1 {
		return ErrInvalidConfig
	}
	return nil
}

// growth returns the per-bucket growth factor g.
func (c HistogramConfig) growth() float64 {
	e := 1 + c.RelativeError
	return e * e
}

// BucketCount returns the number of in-range buckets.
func (c HistogramConfig) BucketCount() int {
	if err := c.Validate(); err != nil {
		return 0
	}
	n := int(math.Floor(math.Log(c.MaxValue/c.MinValue)/math.Log(c.growth()))) + 1
	if n < 1 {
		n = 1
	}
	return n
}

// bucketIndex maps a sample value to its bucket index. It returns -1 for
// underflow values and N (BucketCount) for overflow values.
func (c HistogramConfig) bucketIndex(v float64) int {
	if v != v || math.IsInf(v, 0) || v < c.MinValue {
		return -1
	}
	n := c.BucketCount()
	g := c.growth()
	i := int(math.Floor(math.Log(v/c.MinValue) / math.Log(g)))
	// Correct floating-point drift with explicit boundary checks.
	for i >= 0 && v < c.MinValue*math.Pow(g, float64(i)) {
		i--
	}
	for float64(i+1) < 0 || v >= c.MinValue*math.Pow(g, float64(i+1)) {
		i++
	}
	if i < 0 {
		return -1
	}
	if i >= n {
		return n
	}
	return i
}

// bucketLower returns the lower (inclusive) boundary of bucket i.
func (c HistogramConfig) bucketLower(i int) float64 {
	return c.MinValue * math.Pow(c.growth(), float64(i))
}

// bucketUpper returns the upper (exclusive) boundary of bucket i.
func (c HistogramConfig) bucketUpper(i int) float64 {
	return c.MinValue * math.Pow(c.growth(), float64(i+1))
}

// bucketValue returns the geometric-midpoint estimate for bucket i.
func (c HistogramConfig) bucketValue(i int) float64 {
	return c.MinValue * math.Pow(c.growth(), float64(i)+0.5)
}

// equal reports whether two configs describe the same layout.
func (c HistogramConfig) equal(o HistogramConfig) bool {
	return c.MinValue == o.MinValue &&
		c.MaxValue == o.MaxValue &&
		c.RelativeError == o.RelativeError
}
