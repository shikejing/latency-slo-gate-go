package latslo

import (
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.bin")
	cfg := DefaultConfig()
	rng := rand.New(rand.NewSource(42))

	// First "process lifetime": record samples.
	h1, _ := NewHistogram(cfg, 1)
	for i := 0; i < 5000; i++ {
		h1.Record(time.Duration(rng.Int63n(int64(time.Hour))))
	}
	wantSnap := h1.Snapshot()
	store := NewStore(path)
	if err := store.Save(&Checkpoint{Histogram: h1}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// "Restart": load and verify state.
	cp, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	gotSnap := cp.Histogram.Snapshot()
	if gotSnap.Total() != wantSnap.Total() {
		t.Fatalf("after restart total=%d, want %d", gotSnap.Total(), wantSnap.Total())
	}
	for i := range wantSnap.buckets {
		if gotSnap.buckets[i] != wantSnap.buckets[i] {
			t.Fatalf("after restart bucket %d: %d vs %d", i, gotSnap.buckets[i], wantSnap.buckets[i])
		}
	}
	for _, p := range []float64{0.5, 0.9, 0.99, 0.999} {
		if gotSnap.ValueAtPercentile(p) != wantSnap.ValueAtPercentile(p) {
			t.Errorf("after restart p%.3f: %s vs %s", p, gotSnap.ValueAtPercentile(p), wantSnap.ValueAtPercentile(p))
		}
	}

	// Continue recording after restart: no double-count, no loss.
	for i := 0; i < 3000; i++ {
		cp.Histogram.Record(time.Duration(rng.Int63n(int64(time.Hour))))
	}
	if err := store.Save(&Checkpoint{Histogram: cp.Histogram}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	cp2, err := store.Load()
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if cp2.Histogram.Snapshot().Total() != 8000 {
		t.Errorf("after second restart total=%d, want 8000", cp2.Histogram.Snapshot().Total())
	}
}

func TestPersistCorruptedData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.bin")
	cfg := DefaultConfig()
	h, _ := NewHistogram(cfg, 1)
	for i := 0; i < 1000; i++ {
		h.Record(time.Duration(i) * time.Microsecond)
	}
	store := NewStore(path)
	if err := store.Save(&Checkpoint{Histogram: h}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Corrupt a byte in the payload region (after magic+length, before checksum).
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 30 {
		t.Fatalf("checkpoint too short: %d bytes", len(data))
	}
	data[20] ^= 0xFF
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	_, err = store.Load()
	if err == nil {
		t.Fatal("expected corruption error, got nil")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("error=%v, want ErrCorrupt", err)
	}
}

func TestPersistCorruptedMagic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.bin")
	cfg := DefaultConfig()
	h, _ := NewHistogram(cfg, 1)
	h.Record(10 * time.Millisecond)
	store := NewStore(path)
	if err := store.Save(&Checkpoint{Histogram: h}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	data[0] = 'X'
	os.WriteFile(path, data, 0644)
	_, err := store.Load()
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("error=%v, want ErrCorrupt", err)
	}
}

func TestPersistWithWindowBuckets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.bin")
	cfg := DefaultConfig()
	h, _ := NewHistogram(cfg, 1)
	h.Record(10 * time.Millisecond)
	now := time.Unix(5000, 0)
	cp := &Checkpoint{
		Histogram: h,
		WindowConfig: WindowConfig{
			WindowSize: 10 * time.Second,
			BucketSize: time.Second,
		},
		WindowBuckets: []PersistedBucket{
			{Start: now.UnixNano(), Hist: h},
		},
	}
	store := NewStore(path)
	if err := store.Save(cp); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.WindowBuckets) != 1 {
		t.Fatalf("window buckets=%d, want 1", len(loaded.WindowBuckets))
	}
	if loaded.WindowBuckets[0].Start != now.UnixNano() {
		t.Errorf("bucket start=%d, want %d", loaded.WindowBuckets[0].Start, now.UnixNano())
	}
	if loaded.WindowConfig.WindowSize != 10*time.Second {
		t.Errorf("window size=%s", loaded.WindowConfig.WindowSize)
	}
}

func TestPersistMissingFile(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "nonexistent.bin"))
	_, err := store.Load()
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
