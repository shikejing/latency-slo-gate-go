package latslo

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

func TestHistogramErrorBound(t *testing.T) {
	cfg := DefaultConfig()
	h, _ := NewHistogram(cfg, 1)
	v := 12345 * time.Nanosecond
	for i := 0; i < 10000; i++ {
		h.Record(v)
	}
	snap := h.Snapshot()
	for _, p := range []float64{0.5, 0.9, 0.99, 0.999, 1.0} {
		got := snap.ValueAtPercentile(p)
		if got < v {
			t.Errorf("p%.3f = %s < recorded %s (under-estimate)", p, got, v)
		}
		rel := float64(got-v) / float64(v)
		if rel > 0.02 {
			t.Errorf("p%.3f = %s, rel error %.4f exceeds 2%% bound", p, got, rel)
		}
	}
}

func TestHistogramUnderflowOverflow(t *testing.T) {
	cfg := DefaultConfig()
	h, _ := NewHistogram(cfg, 1)
	h.Record(0)
	h.Record(2 * time.Hour)
	h.Record(500 * time.Millisecond)
	snap := h.Snapshot()
	if snap.Total() != 3 {
		t.Fatalf("total=%d, want 3", snap.Total())
	}
	if snap.Underflow() != 1 {
		t.Errorf("underflow=%d, want 1", snap.Underflow())
	}
	if snap.Overflow() != 1 {
		t.Errorf("overflow=%d, want 1", snap.Overflow())
	}
	if snap.InRange() != 1 {
		t.Errorf("inRange=%d, want 1", snap.InRange())
	}
}

func TestHistogramPercentileMonotonic(t *testing.T) {
	cfg := DefaultConfig()
	h, _ := NewHistogram(cfg, 1)
	rng := rand.New(rand.NewSource(123))
	lo := float64(time.Microsecond)
	hi := float64(10 * time.Minute)
	n := 100000
	for i := 0; i < n; i++ {
		v := time.Duration(lo * math.Pow(10, rng.Float64()*math.Log10(hi/lo)))
		h.Record(v)
	}
	snap := h.Snapshot()
	prev := time.Duration(0)
	for _, p := range []float64{0.1, 0.25, 0.5, 0.75, 0.9, 0.95, 0.99, 0.999} {
		got := snap.ValueAtPercentile(p)
		if got < prev {
			t.Errorf("p%.3f = %s < previous %s (non-monotonic)", p, got, prev)
		}
		prev = got
	}
}

func TestHistogramDeterminism(t *testing.T) {
	cfg := DefaultConfig()
	rng := rand.New(rand.NewSource(555))
	values := make([]time.Duration, 5000)
	for i := range values {
		values[i] = time.Duration(rng.Int63n(int64(time.Hour)))
	}
	build := func() Snapshot {
		h, _ := NewHistogram(cfg, 1)
		for _, v := range values {
			h.Record(v)
		}
		return h.Snapshot()
	}
	s1 := build()
	s2 := build()
	if s1.Total() != s2.Total() {
		t.Fatal("non-deterministic total")
	}
	for i := range s1.buckets {
		if s1.buckets[i] != s2.buckets[i] {
			t.Fatalf("non-deterministic bucket %d", i)
		}
	}
}

func TestHistogramSnapshotConsistency(t *testing.T) {
	cfg := DefaultConfig()
	h, _ := NewHistogram(cfg, 1)
	for i := 0; i < 1000; i++ {
		h.Record(time.Duration(i) * time.Microsecond)
	}
	snap := h.Snapshot()
	var sum int64
	for _, c := range snap.buckets {
		sum += c
	}
	if sum != snap.InRange() {
		t.Errorf("bucket sum %d != inRange %d", sum, snap.InRange())
	}
	if snap.InRange()+snap.Underflow()+snap.Overflow() != snap.Total() {
		t.Errorf("inRange+under+over != total")
	}
}
