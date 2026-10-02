package slogate

import "math"

// RegressionConfig configures baseline regression detection.
type RegressionConfig struct {
	// MinSamples is the required size of EACH distribution before any
	// definite verdict. Below it the verdict is Inconclusive.
	MinSamples uint64
	// Alpha is the significance level (e.g. 0.05). A one-sided test must
	// show a regression with p < Alpha to declare one.
	Alpha float64
	// Tolerance is the minimum relative median increase (e.g. 0.05 for 5%)
	// that counts as a meaningful regression. Smaller shifts, even if
	// statistically significant, are reported within tolerance.
	Tolerance float64
	// NoiseMargin is how far the bootstrap confidence interval must clear
	// the tolerance threshold before a definite verdict is returned. When
	// the CI straddles the decision boundary the verdict is Inconclusive
	// (random noise cannot be separated from a real shift).
	NoiseMargin float64
	// BootstrapReplicates is the number of deterministic resamples for the
	// effect confidence interval. 0 disables the bootstrap (CI collapses
	// to the point estimate).
	BootstrapReplicates int
	// Seed makes every statistic bit-for-bit reproducible for equal inputs.
	Seed int64
}

// DefaultRegressionConfig returns conservative defaults.
func DefaultRegressionConfig() RegressionConfig {
	return RegressionConfig{
		MinSamples:          100,
		Alpha:               0.05,
		Tolerance:           0.05,
		NoiseMargin:         0.02,
		BootstrapReplicates: 10000,
		Seed:                1,
	}
}

func (c RegressionConfig) validate() error {
	if c.Alpha <= 0 || c.Alpha >= 1 {
		return ErrInvalidArgument
	}
	if c.Tolerance < 0 || c.NoiseMargin < 0 {
		return ErrInvalidArgument
	}
	if c.BootstrapReplicates < 0 {
		return ErrInvalidArgument
	}
	return nil
}

// Verdict is the regression decision.
type Verdict int

const (
	VerdictInconclusive Verdict = iota
	VerdictNoRegression
	VerdictRegression
	VerdictImprovement
)

func (v Verdict) String() string {
	switch v {
	case VerdictNoRegression:
		return "no_regression"
	case VerdictRegression:
		return "regression"
	case VerdictImprovement:
		return "improvement"
	default:
		return "inconclusive"
	}
}

// RegressionReason distinguishes causes of a verdict.
type RegressionReason int

const (
	RegReasonNone RegressionReason = iota
	RegReasonInsufficientSamples
	RegReasonHighVariance
	RegReasonEffectBelowTolerance
	RegReasonSignificant
)

func (r RegressionReason) String() string {
	switch r {
	case RegReasonInsufficientSamples:
		return "insufficient_samples"
	case RegReasonHighVariance:
		return "high_variance"
	case RegReasonEffectBelowTolerance:
		return "effect_below_tolerance"
	case RegReasonSignificant:
		return "significant"
	default:
		return "none"
	}
}

// RegressionResult is the output of a baseline comparison.
type RegressionResult struct {
	Verdict        Verdict
	Reasons        []RegressionReason
	PValueUp       float64 // one-sided p for current being LARGER
	PValueDown     float64 // one-sided p for current being SMALLER
	EstEffect      float64 // current median / baseline median - 1
	CILeft         float64 // bootstrap 2.5% bound of EstEffect
	CIRight        float64 // bootstrap 97.5% bound of EstEffect
	BaselineMedian float64
	CurrentMedian  float64
}

// Detector compares current distributions against a baseline.
type Detector struct{ cfg RegressionConfig }

// NewDetector builds a detector with cfg.
func NewDetector(cfg RegressionConfig) *Detector { return &Detector{cfg: cfg} }

