package latslo

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"
)

// Conclusion is the regression gate verdict.
type Conclusion int

const (
	// ConclusionInconclusive means the gate cannot emit a deterministic
	// verdict (insufficient samples or high variance).
	ConclusionInconclusive Conclusion = iota
	// ConclusionNoChange means current and baseline are statistically
	// equivalent within the tolerance.
	ConclusionNoChange
	// ConclusionDegraded means current is statistically worse than
	// baseline beyond the tolerance.
	ConclusionDegraded
	// ConclusionImproved means current is statistically better than
	// baseline beyond the tolerance.
	ConclusionImproved
)

// String returns the human-readable name of the conclusion.
func (c Conclusion) String() string {
	switch c {
	case ConclusionInconclusive:
		return "INCONCLUSIVE"
	case ConclusionNoChange:
		return "NO_CHANGE"
	case ConclusionDegraded:
		return "DEGRADED"
	case ConclusionImproved:
		return "IMPROVED"
	default:
		return "UNKNOWN"
	}
}

// RegressionConfig controls the regression gate.
type RegressionConfig struct {
	// MinSamples is the minimum sample count required on each side.
	MinSamples int64
	// Tolerance is the relative equivalence margin, e.g. 0.1 means the
	// confidence interval must stay within +/-10% of the baseline.
	Tolerance float64
	// Alpha is the significance level, e.g. 0.05.
	Alpha float64
	// BootstrapIterations is the number of bootstrap resamples.
	BootstrapIterations int
	// Seed seeds the bootstrap RNG for deterministic output.
	Seed int64
	// Percentile is the percentile to compare, e.g. 0.99.
	Percentile float64
}

// DefaultRegressionConfig returns a config with sensible defaults.
func DefaultRegressionConfig() RegressionConfig {
	return RegressionConfig{
		MinSamples:          1000,
		Tolerance:           0.1,
		Alpha:               0.05,
		BootstrapIterations: 200,
		Seed:                1,
		Percentile:          0.99,
	}
}

// RegressionResult is the regression gate verdict.
type RegressionResult struct {
	Conclusion         Conclusion
	Reason             string
	Percentile         float64
	CurrentPercentile  time.Duration
	BaselinePercentile time.Duration
	CurrentSamples     int64
	BaselineSamples    int64
	RatioLower         float64 // bootstrap CI lower bound of current/baseline
	RatioUpper         float64 // bootstrap CI upper bound of current/baseline
}

// RegressionGate compares a current snapshot against a historical baseline.
type RegressionGate struct {
	config RegressionConfig
}

// NewRegressionGate creates a regression gate.
func NewRegressionGate(cfg RegressionConfig) *RegressionGate {
	if cfg.MinSamples <= 0 {
		cfg.MinSamples = 1000
	}
	if cfg.Tolerance <= 0 {
		cfg.Tolerance = 0.1
	}
	if cfg.Alpha <= 0 || cfg.Alpha >= 1 {
		cfg.Alpha = 0.05
	}
	if cfg.BootstrapIterations <= 0 {
		cfg.BootstrapIterations = 200
	}
	if cfg.Percentile <= 0 || cfg.Percentile >= 1 {
		cfg.Percentile = 0.99
	}
	return &RegressionGate{config: cfg}
}

