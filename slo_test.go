package slogate

import (
	"testing"
	"time"
)

func snapWith(vals []float64, errs int) Snapshot {
	h, _ := New(HistogramConfig{MinValue: 1, MaxValue: 100000, RelativeError: 0.01})
	for i, v := range vals {
		h.ObserveOutcome(v, i < errs)
	}
	return h.Snapshot()
}

func TestEvaluatePassWarnFail(t *testing.T) {
	vals := make([]float64, 1000)
	for i := range vals {
		vals[i] = 100 // all ~100
	}
	// p99 of 1000 equal samples = 100
	s := snapWith(vals, 0)
	obj := Objective{Name: "p99", Percentile: 99, LatencyLimit: 200, ErrorBudget: 0.01, MinSamples: 100, WarnFraction: 0.9}
	rep := Evaluate(s, time.Time{}, []Objective{obj})
	if rep.Status != StatusPass || rep.Results[0].Reasons[0] != ReasonNone {
		t.Fatalf("want pass, got %v %v", rep.Status, rep.Results[0].Reasons)
	}

	// near limit: values 185 at tail -> p99 region around 185 >= 180
	vals2 := make([]float64, 1000)
	for i := range vals2 {
		if i >= 989 {
			vals2[i] = 185
		} else {
			vals2[i] = 100
		}
	}
	s2 := snapWith(vals2, 0)
	rep2 := Evaluate(s2, time.Time{}, []Objective{obj})
	if rep2.Status != StatusWarn {
		t.Fatalf("want warn got %v (%v)", rep2.Status, rep2.Results[0].Percentile)
	}

	// breach
	vals3 := make([]float64, 1000)
	for i := range vals3 {
		vals3[i] = 300
	}
	s3 := snapWith(vals3, 0)
	rep3 := Evaluate(s3, time.Time{}, []Objective{obj})
	if rep3.Status != StatusFail {
		t.Fatalf("want fail got %v", rep3.Status)
	}
	if !hasReason(rep3.Results[0].Reasons, ReasonPercentileBreach) {
		t.Fatal("missing breach reason")
	}
}

func TestEvaluateErrorBudget(t *testing.T) {
	vals := make([]float64, 1000)
	for i := range vals {
		vals[i] = 10
	}
	obj := Objective{Name: "avail", Percentile: 99, LatencyLimit: 1000, ErrorBudget: 0.005, MinSamples: 100, WarnFraction: 0.8}

	// 4/1000 errors = 0.004 -> 80% budget -> warn burning
	s1 := snapWith(vals, 4)
	r1 := Evaluate(s1, time.Time{}, []Objective{obj})
	if r1.Status != StatusWarn || !hasReason(r1.Results[0].Reasons, ReasonErrorBudgetBurning) {
		t.Fatalf("want burning warn got %v %v", r1.Status, r1.Results[0].Reasons)
	}
	// 10/1000 = 1% > 0.5% -> fail exhausted
	s2 := snapWith(vals, 10)
	r2 := Evaluate(s2, time.Time{}, []Objective{obj})
	if r2.Status != StatusFail || !hasReason(r2.Results[0].Reasons, ReasonErrorBudgetExhausted) {
		t.Fatalf("want exhausted fail got %v %v", r2.Status, r2.Results[0].Reasons)
	}
}

func TestEvaluateInsufficient(t *testing.T) {
	s := snapWith([]float64{1, 2, 3}, 0)
	obj := Objective{Name: "x", Percentile: 99, LatencyLimit: 100, ErrorBudget: 0.01, MinSamples: 100}
	r := Evaluate(s, time.Time{}, []Objective{obj})
	if r.Status != StatusUnknown || r.Results[0].Reasons[0] != ReasonInsufficientData {
		t.Fatalf("got %v %v", r.Status, r.Results[0].Reasons)
	}
}

func TestEvaluateMultiObjective(t *testing.T) {
	s := snapWith([]float64{5, 6, 7}, 0)
	good := Objective{Name: "g", Percentile: 99, LatencyLimit: 100, ErrorBudget: 0.01, MinSamples: 1}
	bad := Objective{Name: "b", Percentile: 99, LatencyLimit: 1, ErrorBudget: 0.01, MinSamples: 1}
	r := Evaluate(s, time.Time{}, []Objective{good, bad})
	if r.Status != StatusFail || len(r.Results) != 2 {
		t.Fatalf("overall %v n=%d", r.Status, len(r.Results))
	}
}

func hasReason(rs []Reason, want Reason) bool {
	for _, r := range rs {
		if r == want {
			return true
		}
	}
	return false
}
