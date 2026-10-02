package slogate

import (
	"math"
	"math/rand"
	"testing"
)

func logNormalSnap(t *testing.T, seed int64, n int, median float64, sigma float64) Snapshot {
	t.Helper()
	h, err := New(HistogramConfig{MinValue: 0.01, MaxValue: 1e6, RelativeError: 0.005})
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewSource(seed))
	for i := 0; i < n; i++ {
		v := median * math.Exp(r.NormFloat64()*sigma)
		h.Observe(v)
	}
	return h.Snapshot()
}

func TestRegressionDetectsReal(t *testing.T) {
	base := logNormalSnap(t, 42, 2000, 100, 0.1)
	cur := logNormalSnap(t, 7, 2000, 140, 0.1) // +40% median
	d := NewDetector(RegressionConfig{MinSamples: 200, Alpha: 0.05, Tolerance: 0.05,
		NoiseMargin: 0.01, BootstrapReplicates: 2000, Seed: 1})
	res := d.Compare(cur, base)
	if res.Verdict != VerdictRegression {
		t.Fatalf("want regression, got %v reasons=%v p=%g eff=%g CI=[%g,%g]",
			res.Verdict, res.Reasons, res.PValueUp, res.EstEffect, res.CILeft, res.CIRight)
	}
}

func TestRegressionImprovement(t *testing.T) {
	base := logNormalSnap(t, 42, 2000, 140, 0.1)
	cur := logNormalSnap(t, 7, 2000, 95, 0.1)
	d := NewDetector(RegressionConfig{MinSamples: 200, Alpha: 0.05, Tolerance: 0.05,
		NoiseMargin: 0.01, BootstrapReplicates: 2000, Seed: 1})
	if res := d.Compare(cur, base); res.Verdict != VerdictImprovement {
		t.Fatalf("want improvement got %v", res.Verdict)
	}
}

func TestRegressionNoiseInconclusiveOrNoReg(t *testing.T) {
	// Same distribution: must never claim regression.
	base := logNormalSnap(t, 100, 800, 100, 0.5)
	cur := logNormalSnap(t, 101, 800, 102, 0.5) // tiny 2% + high variance
	d := NewDetector(RegressionConfig{MinSamples: 200, Alpha: 0.05, Tolerance: 0.10,
		NoiseMargin: 0.02, BootstrapReplicates: 2000, Seed: 1})
	res := d.Compare(cur, base)
	if res.Verdict == VerdictRegression {
		t.Fatalf("noise misjudged as regression: %g CI=[%g,%g]", res.EstEffect, res.CILeft, res.CIRight)
	}
}

func TestRegressionInsufficientSamples(t *testing.T) {
	h1, _ := New(DefaultConfig())
	h2, _ := New(DefaultConfig())
	for i := 0; i < 10; i++ {
		h1.Observe(float64(100 + i))
		h2.Observe(float64(200 + i))
	}
	d := NewDetector(DefaultRegressionConfig())
	res := d.Compare(h2.Snapshot(), h1.Snapshot())
	if res.Verdict != VerdictInconclusive || res.Reasons[0] != RegReasonInsufficientSamples {
		t.Fatalf("got %v %v", res.Verdict, res.Reasons)
	}
}

func TestRegressionDeterministic(t *testing.T) {
	base := logNormalSnap(t, 5, 1500, 100, 0.2)
	cur := logNormalSnap(t, 6, 1500, 115, 0.2)
	cfg := RegressionConfig{MinSamples: 100, Alpha: 0.05, Tolerance: 0.05,
		NoiseMargin: 0.01, BootstrapReplicates: 1500, Seed: 99}
	r1 := NewDetector(cfg).Compare(cur, base)
	r2 := NewDetector(cfg).Compare(cur, base)
	if r1.CILeft != r2.CILeft || r1.CIRight != r2.CIRight || r1.PValueUp != r2.PValueUp {
		t.Fatal("non-reproducible statistics")
	}
	// Different seed may differ.
	cfg2 := cfg
	cfg2.Seed = 100
	r3 := NewDetector(cfg2).Compare(cur, base)
	_ = r3
}