// Compare runs a deterministic regression test. Equal inputs with an
// equal config Seed always produce equal output.
func (d *Detector) Compare(current, baseline Snapshot) RegressionResult {
	res := RegressionResult{Verdict: VerdictInconclusive}
	if err := d.cfg.validate(); err != nil {
		return res
	}
	if !current.MergeableWith(baseline) || current.Total == 0 || baseline.Total == 0 {
		res.Reasons = []RegressionReason{RegReasonInsufficientSamples}
		return res
	}
	if current.Total < d.cfg.MinSamples || baseline.Total < d.cfg.MinSamples {
		res.Reasons = []RegressionReason{RegReasonInsufficientSamples}
		return res
	}

	baseMed, _ := baseline.Quantile(0.5)
	curMed, _ := current.Quantile(0.5)
	res.BaselineMedian, res.CurrentMedian = baseMed, curMed
	res.EstEffect = curMed/baseMed - 1

	res.PValueUp, res.PValueDown = mannWhitney(current, baseline)

	if d.cfg.BootstrapReplicates > 0 {
		res.CILeft, res.CIRight = bootstrapEffect(current, baseline,
			d.cfg.BootstrapReplicates, d.cfg.Seed)
	} else {
		res.CILeft, res.CIRight = res.EstEffect, res.EstEffect
	}

	tol, noise := d.cfg.Tolerance, d.cfg.NoiseMargin
	sigUp := res.PValueUp < d.cfg.Alpha
	sigDown := res.PValueDown < d.cfg.Alpha

	switch {
	case sigUp && res.CILeft >= tol-noise:
		if res.EstEffect >= tol {
			res.Verdict, res.Reasons = VerdictRegression, []RegressionReason{RegReasonSignificant}
		} else {
			res.Verdict, res.Reasons = VerdictNoRegression, []RegressionReason{RegReasonEffectBelowTolerance}
		}
	case sigUp:
		res.Verdict, res.Reasons = VerdictInconclusive, []RegressionReason{RegReasonHighVariance}
	case sigDown && res.CIRight <= -tol+noise:
		res.Verdict, res.Reasons = VerdictImprovement, []RegressionReason{RegReasonSignificant}
	case sigDown:
		res.Verdict, res.Reasons = VerdictInconclusive, []RegressionReason{RegReasonHighVariance}
	default:
		res.Verdict, res.Reasons = VerdictNoRegression, []RegressionReason{RegReasonEffectBelowTolerance}
	}
	return res
}

type massPoint struct {
	value float64
	mass  uint64
}

func points(s Snapshot) []massPoint {
	n := s.Config.BucketCount()
	ps := make([]massPoint, 0, n+2)
	if s.Underflow > 0 {
		ps = append(ps, massPoint{s.UnderflowMin, s.Underflow})
	}
	for i, c := range s.Counts {
		if c > 0 {
			ps = append(ps, massPoint{s.Config.bucketValue(i), c})
		}
	}
	if s.Overflow > 0 {
		ps = append(ps, massPoint{s.OverflowMax, s.Overflow})
	}
	return ps
}

// mannWhitney returns one-sided p-values P(current larger) and the reverse
// using the normal approximation with tie correction and continuity
// correction.
func mannWhitney(cur, base Snapshot) (up, down float64) {
	a, b := points(cur), points(base)
	n1, n2 := float64(cur.Total), float64(base.Total)

	type tv struct {
		v      float64
		m1, m2 uint64
	}
	all := make([]tv, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case j == len(b) || (i < len(a) && a[i].value < b[j].value):
			all = append(all, tv{a[i].value, a[i].mass, 0})
			i++
		case i == len(a) || b[j].value < a[i].value:
			all = append(all, tv{b[j].value, 0, b[j].mass})
			j++
		default:
			all = append(all, tv{a[i].value, a[i].mass, b[j].mass})
			i++
			j++
		}
	}

	var rank, sumRanks1, tieSum float64 = 1, 0, 0
	N := n1 + n2
	for _, e := range all {
		t := float64(e.m1 + e.m2)
		avg := rank + (t-1)/2
		sumRanks1 += avg * float64(e.m1)
		tieSum += t*t*t - t
		rank += t
	}
	u1 := sumRanks1 - n1*(n1+1)/2
	mean := n1 * n2 / 2
	varT := n1 * n2 / 12 * ((N + 1) - tieSum/(N*(N-1)))
	if varT <= 0 {
		return 1, 1
	}
	sd := math.Sqrt(varT)
	up = 1 - normalCDF((u1-mean-0.5)/sd)
	down = 1 - normalCDF((mean-u1-0.5)/sd)
	return up, down
}

