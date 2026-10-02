// Package slogate provides an embeddable, fixed-memory latency histogram
// with rolling-window SLO evaluation, baseline regression detection and
// durable state for continuous load and regression testing.
//
// The package is built around a small set of types:
//
//   - HistogramConfig / Histogram / Snapshot: fixed-size log-scaled
//     latency distribution with concurrency-safe recording and consistent
//     point-in-time snapshots;
//   - Merger: associative, idempotent combination of per-source snapshots;
//   - WindowConfig / Window: a rolling time window over fixed slice
//     histograms;
//   - Objective / Evaluate: multi-target SLO decisions with percentile
//     thresholds, error budgets and distinguishable reasons;
//   - Detector: seeded, reproducible baseline regression testing that
//     separates real shifts from random noise;
//   - Encode/Decode/Save/Load/Restore: checksummed, atomic persistence.
//
// See the repository README for statistical semantics, error bounds and
// the full configuration reference.
package slogate
