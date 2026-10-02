package latslo

import "fmt"

// Merge combines another histogram replica into h using the grow-only
// counter CRDT (pointwise maximum per source). Merge is associative,
// commutative and idempotent: merging the same source twice never
// double-counts. Both histograms must share the same configuration.
//
// The merged result is statistically equivalent to feeding every sample to a
// single instance within the histogram's relative error bound.
func (h *Histogram) Merge(other *Histogram) error {
	if h.config != other.config {
		return &mergeError{msg: fmt.Sprintf("config mismatch: %+v vs %+v", h.config, other.config)}
	}
	// Lock in a stable order to avoid deadlock.
	first, second := h, other
	if first.id > second.id {
		first, second = second, first
	}
	first.mu.Lock()
	second.mu.Lock()
	defer second.mu.Unlock()
	defer first.mu.Unlock()

	for id, sc := range other.sources {
		dst := h.sources[id]
		if dst == nil {
			dst = &sourceCount{buckets: make([]int64, len(sc.buckets))}
			h.sources[id] = dst
		}
		for i, c := range sc.buckets {
			if c > dst.buckets[i] {
				dst.buckets[i] = c
			}
		}
		if sc.underflow > dst.underflow {
			dst.underflow = sc.underflow
		}
		if sc.overflow > dst.overflow {
			dst.overflow = sc.overflow
		}
		if sc.total > dst.total {
			dst.total = sc.total
		}
		if sc.clock > dst.clock {
			dst.clock = sc.clock
		}
	}
	if other.clock > h.clock {
		h.clock = other.clock
	}
	return nil
}

// Add adds the counts of a disjoint histogram into h. Unlike Merge, Add
// performs component-wise addition and is intended for aggregating
// non-overlapping time buckets, not for reconciling replicas. Both
// histograms must share the same configuration.
func (h *Histogram) Add(other *Histogram) error {
	if h.config != other.config {
		return &mergeError{msg: fmt.Sprintf("config mismatch: %+v vs %+v", h.config, other.config)}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	other.mu.RLock()
	defer other.mu.RUnlock()
	for id, sc := range other.sources {
		dst := h.sources[id]
		if dst == nil {
			dst = &sourceCount{buckets: make([]int64, len(sc.buckets))}
			h.sources[id] = dst
		}
		for i, c := range sc.buckets {
			dst.buckets[i] += c
		}
		dst.underflow += sc.underflow
		dst.overflow += sc.overflow
		dst.total += sc.total
		dst.clock += sc.clock
	}
	h.clock += other.clock
	return nil
}

// mergeError indicates a configuration mismatch during merge/add.
type mergeError struct {
	msg string
}

func (e *mergeError) Error() string { return fmt.Sprintf("latslo: %s", e.msg) }
