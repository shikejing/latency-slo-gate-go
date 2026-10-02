package latslo

import (
	"math/rand"
	"testing"
	"time"
)

func TestMergeIdempotent(t *testing.T) {
	cfg := DefaultConfig()
	a, _ := NewHistogram(cfg, 1)
	b, _ := NewHistogram(cfg, 2)
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 10000; i++ {
		a.Record(time.Duration(rng.Int63n(int64(time.Hour))))
		b.Record(time.Duration(rng.Int63n(int64(time.Hour))))
	}
	before := b.Snapshot()
	if err := a.Merge(b); err != nil {
		t.Fatal(err)
	}
	after1 := a.Snapshot()
	if err := a.Merge(b); err != nil {
		t.Fatal(err)
	}
	after2 := a.Snapshot()
	if after1.Total() != after2.Total() {
		t.Fatalf("idempotency violated: total %d -> %d", after1.Total(), after2.Total())
	}
	for i := range after1.buckets {
		if after1.buckets[i] != after2.buckets[i] {
			t.Fatalf("idempotency violated at bucket %d", i)
		}
	}
	_ = before
}

func TestMergeCommutativeAndAssociative(t *testing.T) {
	cfg := DefaultConfig()
	rng := rand.New(rand.NewSource(7))
	mk := func(id uint64) *Histogram {
		h, _ := NewHistogram(cfg, id)
		for i := 0; i < 5000; i++ {
			h.Record(time.Duration(rng.Int63n(int64(time.Hour))))
		}
		return h
	}
	a, b, c := mk(1), mk(2), mk(3)

	// (a merge b) merge c
	ab, _ := NewHistogram(cfg, 100)
	ab.Merge(a)
	ab.Merge(b)
	ab.Merge(c)
	// a merge (b merge c)
	bc, _ := NewHistogram(cfg, 101)
	bc.Merge(b)
	bc.Merge(c)
	bc.Merge(a)
	s1 := ab.Snapshot()
	s2 := bc.Snapshot()
	if s1.Total() != s2.Total() {
		t.Fatalf("associativity violated: %d vs %d", s1.Total(), s2.Total())
	}
	for i := range s1.buckets {
		if s1.buckets[i] != s2.buckets[i] {
			t.Fatalf("associativity violated at bucket %d: %d vs %d", i, s1.buckets[i], s2.buckets[i])
		}
	}
}

func TestMergeStatisticalEquivalence(t *testing.T) {
	cfg := DefaultConfig()
	rng := rand.New(rand.NewSource(99))
	// Single instance gets all samples.
	single, _ := NewHistogram(cfg, 1)
	// Three instances split samples.
	h1, _ := NewHistogram(cfg, 2)
	h2, _ := NewHistogram(cfg, 3)
	h3, _ := NewHistogram(cfg, 4)
	for i := 0; i < 30000; i++ {
		v := time.Duration(rng.Int63n(int64(time.Hour)))
		single.Record(v)
		switch i % 3 {
		case 0:
			h1.Record(v)
		case 1:
			h2.Record(v)
		default:
			h3.Record(v)
		}
	}
	merged, _ := NewHistogram(cfg, 5)
	merged.Merge(h1)
	merged.Merge(h2)
	merged.Merge(h3)
	ss := single.Snapshot()
	ms := merged.Snapshot()
	if ss.Total() != ms.Total() {
		t.Fatalf("total mismatch: %d vs %d", ss.Total(), ms.Total())
	}
	for _, p := range []float64{0.5, 0.9, 0.99, 0.999} {
		sv := ss.ValueAtPercentile(p)
		mv := ms.ValueAtPercentile(p)
		rel := float64(sv-mv) / float64(sv)
		if rel < 0 {
			rel = -rel
		}
		if rel > 0.02 {
			t.Fatalf("p%.3f mismatch: single=%s merged=%s rel=%.4f", p, sv, mv, rel)
		}
	}
}

func TestAddDisjoint(t *testing.T) {
	cfg := DefaultConfig()
	a, _ := NewHistogram(cfg, 1)
	b, _ := NewHistogram(cfg, 2)
	for i := 0; i < 1000; i++ {
		a.Record(10 * time.Millisecond)
	}
	for i := 0; i < 2000; i++ {
		b.Record(20 * time.Millisecond)
	}
	if err := a.Add(b); err != nil {
		t.Fatal(err)
	}
	snap := a.Snapshot()
	if snap.Total() != 3000 {
		t.Errorf("total=%d, want 3000", snap.Total())
	}
	// Adding b again double-counts (Add is for disjoint histograms only).
	if err := a.Add(b); err != nil {
		t.Fatal(err)
	}
	if snap.Total() != 3000 {
		t.Errorf("snapshot changed after second Add (snapshot is immutable)")
	}
	if a.Snapshot().Total() != 5000 {
		t.Errorf("total after second Add=%d, want 5000", a.Snapshot().Total())
	}
}

func TestAddConfigMismatch(t *testing.T) {
	cfg1 := DefaultConfig()
	cfg2 := DefaultConfig()
	cfg2.SignificantDigits = 3
	a, _ := NewHistogram(cfg1, 1)
	b, _ := NewHistogram(cfg2, 2)
	if err := a.Add(b); err == nil {
		t.Error("expected error for config mismatch")
	}
	if err := a.Merge(b); err == nil {
		t.Error("expected error for config mismatch")
	}
}
