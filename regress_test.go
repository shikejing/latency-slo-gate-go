package latslo

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func buildHistogram(cfg Config, id uint64, values []time.Duration) *Histogram {
	h, _ := NewHistogram(cfg, id)
	for _, v := range values {
		h.Record(v)
	}
	return h
}

func TestRegressionNoChange(t *testing.T) {
	cfg := DefaultConfig()
	rng := rand.New(rand.NewSource(1))
	values := make([]time.Duration, 5000)
	for i := range values {
		values[i] = time.Duration(10*time.Millisecond + time.Duration(rng.Intn(1000)))
	}
	baseline := buildHistogram(cfg, 1, values).Snapshot()
	current := buildHistogram(cfg, 2, values).Snapshot()
	gate := NewRegressionGate(DefaultRegressionConfig())
	res := gate.Compare(current, baseline)
	if res.Conclusion != ConclusionNoChange {
		t.Errorf("conclusion=%s, want NO_CHANGE: %s", res.Conclusion, res.Reason)
	}
}

func TestRegressionDegraded(t *testing.T) {
	cfg := DefaultConfig()
	rng := rand.New(rand.NewSource(2))
	baselineVals := make([]time.Duration, 5000)
	currentVals := make([]time.Duration, 5000)
	for i := range baselineVals {
		baselineVals[i] = time.Duration(10*time.Millisecond + time.Duration(rng.Intn(100)))
		currentVals[i] = time.Duration(20*time.Millisecond + time.Duration(rng.Intn(100)))
	}
	baseline := buildHistogram(cfg, 1, baselineVals).Snapshot()
	current := buildHistogram(cfg, 2, currentVals).Snapshot()
	gate := NewRegressionGate(DefaultRegressionConfig())
	res := gate.Compare(current, baseline)
	if res.Conclusion != ConclusionDegraded {
		t.Errorf("conclusion=%s, want DEGRADED: %s", res.Conclusion, res.Reason)
	}
}

func TestRegressionImproved(t *testing.T) {
	cfg := DefaultConfig()
	rng := rand.New(rand.NewSource(3))
	baselineVals := make([]time.Duration, 5000)
	currentVals := make([]time.Duration, 5000)
	for i := range baselineVals {
		baselineVals[i] = time.Duration(20*time.Millisecond + time.Duration(rng.Intn(100)))
		currentVals[i] = time.Duration(10*time.Millisecond + time.Duration(rng.Intn(100)))
	}
	baseline := buildHistogram(cfg, 1, baselineVals).Snapshot()
	current := buildHistogram(cfg, 2, currentVals).Snapshot()
	gate := NewRegressionGate(DefaultRegressionConfig())
	res := gate.Compare(current, baseline)
	if res.Conclusion != ConclusionImproved {
		t.Errorf("conclusion=%s, want IMPROVED: %s", res.Conclusion, res.Reason)
	}
}

func TestRegressionInconclusiveInsufficientSamples(t *testing.T) {
	cfg := DefaultConfig()
	baseline := buildHistogram(cfg, 1, []time.Duration{10 * time.Millisecond}).Snapshot()
	current := buildHistogram(cfg, 2, []time.Duration{20 * time.Millisecond}).Snapshot()
	gate := NewRegressionGate(DefaultRegressionConfig())
	res := gate.Compare(current, baseline)
	if res.Conclusion != ConclusionInconclusive {
		t.Errorf("conclusion=%s, want INCONCLUSIVE", res.Conclusion)
	}
}

func TestRegressionInconclusiveHighVariance(t *testing.T) {
	cfg := DefaultConfig()
	rng := rand.New(rand.NewSource(4))
	gen := func() []time.Duration {
		vals := make([]time.Duration, 200)
		for i := range vals {
			vals[i] = time.Duration(float64(time.Millisecond) * math.Pow(10, rng.Float64()*3))
		}
		return vals
	}
	baseline := buildHistogram(cfg, 1, gen()).Snapshot()
	current := buildHistogram(cfg, 2, gen()).Snapshot()
	rc := DefaultRegressionConfig()
	rc.MinSamples = 100
	gate := NewRegressionGate(rc)
	res := gate.Compare(current, baseline)
	if res.Conclusion != ConclusionInconclusive {
		t.Errorf("conclusion=%s, want INCONCLUSIVE (high variance): %s", res.Conclusion, res.Reason)
	}
}

func TestRegressionDeterminism(t *testing.T) {
	cfg := DefaultConfig()
	rng := rand.New(rand.NewSource(5))
	gen := func(shift time.Duration) []time.Duration {
		vals := make([]time.Duration, 2000)
		for i := range vals {
			vals[i] = time.Duration(10*time.Millisecond+shift) + time.Duration(rng.Intn(1000))
		}
		return vals
	}
	baseline := buildHistogram(cfg, 1, gen(0)).Snapshot()
	current := buildHistogram(cfg, 2, gen(5*time.Millisecond)).Snapshot()
	gate := NewRegressionGate(DefaultRegressionConfig())
	r1 := gate.Compare(current, baseline)
	r2 := gate.Compare(current, baseline)
	if r1.Conclusion != r2.Conclusion || r1.RatioLower != r2.RatioLower || r1.RatioUpper != r2.RatioUpper {
		t.Errorf("non-deterministic: %+v vs %+v", r1, r2)
	}
}
