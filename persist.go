package latslo

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"time"
)

// magicHeader identifies a latslo checkpoint file.
const magicHeader = "LATSLO01"

// ErrCorrupt is returned when a checkpoint fails verification. Load refuses
// to continue after a corruption error; it never silently yields wrong
// conclusions.
var ErrCorrupt = &corruptError{}

type corruptError struct{}

func (e *corruptError) Error() string { return "latslo: checkpoint is corrupt" }

// Checkpoint is the persistent state of a histogram and its window.
type Checkpoint struct {
	Histogram     *Histogram
	WindowBuckets []PersistedBucket
	WindowConfig  WindowConfig
}

// WindowConfig is the serializable window configuration.
type WindowConfig struct {
	WindowSize time.Duration
	BucketSize time.Duration
}

// PersistedBucket is a serializable window bucket.
type PersistedBucket struct {
	Start int64
	Hist  *Histogram
}

// Store persists and recovers checkpoints.
type Store struct {
	path string
}

// NewStore creates a store backed by the given file path.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// persistedHistogram is the gob-serializable form of a Histogram.
type persistedHistogram struct {
	Config  Config
	ID      uint64
	Clock   int64
	Sources map[uint64]persistedSource
}

// persistedSource is the gob-serializable form of a sourceCount.
type persistedSource struct {
	Buckets   []int64
	Underflow int64
	Overflow  int64
	Total     int64
	Clock     int64
}

// checkpointData is the gob-serializable checkpoint payload.
type checkpointData struct {
	Histogram     persistedHistogram
	WindowBuckets []persistedWindowBucket
	WindowConfig  WindowConfig
}

// persistedWindowBucket is the gob-serializable form of a window bucket.
type persistedWindowBucket struct {
	Start int64
	Hist  persistedHistogram
}

