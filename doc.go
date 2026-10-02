// Package latslo provides an embeddable latency histogram and SLO gating
// library for load tests and regression runs.
//
// # Overview
//
// The package maintains a fixed-memory latency histogram whose bucket count
// is independent of the number of samples recorded. Samples are written
// concurrently by many goroutines; snapshots copy a consistent point-in-time
// view without blocking writers for long. Multiple process instances can merge
// their histograms into one, and merged state is idempotent and associative
// (a grow-only counter CRDT per source instance), so re-merging the same
// source never double-counts.
//
// # Statistical semantics and error bounds
//
// The histogram uses a base-2 logarithmic bucket layout with M = 10^p
// linear sub-buckets per magnitude bucket, where p is the number of
// significant digits (Config.SignificantDigits, 1..3). A value falling in a
// sub-bucket of width w is reported using the sub-bucket's upper edge, so
// percentile estimates are conservative over-estimates with relative error
// at most 1/M = 10^-p (e.g. p=2 -> 1%). Values below MinTrackableValue are
// counted in the underflow counter; values above MaxTrackableValue are
// counted in the overflow counter. Both counters are exposed separately and
// never silently dropped.
//
// Percentile estimates are upper-bound estimates: the reported value is the
// largest latency consistent with the histogram. SLO checks that compare a
// percentile against a threshold are therefore conservative. The fraction of
// samples exceeding a threshold is likewise computed conservatively (a bucket
// is counted as exceeding whenever its upper edge is above the threshold).
//
// # Concurrency
//
// All methods on Histogram and Window are safe for concurrent use. Record
// takes an exclusive lock briefly; Snapshot takes a read lock and copies the
// bucket array, yielding a consistent point-in-time view. Snapshots are
// independent and do not accumulate state, so taking several snapshots never
// double-counts or loses samples.
//
// # Merging
//
// Each histogram instance carries a stable source id and a monotonic clock
// that advances per recorded sample. Merge unions the per-source grow-only
// counters by taking the pointwise maximum (G-Counter CRDT). Merge is
// associative, commutative and idempotent: merging the same source twice (or
// merging in any order) yields the same result as merging it once. The merged
// result is statistically equivalent to feeding every sample to a single
// instance within the histogram's error bound.
//
// # Time windows and SLOs
//
// Window keeps a ring of per-bucket histograms covering a rolling time
// range. Evaluate sums the buckets whose timestamps fall within the window.
// Each SLO target pairs a percentile threshold with an error budget (the
// allowed fraction of samples over the threshold). A target passes only when
// the observed percentile is at or below the threshold AND the observed
// over-threshold fraction is within the error budget. Failures carry a
// machine-readable reason.
//
// # Regression comparison
//
// RegressionGate compares a current snapshot against a historical baseline at
// a configured percentile using a seeded Poisson bootstrap of the bucket
// counts. It returns Degraded, Improved, NoChange or Inconclusive. When
// either side has fewer than MinSamples, or when the bootstrap confidence
// interval crosses the equivalence margin, the conclusion is Inconclusive:
// the gate refuses to emit a deterministic verdict under insufficient data or
// high variance. The RNG is seeded from a configurable seed, so identical
// input and seed produce identical output.
//
// # Persistence
//
// Checkpoints are written atomically (temp file + rename) with a magic
// header and a CRC-32 checksum over the payload. On load, magic, checksum and
// internal consistency (bucket lengths, non-negativity, total recomputation,
// config validity) are verified. Any corruption causes Load to return an
// explicit error and refuse to continue accumulating; it never silently
// yields wrong conclusions.
package latslo
