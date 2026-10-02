package slogate

import (
	"sync"
	"testing"
)

func makeSnap(t *testing.T, id string, epoch uint64, seq uint64, vals []float64) Snapshot {
	t.Helper()
	h, err := NewWithSource(DefaultConfig(), SourceID{ID: id, Epoch: epoch})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vals {
		h.Observe(v)
	}
	s := h.Snapshot()
	s.Seq = seq
	return s
}

func TestMergeDistinctAndDuplicate(t *testing.T) {
	cfg := DefaultConfig()
	a := makeSnap(t, "a", 1, 1, []float64{1, 10, 100})
	b := makeSnap(t, "b", 1, 1, []float64{2, 20, 200})
	m := NewMerger(cfg)
	if err := m.Add(a); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(a); err != nil {
		t.Fatalf("idempotent re-add failed: %v", err)
	}
	if err := m.Add(b); err != nil {
		t.Fatal(err)
	}
	out := m.Snapshot()
	if out.Total != 6 {
		t.Fatalf("merged total %d", out.Total)
	}
	// Newer snapshot from a replaces its older one.
	a2 := makeSnap(t, "a", 1, 2, []float64{1, 10, 100, 1000})
	if err := m.Add(a2); err != nil {
		t.Fatal(err)
	}
	out = m.Snapshot()
	if out.Total != 7 {
		t.Fatalf("after replace total %d", out.Total)
	}
	// Older seq rejected.
	if err := m.Add(a); err == nil {
		t.Fatal("expected regression error for older seq")
	}
}

func TestMergeAssociative(t *testing.T) {
	cfg := DefaultConfig()
	a := makeSnap(t, "a", 1, 1, []float64{1, 2, 3})
	b := makeSnap(t, "b", 1, 1, []float64{4, 5, 6})
	c := makeSnap(t, "c", 1, 1, []float64{7, 8, 9})

	left := NewMerger(cfg)
	g1 := NewMerger(cfg)
	_ = g1.Add(a)
	_ = g1.Add(b)
	_ = left.Merge(g1)
	g2 := NewMerger(cfg)
	_ = g2.Add(c)
	_ = left.Merge(g2)

	right := NewMerger(cfg)
	g3 := NewMerger(cfg)
	_ = g3.Add(b)
	_ = g3.Add(c)
	_ = right.Merge(g3)
	g4 := NewMerger(cfg)
	_ = g4.Add(a)
	_ = right.Merge(g4)

	l, r := left.Snapshot(), right.Snapshot()
	if l.Total != 9 || r.Total != 9 || l.Total != r.Total {
		t.Fatalf("totals %d %d", l.Total, r.Total)
	}
	for i := range l.Counts {
		if l.Counts[i] != r.Counts[i] {
			t.Fatalf("bucket %d: %d vs %d", i, l.Counts[i], r.Counts[i])
		}
	}
	// Merging again is a no-op (same sources/seqs).
	if err := left.Merge(right); err != nil {
		t.Fatalf("re-merge: %v", err)
	}
	if left.Snapshot().Total != 9 {
		t.Fatal("re-merge double counted")
	}
}

func TestMergeEqualsSingleHistogram(t *testing.T) {
	cfg := DefaultConfig()
	vals := []float64{1, 5, 17, 200, 999, 42, 3}
	h1, _ := NewWithSource(cfg, SourceID{ID: "x", Epoch: 1})
	h2, _ := NewWithSource(cfg, SourceID{ID: "y", Epoch: 1})
	for i, v := range vals {
		if i%2 == 0 {
			h1.Observe(v)
		} else {
			h2.Observe(v)
		}
	}
	merged, err := MergeSnapshots(cfg, h1.Snapshot(), h2.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := New(cfg)
	for _, v := range vals {
		ref.Observe(v)
	}
	rs := ref.Snapshot()
	if merged.Total != rs.Total {
		t.Fatalf("total %d vs %d", merged.Total, rs.Total)
	}
	for i := range rs.Counts {
		if merged.Counts[i] != rs.Counts[i] {
			t.Fatalf("bucket %d: %d vs %d", i, merged.Counts[i], rs.Counts[i])
		}
	}
	for _, q := range []float64{0.5, 0.9, 0.99} {
		gv, _ := merged.Quantile(q)
		rv, _ := rs.Quantile(q)
		if gv != rv {
			t.Fatalf("q=%v merged=%v ref=%v", q, gv, rv)
		}
	}
}

func TestMergeConfigMismatch(t *testing.T) {
	m := NewMerger(DefaultConfig())
	other := HistogramConfig{MinValue: 2, MaxValue: 1000, RelativeError: 0.01}
	h, _ := New(other)
	h.Observe(10)
	if err := m.Add(h.Snapshot()); err != ErrConfigMismatch {
		t.Fatalf("got %v", err)
	}
}

func TestMergerConcurrent(t *testing.T) {
	cfg := DefaultConfig()
	m := NewMerger(cfg)
	done := make(chan struct{})
	for w := 0; w < 4; w++ {
		go func() {
			for {
				select {
				case <-done:
					return
				default:
					m.Snapshot()
				}
			}
		}()
	}
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for seq := uint64(1); seq <= 50; seq++ {
				h, _ := NewWithSource(cfg, SourceID{ID: string(rune('a' + id)), Epoch: 1})
				for i := 0; i < int(seq)*3; i++ {
					h.Observe(float64(1 + i%500))
				}
				s := h.Snapshot()
				s.Seq = seq
				if err := m.Add(s); err != nil {
					t.Errorf("add: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(done)
	out := m.Snapshot()
	if out.Total != 150+150 {
		t.Fatalf("concurrent merged total %d", out.Total)
	}
}
