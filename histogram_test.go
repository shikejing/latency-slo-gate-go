package slogate

import (
	"math"
	"sync"
	"testing"
)

func TestHistogramBasic(t *testing.T) {
	cfg := HistogramConfig{MinValue: 1, MaxValue: 1000, RelativeError: 0.05}
	h, _ := New(cfg)
	h.Observe(0.1) // underflow
	for _, v := range []float64{1, 2, 3, 5, 10, 20, 50, 100, 200, 500} {
		h.Observe(v)
	}
	h.ObserveOutcome(5000, true) // overflow + error
	s := h.Snapshot()
	if s.Total != 12 || s.Underflow != 1 || s.Overflow != 1 || s.Errors != 1 {
		t.Fatalf("counts total=%d under=%d over=%d err=%d", s.Total, s.Underflow, s.Overflow, s.Errors)
	}
	if s.UnderflowMin != 0.1 || s.OverflowMax != 5000 {
		t.Fatalf("extrema %v %v", s.UnderflowMin, s.OverflowMax)
	}
	if _, r, err := s.QuantileEx(0.5); err != nil || r != RegionInRange {
		t.Fatalf("median region=%v err=%v", r, err)
	}
	q90, r90, _ := s.QuantileEx(0.9)
	if !(q90 >= 200 && q90 <= 500*1.05) {
		t.Fatalf("p90 %v", q90)
	}
	if r90 != RegionInRange {
		t.Fatalf("p90 region %v", r90)
	}
	q99, r99, _ := s.QuantileEx(0.99)
	if r99 != RegionOverflow || q99 < 500 {
		t.Fatalf("p99 %v region %v", q99, r99)
	}
}

func TestQuantileErrorBound(t *testing.T) {
	cfg := HistogramConfig{MinValue: 0.1, MaxValue: 1e6, RelativeError: 0.01}
	h, _ := New(cfg)
	// Dense log-uniform samples inside the range.
	const N = 200000
	lo, hi := math.Log(1.0), math.Log(1e5)
	values := make([]float64, N)
	for i := 0; i < N; i++ {
		u := float64(i) / (N - 1)
		values[i] = math.Exp(lo + u*(hi-lo))
		h.Observe(values[i])
	}
	s := h.Snapshot()
	if s.Underflow != 0 || s.Overflow != 0 {
		t.Fatalf("unexpected under/over %d %d", s.Underflow, s.Overflow)
	}
	for _, q := range []float64{0.5, 0.9, 0.95, 0.99, 0.999} {
		est, region, err := s.QuantileEx(q)
		if err != nil || region != RegionInRange {
			t.Fatalf("q=%v region=%v err=%v", q, region, err)
		}
		// Log-uniform exact quantile.
		truth := math.Exp(lo + q*(hi-lo))
		re := math.Abs(est-truth) / truth
		// allow a small margin beyond RelativeError for interpolation.
		if re > cfg.RelativeError+0.002 {
			t.Fatalf("q=%v est=%v truth=%v relerr=%v", q, est, truth, re)
		}
	}
}

func TestHistogramConcurrent(t *testing.T) {
	h, _ := New(DefaultConfig())
	var wg sync.WaitGroup
	const workers, per = 8, 10000
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				h.Observe(float64(1 + (i % 500)))
				if i%37 == 0 {
					h.Snapshot()
				}
			}
		}()
	}
	wg.Wait()
	s := h.Snapshot()
	if s.Total != workers*per {
		t.Fatalf("lost/dup samples: %d", s.Total)
	}
	var want float64
	for i := 0; i < per; i++ {
		want += float64(1 + (i % 500))
	}
	want *= workers
	if math.Abs(s.Sum-want) > 1e-6*s.Sum {
		t.Fatal("sum mismatch")
	}
}

func TestSnapshotConsistency(t *testing.T) {
	h, _ := New(DefaultConfig())
	for i := 0; i < 1000; i++ {
		h.Observe(float64(1 + i))
	}
	s := h.Snapshot()
	var in uint64
	for _, c := range s.Counts {
		in += c
	}
	if in+s.Underflow+s.Overflow != s.Total {
		t.Fatal("inconsistent snapshot")
	}
}
