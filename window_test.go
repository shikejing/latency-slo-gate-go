package slogate

import (
	"testing"
	"time"
)

func newTestWindow(t *testing.T) (*Window, *time.Time) {
	t.Helper()
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := WindowConfig{SliceWidth: time.Minute, SliceCount: 5, Clock: func() time.Time { return base }}
	w, err := NewWindow(DefaultConfig(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return w, &base
}

func TestWindowRolling(t *testing.T) {
	w, base := newTestWindow(t)
	// 10 samples in minute 0.
	for i := 0; i < 10; i++ {
		w.Observe(*base, float64(10+i), false)
	}
	if s := w.Snapshot(); s.Total != 10 {
		t.Fatalf("initial total %d", s.Total)
	}
	// advance 2 minutes, add 5 more.
	*base = base.Add(2 * time.Minute)
	for i := 0; i < 5; i++ {
		w.Observe(*base, 100, true)
	}
	if s := w.Snapshot(); s.Total != 15 || s.Errors != 5 {
		t.Fatalf("after advance total=%d err=%d", s.Total, s.Errors)
	}
	// At minute 4 the window still covers 0..4: nothing expired yet.
	*base = base.Add(2 * time.Minute) // minute 4
	if s := w.Snapshot(); s.Total != 15 || s.Errors != 5 {
		t.Fatalf("at minute 4 total=%d err=%d", s.Total, s.Errors)
	}
	// At minute 5 the window lazily rolls to 1..5: minute 0 expires.
	*base = base.Add(1 * time.Minute) // minute 5
	if s := w.Snapshot(); s.Total != 5 {
		t.Fatalf("after expiry total=%d", s.Total)
	}
	// minute 2 (the 5 error samples) expires at minute 7.
	*base = base.Add(2 * time.Minute) // minute 7
	if s := w.Snapshot(); s.Total != 0 {
		t.Fatalf("after full expiry total=%d", s.Total)
	}
	// jump far forward clears everything.
	*base = base.Add(60 * time.Minute)
	w.Observe(*base, 1, false)
	if s := w.Snapshot(); s.Total != 1 {
		t.Fatalf("after big jump total=%d", s.Total)
	}
}

func TestWindowOldSampleDropped(t *testing.T) {
	w, base := newTestWindow(t)
	w.Observe(*base, 5, false)
	old := base.Add(-time.Minute)
	w.Observe(old, 6, false)
	if s := w.Snapshot(); s.Total != 1 {
		t.Fatalf("old sample accepted: %d", s.Total)
	}
}

func TestWindowFixedMemory(t *testing.T) {
	w, base := newTestWindow(t)
	for i := 0; i < 100000; i++ {
		ts := base.Add(time.Duration(i%7) * time.Minute)
		w.Observe(ts, float64(1+i%500), i%100 == 0)
	}
	s := w.Snapshot()
	var in uint64
	for _, c := range s.Counts {
		in += c
	}
	if in+s.Underflow+s.Overflow != s.Total {
		t.Fatal("inconsistent window snapshot")
	}
}