// distModel is a compact, reusable description of one histogram's mass
// cells for deterministic resampling.
type distModel struct {
	n       int
	cells   []modelCell
	weights []uint64
	counts  []uint64 // scratch for one resample
	segs    []medSeg // scratch for median interpolation
	total   uint64
	logFact []float64
}

type medSeg struct {
	first, anchor, last float64
	mass                uint64
}

type modelCell struct {
	logv float64
	mass uint64
	kind int // -1 underflow, 0 bucket, 1 overflow
	idx  int
}

func buildModel(s Snapshot) *distModel {
	n := s.Config.BucketCount()
	m := &distModel{
		n:      n,
		cells:  make([]modelCell, 0, n+2),
		counts: make([]uint64, n),
		segs:   make([]medSeg, 0, n+2),
	}
	if s.Underflow > 0 {
		m.cells = append(m.cells, modelCell{math.Log(s.UnderflowMin), s.Underflow, -1, 0})
	}
	for i, c := range s.Counts {
		if c > 0 {
			m.cells = append(m.cells, modelCell{math.Log(s.Config.bucketValue(i)), c, 0, i})
		}
	}
	if s.Overflow > 0 {
		m.cells = append(m.cells, modelCell{math.Log(s.OverflowMax), s.Overflow, 1, 0})
	}
	m.weights = make([]uint64, len(m.cells))
	var cum uint64
	for k, cl := range m.cells {
		cum += cl.mass
		m.weights[k] = cum
	}
	m.total = cum
	m.logFact = makeLogFactorial(int(cum))
	return m
}

// makeLogFactorial returns logFact[k] = ln(k!) for k=0..n.
func makeLogFactorial(n int) []float64 {
	tab := make([]float64, n+1)
	for i := 2; i <= n; i++ {
		tab[i] = tab[i-1] + math.Log(float64(i))
	}
	return tab
}

// resampleInto draws one exact multinomial resample via sequential
// conditional binomials: cell k receives Binomial(remaining,
// cellMass/massRemaining). It consumes only O(K) random numbers per
// replicate and reuses m's scratch buffers.
func (m *distModel) resampleInto(rng *lcg) (under, over uint64) {
	for i := range m.counts {
		m.counts[i] = 0
	}
	massLeft := m.total
	remaining := m.total
	for k, cl := range m.cells {
		var drawn uint64
		if k == len(m.cells)-1 {
			drawn = remaining
		} else {
			drawn = binomialFast(rng, remaining, float64(cl.mass)/float64(massLeft), m.logFact)
		}
		switch cl.kind {
		case -1:
			under = drawn
		case 1:
			over = drawn
		default:
			m.counts[cl.idx] = drawn
		}
		remaining -= drawn
		massLeft -= cl.mass
	}
	return under, over
}

// binomialFast is an exact Binomial(n,p) sampler: direct trials for small
// n, otherwise constant-expected rejection against the exact binomial
// mass with O(1) lookups into the log-factorial table.
func binomialFast(rng *lcg, n uint64, p float64, logFact []float64) uint64 {
	if p <= 0 || n == 0 {
		return 0
	}
	if p >= 1 {
		return n
	}
	if p > 0.5 {
		return n - binomialFast(rng, n, 1-p, logFact)
	}
	if n < 25 {
		var k uint64
		for i := uint64(0); i < n; i++ {
			if rng.float64() < p {
				k++
			}
		}
		return k
	}
	mean := float64(n) * p
	sd := math.Sqrt(mean * (1 - p))
	radius := 7.0 * sd
	if radius > float64(n) {
		radius = float64(n)
	}
	maxLog := binomMassTab(mean, float64(n), p, logFact)
	for {
		u, v := rng.float64(), rng.float64()
		k := math.Floor(mean + (2*u-1)*radius + 0.5)
		if k < 0 || k > float64(n) {
			continue
		}
		lp := binomMassTab(k, float64(n), p, logFact)
		if v <= math.Exp(lp-maxLog) {
			return uint64(k)
		}
	}
}

