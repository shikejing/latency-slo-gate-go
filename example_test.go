package slogate_test

import (
	"fmt"
	"time"

	"github.com/shikejing/latency-slo-gate-go"
)

func Example() {
	cfg := slogate.HistogramConfig{MinValue: 1, MaxValue: 1_000_000, RelativeError: 0.01}
	h, _ := slogate.New(cfg)
	for i := 0; i < 1900; i++ {
		h.Observe(float64(50 + i%200))
	}
	// A slow tail pushes p99 past the limit; one sample exceeds the
	// tracked range and lands in the overflow region.
	for i := 0; i < 100; i++ {
		h.ObserveOutcome(float64(5000+i), true)
	}
	h.ObserveOutcome(5_000_000, true)

	s := h.Snapshot()
	p99, region, _ := s.QuantileEx(0.99)

	obj := slogate.Objective{
		Name: "p99", Percentile: 99, LatencyLimit: 1000,
		ErrorBudget: 0.001, MinSamples: 100, WarnFraction: 0.9,
	}
	rep := slogate.Evaluate(s, time.Unix(0, 0), []slogate.Objective{obj})

	fmt.Println("total:", s.Total)
	fmt.Println("overflow:", s.Overflow, "p99-region:", int(region))
	fmt.Printf("p99>limit: %v\n", p99 > obj.LatencyLimit)
	fmt.Println("status:", rep.Status, "error-fraction:", float64(s.Errors)/float64(s.Total))
	// Output:
	// total: 2001
	// overflow: 1 p99-region: 0
	// p99>limit: true
	// status: fail error-fraction: 0.050474762618690654
}
