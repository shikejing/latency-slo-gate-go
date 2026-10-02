package slogate

import (
	"testing"
	"time"
)

func TestEndToEndDeterministic(t *testing.T) {
	mk := func(seed int64, n int, med float64) Snapshot {
		return logNormalSnap(t, seed, n, med, 0.15)
	}
	cfg := RegressionConfig{MinSamples: 200, Alpha: 0.05, Tolerance: 0.05,
		NoiseMargin: 0.01, BootstrapReplicates: 800, Seed: 2024}
	objs := []Objective{{Name: "p99", Percentile: 99, LatencyLimit: 300,
		ErrorBudget: 0.01, MinSamples: 100, WarnFraction: 0.9}}

	var prevReg *RegressionResult
	var prevRep *Report
	for iter := 0; iter < 5; iter++ {
		base := mk(11, 1200, 100)
		cur := mk(22, 1200, 118)
		r := NewDetector(cfg).Compare(cur, base)
		rep := Evaluate(cur, time.Unix(1700000000, 0), objs)
		if prevReg != nil {
			if r.CILeft != prevReg.CILeft || r.CIRight != prevReg.CIRight ||
				r.PValueUp != prevReg.PValueUp || r.Verdict != prevReg.Verdict {
				t.Fatalf("regression not reproducible on iter %d", iter)
			}
			if rep.Status != prevRep.Status ||
				rep.Results[0].Percentile != prevRep.Results[0].Percentile {
				t.Fatalf("SLO report not reproducible on iter %d", iter)
			}
		}
		prevReg, prevRep = &r, &rep
	}
}