func binomMassTab(k, n, p float64, logFact []float64) float64 {
	kk := uint64(k)
	choose := logFact[uint64(n)] - logFact[kk] - logFact[uint64(n)-kk]
	return choose + k*math.Log(p) + (n-k)*math.Log1p(-p)
}

// bootstrapMedian returns the median (log value) of the current resample
// without allocating a Snapshot, using the same anchor CDF semantics as
// Snapshot.Quantile(0.5).
func (m *distModel) bootstrapMedian(total, under, over uint64, s Snapshot) float64 {
	segs := m.segs[:0]
	add := func(first, anchor, last float64, mass uint64) {
		segs = append(segs, medSeg{math.Log(first), math.Log(anchor), math.Log(last), mass})
	}
	if under > 0 {
		add(s.UnderflowMin, s.Config.MinValue, s.Config.MinValue, under)
	}
	for i, c := range m.counts {
		if c > 0 {
			add(s.Config.bucketLower(i), s.Config.bucketValue(i), s.Config.bucketValue(i), c)
		}
	}
	if over > 0 {
		add(s.Config.bucketUpper(m.n-1), s.OverflowMax, s.OverflowMax, over)
	}
	m.segs = segs
	target := 0.5 * float64(total-1)
	if target >= float64(total-1) {
		return segs[len(segs)-1].last
	}
	lo := math.Floor(target)
	frac := target - lo
	startPos := float64(0)
	for k, sg := range segs {
		endPos := startPos + float64(sg.mass)
		if lo >= startPos && lo < endPos {
			anchorAt := func(pos float64) float64 {
				switch {
				case pos == float64(total-1):
					return segs[len(segs)-1].last
				case pos <= startPos:
					return sg.first
				default:
					if k+1 < len(segs) && pos >= endPos {
						return segs[k+1].first
					}
					return sg.anchor
				}
			}
			a := anchorAt(lo)
			b := anchorAt(lo + 1)
			return a + frac*(b-a)
		}
		startPos = endPos
	}
	return segs[len(segs)-1].last
}

// bootstrapEffect returns the [2.5%,97.5%] percentile bootstrap CI of the
// relative median shift (exp(delta)-1), fully determined by seed.
func bootstrapEffect(cur, base Snapshot, reps int, seed int64) (float64, float64) {
	cm := buildModel(cur)
	bm := buildModel(base)
	rng := newLCG(seed)
	effects := make([]float64, reps)
	for k := 0; k < reps; k++ {
		cu, co := cm.resampleInto(rng)
		bu, bo := bm.resampleInto(rng)
		cmed := math.Exp(cm.bootstrapMedian(cur.Total, cu, co, cur))
		bmed := math.Exp(bm.bootstrapMedian(base.Total, bu, bo, base))
		effects[k] = cmed/bmed - 1
	}
	sortFloat64s(effects)
	return effects[int(0.025*float64(reps))], effects[int(0.975*float64(reps))]
}

// lcg is a deterministic 64-bit PRNG (Numerical Recipes constants).
type lcg struct{ state uint64 }

func newLCG(seed int64) *lcg { return &lcg{state: uint64(seed) ^ 0x9E3779B97F4A7C15} }

func (r *lcg) next() uint64 {
	r.state = r.state*6364136223846793005 + 1442695040888963407
	return r.state
}

func (r *lcg) uint64n(n uint64) uint64 { return (r.next() >> 1) % n }

func sortFloat64s(a []float64) {
	n := len(a)
	for i := n/2 - 1; i >= 0; i-- {
		heapDown(a, i, n)
	}
	for i := n - 1; i > 0; i-- {
		a[0], a[i] = a[i], a[0]
		heapDown(a, 0, i)
	}
}

func heapDown(a []float64, i, n int) {
	for {
		l, r := 2*i+1, 2*i+2
		big := i
		if l < n && a[l] > a[big] {
			big = l
		}
		if r < n && a[r] > a[big] {
			big = r
		}
		if big == i {
			return
		}
		a[i], a[big] = a[big], a[i]
		i = big
	}
}

func normalCDF(x float64) float64 { return 0.5 * math.Erfc(-x/math.Sqrt2) }

func (r *lcg) float64() float64 {
	return float64(r.next()>>11) / float64(uint64(1)<<53)
}
