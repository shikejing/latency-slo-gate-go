package slogate

import (
	"bytes"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

func populatedSnap() Snapshot {
	h, _ := NewWithSource(DefaultConfig(), SourceID{ID: "node-1", Epoch: 77})
	for i := 0; i < 500; i++ {
		h.Observe(float64(1 + i%900))
	}
	h.ObserveOutcome(0.2, false)
	h.ObserveOutcome(500000, true)
	return h.Snapshot()
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	s := populatedSnap()
	var buf bytes.Buffer
	if err := Encode(&buf, s); err != nil {
		t.Fatal(err)
	}
	got, err := Decode(&buf, s.Config)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != s.Total || got.Seq != s.Seq || got.Source != s.Source {
		t.Fatalf("meta mismatch %+v vs %+v", got, s)
	}
	if got.Sum != s.Sum || got.Underflow != s.Underflow || got.Overflow != s.Overflow ||
		got.Errors != s.Errors || got.UnderflowMin != s.UnderflowMin || got.OverflowMax != s.OverflowMax {
		t.Fatal("aggregate mismatch")
	}
	if len(got.Counts) != len(s.Counts) {
		t.Fatal("bucket count mismatch")
	}
	for i := range s.Counts {
		if got.Counts[i] != s.Counts[i] {
			t.Fatalf("bucket %d mismatch", i)
		}
	}
	gq, _ := got.Quantile(0.99)
	sq, _ := s.Quantile(0.99)
	if gq != sq {
		t.Fatal("quantile changed across roundtrip")
	}
}

func TestRestartContinuesAccumulation(t *testing.T) {
	cfg := DefaultConfig()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.bin")

	// First process lifetime.
	h1, _ := NewWithSource(cfg, SourceID{ID: "svc", Epoch: 1})
	for i := 0; i < 1000; i++ {
		h1.Observe(float64(10 + i%400))
	}
	snap1 := h1.Snapshot()
	if err := Save(path, snap1); err != nil {
		t.Fatal(err)
	}

	// Restart: load, restore, keep accumulating under SAME source/seq.
	loaded, err := Load(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := Restore(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if h2.Source() != snap1.Source {
		t.Fatal("source identity lost across restart")
	}
	for i := 0; i < 500; i++ {
		h2.Observe(float64(20 + i%400))
	}
	snap2 := h2.Snapshot()
	if snap2.Total != 1500 {
		t.Fatalf("expected continuity total=1500 got %d", snap2.Total)
	}
	// Independent reference fed all 1500 samples must agree bucket-wise.
	ref, _ := New(cfg)
	for i := 0; i < 1000; i++ {
		ref.Observe(float64(10 + i%400))
	}
	for i := 0; i < 500; i++ {
		ref.Observe(float64(20 + i%400))
	}
	rs := ref.Snapshot()
	for i := range rs.Counts {
		if snap2.Counts[i] != rs.Counts[i] {
			t.Fatalf("restart mismatch bucket %d: %d vs %d", i, snap2.Counts[i], rs.Counts[i])
		}
	}
}

func TestDecodeRejectsCorruption(t *testing.T) {
	s := populatedSnap()
	var good bytes.Buffer
	if err := Encode(&good, s); err != nil {
		t.Fatal(err)
	}
	raw := good.Bytes()

	// bit flip in a bucket
	flipped := append([]byte(nil), raw...)
	flipped[len(flipped)-10] ^= 0xFF
	if _, err := Decode(bytes.NewReader(flipped), s.Config); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("bit flip: %v", err)
	}
	// truncation
	if _, err := Decode(bytes.NewReader(raw[:len(raw)-7]), s.Config); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("truncation: %v", err)
	}
	// garbage
	if _, err := Decode(bytes.NewReader([]byte("not a state file at all!!!")), s.Config); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("garbage: %v", err)
	}
	// empty
	if _, err := Decode(bytes.NewReader(nil), s.Config); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("empty: %v", err)
	}
	// bad magic with otherwise valid structure
	badMagic := append([]byte(nil), raw...)
	copy(badMagic[:4], "XXXX")
	fixCRC(badMagic)
	if _, err := Decode(bytes.NewReader(badMagic), s.Config); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("bad magic: %v", err)
	}
	// unsupported version
	badVer := append([]byte(nil), raw...)
	badVer[4] = 99
	fixCRC(badVer)
	if _, err := Decode(bytes.NewReader(badVer), s.Config); !errors.Is(err, ErrUnsupportedVer) {
		t.Fatalf("bad version: %v", err)
	}
	// config mismatch
	other := HistogramConfig{MinValue: 2, MaxValue: 2000000, RelativeError: 0.02}
	if _, err := Decode(bytes.NewReader(raw), other); !errors.Is(err, ErrConfigMismatch) {
		t.Fatalf("config mismatch: %v", err)
	}
}

func fixCRC(b []byte) {
	// recompute CRC over body after intentionally changing earlier bytes,
	// so the ONLY thing that can reject is the targeted structural check.
	body := b[:len(b)-4]
	sum := crc32IEEE(body)
	b[len(b)-4] = byte(sum)
	b[len(b)-3] = byte(sum >> 8)
	b[len(b)-2] = byte(sum >> 16)
	b[len(b)-1] = byte(sum >> 24)
}

func TestSaveLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent"), DefaultConfig())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want not-exist got %v", err)
	}
}

func crc32IEEE(p []byte) uint32 {
	return crc32.ChecksumIEEE(p)
}
