package slogate

import "testing"

func TestBinomialFastDistribution(t *testing.T) {
	tab := makeLogFactorial(2000)
	cases := []struct {
		n   uint64
		p   float64
		tol float64
	}{
		{1000, 0.5, 0.02}, {1000, 0.01, 0.06}, {1000, 0.99, 0.002}, {1000, 0.2, 0.04},
		{30, 0.5, 0.07}, {1, 0.3, 0.02},
	}
	const reps = 40000
	for _, c := range cases {
		rng := newLCG(321)
		var sum uint64
		for i := 0; i < reps; i++ {
			sum += binomialFast(rng, c.n, c.p, tab)
		}
		mean := float64(sum) / reps
		want := float64(c.n) * c.p
		tol := c.tol * want
		if tol < 1 {
			tol = 1
		}
		if abs64f(mean-want) > tol {
			t.Fatalf("n=%d p=%v mean=%v want~%v", c.n, c.p, mean, want)
		}
	}
}

func abs64f(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