// Compare evaluates current against baseline. When either side has fewer
// than MinSamples, or when the bootstrap confidence interval crosses the
// equivalence margin, the conclusion is Inconclusive.
func (g *RegressionGate) Compare(current, baseline Snapshot) RegressionResult {
	res := RegressionResult{
		Percentile:         g.config.Percentile,
		CurrentSamples:     current.Total(),
		BaselineSamples:    baseline.Total(),
		CurrentPercentile:  current.ValueAtPercentile(g.config.Percentile),
		BaselinePercentile: baseline.ValueAtPercentile(g.config.Percentile),
	}
	if current.Total() < g.config.MinSamples || baseline.Total() < g.config.MinSamples {
		res.Conclusion = ConclusionInconclusive
		res.Reason = fmt.Sprintf("insufficient samples: current=%d baseline=%d min=%d",
			current.Total(), baseline.Total(), g.config.MinSamples)
		return res
	}

	rng := rand.New(rand.NewSource(g.config.Seed))
	ratios := make([]float64, g.config.BootstrapIterations)
	for b := 0; b < g.config.BootstrapIterations; b++ {
		cp := g.bootstrapPercentile(current, rng)
		bp := g.bootstrapPercentile(baseline, rng)
		if bp == 0 {
			ratios[b] = 1.0
		} else {
			ratios[b] = float64(cp) / float64(bp)
		}
	}
	sort.Float64s(ratios)
	loIdx := int(g.config.Alpha / 2 * float64(g.config.BootstrapIterations))
	hiIdx := int((1 - g.config.Alpha/2) * float64(g.config.BootstrapIterations))
	if loIdx < 0 {
		loIdx = 0
	}
	if hiIdx >= len(ratios) {
		hiIdx = len(ratios) - 1
	}
	res.RatioLower = ratios[loIdx]
	res.RatioUpper = ratios[hiIdx]

	margin := 1 + g.config.Tolerance
	if res.RatioLower > margin {
		res.Conclusion = ConclusionDegraded
		res.Reason = fmt.Sprintf("current is degraded: ratio CI [%.4f, %.4f] > %.4f",
			res.RatioLower, res.RatioUpper, margin)
		return res
	}
	if res.RatioUpper < 1-g.config.Tolerance {
		res.Conclusion = ConclusionImproved
		res.Reason = fmt.Sprintf("current is improved: ratio CI [%.4f, %.4f] < %.4f",
			res.RatioLower, res.RatioUpper, 1-g.config.Tolerance)
		return res
	}
	if res.RatioLower >= 1-g.config.Tolerance && res.RatioUpper <= margin {
		res.Conclusion = ConclusionNoChange
		res.Reason = fmt.Sprintf("no change: ratio CI [%.4f, %.4f] within [%.4f, %.4f]",
			res.RatioLower, res.RatioUpper, 1-g.config.Tolerance, margin)
		return res
	}
	res.Conclusion = ConclusionInconclusive
	res.Reason = fmt.Sprintf("high variance: ratio CI [%.4f, %.4f] crosses equivalence margin [%.4f, %.4f]",
		res.RatioLower, res.RatioUpper, 1-g.config.Tolerance, margin)
	return res
}

// bootstrapPercentile resamples the bucket counts via a Poisson bootstrap
// and returns the percentile of the resampled distribution.
func (g *RegressionGate) bootstrapPercentile(snap Snapshot, rng *rand.Rand) time.Duration {
	buckets := make([]int64, len(snap.buckets))
	var total, under int64
	for i, c := range snap.buckets {
		rc := poisson(rng, float64(c))
		buckets[i] = rc
		total += rc
	}
	under = poisson(rng, float64(snap.underflow))
	total += under
	total += poisson(rng, float64(snap.overflow))
	if total == 0 {
		return 0
	}
	rank := int64(math.Ceil(g.config.Percentile * float64(total)))
	if rank < 1 {
		rank = 1
	}
	var cum int64 = under
	for i, c := range buckets {
		cum += c
		if cum >= rank {
			return snap.config.upperValueForIndex(i)
		}
	}
	return snap.config.MaxTrackableValue
}

// poisson draws a sample from a Poisson distribution with the given mean.
// Uses a normal approximation for large lambda and Knuth's algorithm for
// small lambda.
func poisson(rng *rand.Rand, lambda float64) int64 {
	if lambda <= 0 {
		return 0
	}
	if lambda > 20 {
		v := rng.NormFloat64()*math.Sqrt(lambda) + lambda
		if v < 0 {
			v = 0
		}
		return int64(math.Round(v))
	}
	L := math.Exp(-lambda)
	k := 0
	p := 1.0
	for p > L {
		k++
		p *= rng.Float64()
	}
	return int64(k - 1)
}