// Save writes the checkpoint atomically (temp file + rename). The file
// contains a magic header, a CRC-32 checksum of the gob payload, and the
// payload itself.
func (s *Store) Save(cp *Checkpoint) error {
	if cp == nil || cp.Histogram == nil {
		return fmt.Errorf("latslo: cannot save nil checkpoint")
	}
	data := checkpointData{
		Histogram:    histogramToPersisted(cp.Histogram),
		WindowConfig: cp.WindowConfig,
	}
	for _, b := range cp.WindowBuckets {
		data.WindowBuckets = append(data.WindowBuckets, persistedWindowBucket{
			Start: b.Start,
			Hist:  histogramToPersisted(b.Hist),
		})
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(data); err != nil {
		return fmt.Errorf("latslo: encode checkpoint: %w", err)
	}
	payload := buf.Bytes()
	checksum := crc32.ChecksumIEEE(payload)

	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("latslo: create temp file: %w", err)
	}
	if _, err := f.WriteString(magicHeader); err != nil {
		f.Close()
		return fmt.Errorf("latslo: write magic: %w", err)
	}
	if err := binary.Write(f, binary.LittleEndian, uint64(len(payload))); err != nil {
		f.Close()
		return fmt.Errorf("latslo: write length: %w", err)
	}
	if _, err := f.Write(payload); err != nil {
		f.Close()
		return fmt.Errorf("latslo: write payload: %w", err)
	}
	if err := binary.Write(f, binary.LittleEndian, checksum); err != nil {
		f.Close()
		return fmt.Errorf("latslo: write checksum: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("latslo: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("latslo: close: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("latslo: rename: %w", err)
	}
	return nil
}

// Load reads and verifies a checkpoint. It returns ErrCorrupt (wrapped) if
// the magic, checksum, or internal consistency checks fail.
func (s *Store) Load() (*Checkpoint, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("latslo: open: %w", err)
	}
	defer f.Close()

	var magicBuf [len(magicHeader)]byte
	if _, err := io.ReadFull(f, magicBuf[:]); err != nil {
		return nil, fmt.Errorf("%w: bad magic", ErrCorrupt)
	}
	if string(magicBuf[:]) != magicHeader {
		return nil, fmt.Errorf("%w: bad magic", ErrCorrupt)
	}
	var payloadLen uint64
	if err := binary.Read(f, binary.LittleEndian, &payloadLen); err != nil {
		return nil, fmt.Errorf("%w: bad length", ErrCorrupt)
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(f, payload); err != nil {
		return nil, fmt.Errorf("%w: truncated payload", ErrCorrupt)
	}
	var storedChecksum uint32
	if err := binary.Read(f, binary.LittleEndian, &storedChecksum); err != nil {
		return nil, fmt.Errorf("%w: missing checksum", ErrCorrupt)
	}
	if crc32.ChecksumIEEE(payload) != storedChecksum {
		return nil, fmt.Errorf("%w: checksum mismatch", ErrCorrupt)
	}

	var data checkpointData
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(&data); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if err := validateCheckpointData(&data); err != nil {
		return nil, err
	}

	cp := &Checkpoint{
		Histogram:    persistedToHistogram(data.Histogram),
		WindowConfig: data.WindowConfig,
	}
	for _, b := range data.WindowBuckets {
		cp.WindowBuckets = append(cp.WindowBuckets, PersistedBucket{
			Start: b.Start,
			Hist:  persistedToHistogram(b.Hist),
		})
	}
	return cp, nil
}

// histogramToPersisted converts a Histogram to its serializable form,
// locking the histogram for a consistent view.
func histogramToPersisted(h *Histogram) persistedHistogram {
	if h == nil {
		return persistedHistogram{}
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	ph := persistedHistogram{
		Config:  h.config,
		ID:      h.id,
		Clock:   h.clock,
		Sources: make(map[uint64]persistedSource, len(h.sources)),
	}
	for id, sc := range h.sources {
		ps := persistedSource{
			Buckets:   make([]int64, len(sc.buckets)),
			Underflow: sc.underflow,
			Overflow:  sc.overflow,
			Total:     sc.total,
			Clock:     sc.clock,
		}
		copy(ps.Buckets, sc.buckets)
		ph.Sources[id] = ps
	}
	return ph
}

// persistedToHistogram converts a persisted histogram back to a Histogram.
func persistedToHistogram(ph persistedHistogram) *Histogram {
	h := &Histogram{
		config:  ph.Config,
		id:      ph.ID,
		clock:   ph.Clock,
		sources: make(map[uint64]*sourceCount, len(ph.Sources)),
	}
	for id, ps := range ph.Sources {
		sc := &sourceCount{
			buckets:   make([]int64, len(ps.Buckets)),
			underflow: ps.Underflow,
			overflow:  ps.Overflow,
			total:     ps.Total,
			clock:     ps.Clock,
		}
		copy(sc.buckets, ps.Buckets)
		h.sources[id] = sc
	}
	return h
}

// validateCheckpointData verifies the internal consistency of a decoded
// checkpoint. Any failure returns ErrCorrupt.
func validateCheckpointData(data *checkpointData) error {
	if err := validatePersistedHistogram(&data.Histogram); err != nil {
		return err
	}
	for i := range data.WindowBuckets {
		if err := validatePersistedHistogram(&data.WindowBuckets[i].Hist); err != nil {
			return err
		}
	}
	return nil
}

// validatePersistedHistogram verifies a single persisted histogram.
func validatePersistedHistogram(ph *persistedHistogram) error {
	if err := ph.Config.validate(); err != nil {
		return fmt.Errorf("%w: bad config: %v", ErrCorrupt, err)
	}
	expectedBuckets := ph.Config.bucketCount()
	for id, ps := range ph.Sources {
		if len(ps.Buckets) != expectedBuckets {
			return fmt.Errorf("%w: source %d has %d buckets, want %d",
				ErrCorrupt, id, len(ps.Buckets), expectedBuckets)
		}
		var sum int64
		for i, c := range ps.Buckets {
			if c < 0 {
				return fmt.Errorf("%w: source %d bucket %d is negative", ErrCorrupt, id, i)
			}
			sum += c
		}
		if ps.Underflow < 0 {
			return fmt.Errorf("%w: source %d underflow is negative", ErrCorrupt, id)
		}
		if ps.Overflow < 0 {
			return fmt.Errorf("%w: source %d overflow is negative", ErrCorrupt, id)
		}
		if ps.Total != sum+ps.Underflow+ps.Overflow {
			return fmt.Errorf("%w: source %d total %d != sum %d + underflow %d + overflow %d",
				ErrCorrupt, id, ps.Total, sum, ps.Underflow, ps.Overflow)
		}
		if ps.Clock < 0 {
			return fmt.Errorf("%w: source %d clock is negative", ErrCorrupt, id)
		}
	}
	return nil
}
