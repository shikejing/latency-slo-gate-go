package latslo

import (
	"testing"
	"time"
)

func TestSLOPass(t *testing.T) {
	cfg := DefaultConfig()
	h, _ := NewHistogram(cfg, 1)
	// All samples at 10ms: p99 well under 100ms, error budget 0%.
	for i := 0; i < 10000; i++ {
		h.Record(10 * time.Millisecond)
	}
	snap := h.Snapshot()
	slo := SLO{Name: "p99", Percentile: 0.99, Threshold: 100 * time.Millisecond, ErrorBudget: 0.01}
	res := slo.Evaluate(snap)
	if !res.Pass {
		t.Errorf("expected pass, got reason %s", res.Reason)
	}
	if res.Reason != ReasonOK {
		t.Errorf("reason=%s, want OK", res.Reason)
	}
}

func TestSLOPercentileExceeded(t *testing.T) {
	cfg := DefaultConfig()
	h, _ := NewHistogram(cfg, 1)
	// 5% of samples at 200ms; p99 will be ~200ms > 100ms threshold.
	for i := 0; i < 9500; i++ {
		h.Record(10 * time.Millisecond)
	}
	for i := 0; i < 500; i++ {
		h.Record(200 * time.Millisecond)
	}
	snap := h.Snapshot()
	slo := SLO{Name: "p99", Percentile: 0.99, Threshold: 100 * time.Millisecond, ErrorBudget: 0.01}
	res := slo.Evaluate(snap)
	if res.Pass {
		t.Errorf("expected fail, got pass")
	}
	if res.Reason != ReasonPercentileExceeded {
		t.Errorf("reason=%s, want PERCENTILE_EXCEEDED", res.Reason)
	}
}

func TestSLOErrorBudgetExhausted(t *testing.T) {
	cfg := DefaultConfig()
	h, _ := NewHistogram(cfg, 1)
	// 0.5% of samples at 150ms: p99 = 10ms (passes percentile), but the
	// over-threshold fraction (0.5%) exceeds the 0.1% error budget.
	for i := 0; i < 9950; i++ {
		h.Record(10 * time.Millisecond)
	}
	for i := 0; i < 50; i++ {
		h.Record(150 * time.Millisecond)
	}
	snap := h.Snapshot()
	slo := SLO{Name: "p99", Percentile: 0.99, Threshold: 100 * time.Millisecond, ErrorBudget: 0.001}
	res := slo.Evaluate(snap)
	if res.Pass {
		t.Errorf("expected fail, got pass")
	}
	if res.Reason != ReasonErrorBudgetExhausted {
		t.Errorf("reason=%s, want ERROR_BUDGET_EXHAUSTED (budgetUsed=%.4f)", res.Reason, res.ErrorBudgetUsed)
	}
}

func TestSLOEmptySnapshot(t *testing.T) {
	cfg := DefaultConfig()
	h, _ := NewHistogram(cfg, 1)
	snap := h.Snapshot()
	slo := SLO{Name: "p99", Percentile: 0.99, Threshold: 100 * time.Millisecond, ErrorBudget: 0.01}
	res := slo.Evaluate(snap)
	if !res.Pass {
		t.Errorf("empty snapshot should pass, got reason %s", res.Reason)
	}
}

func TestEvaluateAll(t *testing.T) {
	cfg := DefaultConfig()
	h, _ := NewHistogram(cfg, 1)
	for i := 0; i < 10000; i++ {
		h.Record(10 * time.Millisecond)
	}
	snap := h.Snapshot()
	slos := []SLO{
		{Name: "p50", Percentile: 0.50, Threshold: 50 * time.Millisecond, ErrorBudget: 0.01},
		{Name: "p99", Percentile: 0.99, Threshold: 5 * time.Millisecond, ErrorBudget: 0.01},
	}
	results := EvaluateAll(snap, slos)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if !results[0].Pass {
		t.Errorf("p50 should pass, got %s", results[0].Reason)
	}
	if results[1].Pass {
		t.Errorf("p99 should fail, got pass")
	}
}
