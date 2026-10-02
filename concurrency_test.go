package latslo

import (
	"math/rand"
	"sync"
	"testing"
	"time"
)

func TestConcurrentRecordAndSnapshot(t *testing.T) {
	cfg := DefaultConfig()
	h, _ := NewHistogram(cfg, 1)
	const goroutines = 16
	const perGoroutine = 20000
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < perGoroutine; i++ {
				h.Record(time.Duration(rng.Int63n(int64(time.Hour))))
			}
		}(int64(g))
	}
	snapDone := make(chan struct{})
	go func() {
		defer close(snapDone)
		for i := 0; i < 200; i++ {
			snap := h.Snapshot()
			var sum int64
			for _, c := range snap.buckets {
				sum += c
			}
			if sum != snap.InRange() {
				t.Errorf("snapshot inconsistent: bucket sum %d != inRange %d", sum, snap.InRange())
				return
			}
			if snap.InRange()+snap.Underflow()+snap.Overflow() != snap.Total() {
				t.Errorf("snapshot inconsistent: inRange+under+over != total")
				return
			}
			time.Sleep(time.Microsecond)
		}
	}()
	wg.Wait()
	<-snapDone
	snap := h.Snapshot()
	want := int64(goroutines * perGoroutine)
	if snap.Total() != want {
		t.Errorf("total=%d, want %d (samples lost or duplicated)", snap.Total(), want)
	}
}

func TestConcurrentWindowRecordAndSnapshot(t *testing.T) {
	cfg := DefaultConfig()
	const windowSize = 10 * time.Second
	const bucketSize = time.Second
	w, _ := NewWindow(cfg, 1, windowSize, bucketSize)
	const goroutines = 8
	const perGoroutine = 5000
	var wg sync.WaitGroup
	now := time.Now()
	// Record samples strictly inside the window, leaving a one-bucket
	// margin at the leading edge to avoid bucket-alignment boundary loss.
	span := int64(windowSize - bucketSize)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < perGoroutine; i++ {
				offset := time.Duration(rng.Int63n(span) + int64(bucketSize))
				w.Record(now.Add(offset), time.Duration(rng.Int63n(int64(time.Hour))))
			}
		}(int64(g))
	}
	wg.Wait()
	snap := w.Snapshot(now.Add(windowSize))
	if snap.Total() != int64(goroutines*perGoroutine) {
		t.Errorf("window total=%d, want %d", snap.Total(), goroutines*perGoroutine)
	}
}

func TestConcurrentMerge(t *testing.T) {
	cfg := DefaultConfig()
	target, _ := NewHistogram(cfg, 1)
	const n = 10
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id uint64) {
			defer wg.Done()
			h, _ := NewHistogram(cfg, id)
			rng := rand.New(rand.NewSource(int64(id)))
			for j := 0; j < 10000; j++ {
				h.Record(time.Duration(rng.Int63n(int64(time.Hour))))
			}
			for m := 0; m < 5; m++ {
				if err := target.Merge(h); err != nil {
					t.Errorf("merge error: %v", err)
					return
				}
			}
		}(uint64(i + 2))
	}
	wg.Wait()
	snap := target.Snapshot()
	want := int64(n * 10000)
	if snap.Total() != want {
		t.Errorf("merged total=%d, want %d (double-count or loss)", snap.Total(), want)
	}
}
