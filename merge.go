package slogate

import "sync"

// Merger combines cumulative snapshots from independent sources.
//
// The operation models "all samples were fed to one histogram":
//   - snapshots from distinct sources (SourceID) are summed;
//   - for one source, only the newest cumulative snapshot counts —
//     re-adding an equal snapshot is a no-op and re-adding a newer one
//     replaces the previous state (old counts are removed, new added),
//     so repeated merges never double count;
//   - all inputs must share one HistogramConfig layout.
//
// Merge is associative on the resulting counts because addition of
// per-source last-known states is associative (identifying sources across
// two mergers by their SourceID); see MergeInto for the bulk variant.
type Merger struct {
	mu      sync.Mutex
	cfg     HistogramConfig
	sources map[sourceKey]*sourceState
}

type sourceKey struct {
	id    string
	epoch uint64
}

type sourceState struct {
	snap Snapshot
}

// NewMerger creates an empty merger bound to cfg.
func NewMerger(cfg HistogramConfig) *Merger {
	return &Merger{cfg: cfg, sources: make(map[sourceKey]*sourceState)}
}

func keyOf(s Snapshot) sourceKey {
	return sourceKey{id: s.Source.ID, epoch: s.Source.Epoch}
}

// Add incorporates one source snapshot. A snapshot whose Seq is older
// than (or different-but-equal to) one already seen is handled as:
// equal Seq must carry equal counts (idempotent retry); a strictly older
// Seq is rejected as a regression; a newer Seq replaces the old state.
func (m *Merger) Add(s Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !s.Config.equal(m.cfg) || len(s.Counts) != m.cfg.BucketCount() {
		return ErrConfigMismatch
	}
	if err := s.validate(); err != nil {
		return err
	}
	k := keyOf(s)
	if prev, ok := m.sources[k]; ok {
		switch {
		case s.Seq < prev.snap.Seq:
			return ErrSourceRegression
		case s.Seq == prev.snap.Seq:
			if !sameCounts(prev.snap, s) {
				return ErrSourceRegression
			}
			return nil // idempotent retry
		}
		if !dominates(s, prev.snap) {
			return ErrSourceRegression
		}
	}
	cp := s
	cp.Counts = append([]uint64(nil), s.Counts...)
	m.sources[k] = &sourceState{snap: cp}
	return nil
}

// Has reports whether a snapshot from source k at seq is already merged.
func (m *Merger) Has(k SourceID, seq uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.sources[sourceKey{id: k.ID, epoch: k.Epoch}]
	return ok && st.snap.Seq == seq
}

func sameCounts(a, b Snapshot) bool {
	if a.Total != b.Total || a.Underflow != b.Underflow || a.Overflow != b.Overflow ||
		a.Errors != b.Errors || a.Sum != b.Sum ||
		a.UnderflowMin != b.UnderflowMin || a.OverflowMax != b.OverflowMax ||
		len(a.Counts) != len(b.Counts) {
		return false
	}
	for i := range a.Counts {
		if a.Counts[i] != b.Counts[i] {
			return false
		}
	}
	return true
}

// dominates reports that cumulative snapshot newer contains at least as
// many samples in every region as older.
func dominates(newer, older Snapshot) bool {
	if newer.Underflow < older.Underflow || newer.Overflow < older.Overflow ||
		newer.Errors < older.Errors || newer.Total < older.Total ||
		len(newer.Counts) != len(older.Counts) {
		return false
	}
	for i := range newer.Counts {
		if newer.Counts[i] < older.Counts[i] {
			return false
		}
	}
	return true
}

// Sources returns the number of distinct sources currently merged.
func (m *Merger) Sources() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.sources) }

// Snapshot returns the merged view, equal within bucket error to feeding
// every source's samples to a single histogram.
func (m *Merger) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := Snapshot{
		Config: m.cfg,
		Counts: make([]uint64, m.cfg.BucketCount()),
		Source: SourceID{ID: "merged", Epoch: 0},
	}
	var underMin, overMax float64
	for _, st := range m.sources {
		s := st.snap
		for i, c := range s.Counts {
			out.Counts[i] += c
		}
		out.Underflow += s.Underflow
		out.Overflow += s.Overflow
		out.Errors += s.Errors
		out.Total += s.Total
		out.Sum += s.Sum
		if s.Underflow > 0 && (underMin == 0 || s.UnderflowMin < underMin) {
			underMin = s.UnderflowMin
		}
		if s.OverflowMax > overMax {
			overMax = s.OverflowMax
		}
		if s.CreatedAt.Before(out.CreatedAt) || out.CreatedAt.IsZero() {
			out.CreatedAt = s.CreatedAt
		}
	}
	out.UnderflowMin = underMin
	out.OverflowMax = overMax
	return out
}

// MergeSnapshots merges several compatible snapshots in one call.
func MergeSnapshots(cfg HistogramConfig, snaps ...Snapshot) (Snapshot, error) {
	m := NewMerger(cfg)
	for _, s := range snaps {
		if err := m.Add(s); err != nil {
			return Snapshot{}, err
		}
	}
	return m.Snapshot(), nil
}

// Merge unions the per-source state of other into m. This is the
// associative merge primitive: grouping sources across mergers in any
// order yields the same resulting counts, and the same source present in
// both sides keeps only its newest cumulative state (never summed twice).
func (m *Merger) Merge(other *Merger) error {
	other.mu.Lock()
	defer other.mu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cfg.equal(other.cfg) {
		return ErrConfigMismatch
	}
	for k, st := range other.sources {
		if prev, ok := m.sources[k]; ok {
			s := st.snap
			switch {
			case s.Seq < prev.snap.Seq:
				return ErrSourceRegression
			case s.Seq == prev.snap.Seq:
				if !sameCounts(prev.snap, s) {
					return ErrSourceRegression
				}
				continue
			default:
				if !dominates(s, prev.snap) {
					return ErrSourceRegression
				}
			}
		}
		cp := st.snap
		cp.Counts = append([]uint64(nil), st.snap.Counts...)
		m.sources[k] = &sourceState{snap: cp}
	}
	return nil
}
