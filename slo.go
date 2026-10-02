package latslo

import (
	"time"
)

// Reason is a machine-readable SLO verdict reason.
type Reason int

const (
	// ReasonOK indicates the SLO target is met.
	ReasonOK Reason = iota
	// ReasonPercentileExceeded indicates the observed percentile is above
	// the configured threshold.
	ReasonPercentileExceeded
	// ReasonErrorBudgetExhausted indicates the over-threshold fraction
	// exceeds the configured error budget.
	ReasonErrorBudgetExhausted
)

// String returns the human-readable name of the reason.
func (r Reason) String() string {
	switch r {
	case ReasonOK:
		return "OK"
	case ReasonPercentileExceeded:
		return "PERCENTILE_EXCEEDED"
	case ReasonErrorBudgetExhausted:
		return "ERROR_BUDGET_EXHAUSTED"
	default:
		return "UNKNOWN"
	}
}

// SLO defines a single service-level objective: a percentile threshold paired
// with an error budget.
type SLO struct {
	// Name is a human-readable target name.
	Name string
	// Percentile is the target percentile in (0, 1), e.g. 0.99.
	Percentile float64
	// Threshold is the latency the percentile must stay at or below.
	Threshold time.Duration
	// ErrorBudget is the maximum allowed fraction of samples over
	// Threshold, in [0, 1).
	ErrorBudget float64
}

// SLOResult is the verdict for a single SLO target.
type SLOResult struct {
	Name               string
	Pass               bool
	Reason             Reason
	ObservedPercentile time.Duration
	Threshold          time.Duration
	ErrorBudgetUsed    float64
	ErrorBudget        float64
	Samples            int64
}

// Evaluate checks the SLO target against the given snapshot. A target passes
// only when the observed percentile is at or below the threshold AND the
// observed over-threshold fraction is within the error budget. The percentile
// check takes precedence when both fail.
func (s SLO) Evaluate(snap Snapshot) SLOResult {
	res := SLOResult{
		Name:        s.Name,
		Threshold:   s.Threshold,
		ErrorBudget: s.ErrorBudget,
		Samples:     snap.Total(),
	}
	if res.Samples == 0 {
		res.Pass = true
		res.Reason = ReasonOK
		return res
	}
	res.ObservedPercentile = snap.ValueAtPercentile(s.Percentile)
	over := snap.CountAbove(s.Threshold)
	res.ErrorBudgetUsed = float64(over) / float64(res.Samples)

	if res.ObservedPercentile > s.Threshold {
		res.Pass = false
		res.Reason = ReasonPercentileExceeded
		return res
	}
	if res.ErrorBudgetUsed > s.ErrorBudget {
		res.Pass = false
		res.Reason = ReasonErrorBudgetExhausted
		return res
	}
	res.Pass = true
	res.Reason = ReasonOK
	return res
}

// EvaluateAll checks multiple SLO targets against the snapshot.
func EvaluateAll(snap Snapshot, slos []SLO) []SLOResult {
	results := make([]SLOResult, len(slos))
	for i, s := range slos {
		results[i] = s.Evaluate(snap)
	}
	return results
}
