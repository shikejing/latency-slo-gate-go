package latslo

import (
	"testing"
	"time"
)

func TestWindowRolling(t *testing.T) {
	cfg := DefaultConfig()
	w, _ := NewWindow(cfg, 1, 10*time.Second, time.Second)
	now := time.Unix(1000, 0)
	for b := 0; b < 5; b++ {
		for i := 0; i < 100; i++ {
			w.Record(now.Add(time.Duration(b)*time.Second), 100*time.Millisecond)
		}
	}
	// Window ending at now+5s covers all 5 buckets.
	snap := w.Snapshot(now.Add(5 * time.Second))
	if snap.Total() != 500 {
		t.Errorf("total=%d, want 500", snap.Total())
	}
	// Advance to now+13s: window covers [now+3s, now+13s), so only buckets
	// starting at now+3s and now+4s remain = 200 samples.
	w.Advance(now.Add(13 * time.Second))
	snap = w.Snapshot(now.Add(13 * time.Second))
	if snap.Total() != 200 {
		t.Errorf("after advance total=%d, want 200", snap.Total())
	}
}

func TestWindowEmpty(t *testing.T) {
	cfg := DefaultConfig()
	w, _ := NewWindow(cfg, 1, 10*time.Second, time.Second)
	snap := w.Snapshot(time.Unix(1000, 0))
	if snap.Total() != 0 {
		t.Errorf("empty window total=%d, want 0", snap.Total())
	}
	if snap.ValueAtPercentile(0.99) != 0 {
		t.Errorf("empty window p99=%s, want 0", snap.ValueAtPercentile(0.99))
	}
}

func TestWindowBucketSizeValidation(t *testing.T) {
	cfg := DefaultConfig()
	if _, err := NewWindow(cfg, 1, 10*time.Second, 3*time.Second); err == nil {
		t.Error("expected error for non-dividing bucket size")
	}
	if _, err := NewWindow(cfg, 1, 0, time.Second); err == nil {
		t.Error("expected error for zero window size")
	}
}

func TestWindowRollover(t *testing.T) {
	cfg := DefaultConfig()
	w, _ := NewWindow(cfg, 1, 4*time.Second, time.Second)
	now := time.Unix(2000, 0)
	for i := 0; i < 100; i++ {
		w.Record(now, 50*time.Millisecond)
	}
	for i := 0; i < 200; i++ {
		w.Record(now.Add(4*time.Second), 50*time.Millisecond)
	}
	snap := w.Snapshot(now.Add(4 * time.Second))
	// Bucket 0 was reset on rollover; only the 200 new samples remain.
	if snap.Total() != 200 {
		t.Errorf("after rollover total=%d, want 200", snap.Total())
	}
}
