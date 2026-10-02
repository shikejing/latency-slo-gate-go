package slogate

import (
	"time"
)

// Objective is one SLO target evaluated over a window.
type Objective struct {
	// Name identifies the objective in a Report.
	Name string
	// Percentile is the latency quantile target in (0,100], e.g. 99.
	Percentile float64
	// LatencyLimit is the value the percentile must not exceed.
	LatencyLimit float64
	// ErrorBudget is the tolerated fraction of error outcomes in (0,1),
	// e.g. 0.001 for a 99.9% availability target.
	ErrorBudget float64
	// MinSamples is the minimum window sample count below which the
	// objective is Unknown/InsufficientData. Zero means no gate.
	MinSamples uint64
	// WarnFraction, when > 0, marks Warn when the percentile reaches this
	// fraction of LatencyLimit (e.g. 0.9) or the budget burn exceeds it.
	WarnFraction float64
}

// Validate checks an objective definition.
func (o Objective) Validate() error {
	if o.Percentile <= 0 || o.Percentile > 100 {
		return ErrInvalidArgument
	}
	if o.LatencyLimit <= 0 {
		return ErrInvalidArgument
	}
	if o.ErrorBudget <= 0 || o.ErrorBudget > 1 {
		return ErrInvalidArgument
	}
	if o.WarnFraction < 0 || o.WarnFraction > 1 {
		return ErrInvalidArgument
	}
	return nil
}

// Status is the overall status of an objective.
type Status int

const (
	// StatusUnknown means data was insufficient for a decision.
	StatusUnknown Status = iota
	// StatusPass means latency and error budget are within target.
	StatusPass
	// StatusWarn means a threshold is approaching but not breached.
	StatusWarn
	// StatusFail means at least one hard threshold was breached.
	StatusFail
)

func (st Status) String() string {
	switch st {
	case StatusPass:
		return "pass"
	case StatusWarn:
		return "warn"
	case StatusFail:
		return "fail"
	default:
		return "unknown"
	}
}

// Reason gives a distinguishable cause for a status.
type Reason int

const (
	ReasonNone Reason = iota
	ReasonInsufficientData
	ReasonPercentileBreach
	ReasonErrorBudgetExhausted
	ReasonErrorBudgetBurning
	ReasonPercentileNearLimit
)

func (r Reason) String() string {
	switch r {
	case ReasonInsufficientData:
		return "insufficient_data"
	case ReasonPercentileBreach:
		return "percentile_breach"
	case ReasonErrorBudgetExhausted:
		return "error_budget_exhausted"
	case ReasonErrorBudgetBurning:
		return "error_budget_burning"
	case ReasonPercentileNearLimit:
		return "percentile_near_limit"
	default:
		return "none"
	}
}

// ObjectiveResult is the evaluation of one objective.
type ObjectiveResult struct {
	Objective        Objective
	Status           Status
	Reasons          []Reason
	Percentile       float64
	PercentileRegion Region
	ErrorRatio       float64
	// BudgetUsed is the fraction of the error budget consumed
	// (errorRate / ErrorBudget); >1 means the budget is exhausted.
	BudgetUsed float64
}

// Report is the full multi-objective evaluation result. Its overall
// Status is the worst individual status (Fail > Warn > Pass > Unknown).
type Report struct {
	At      time.Time
	Status  Status
	Results []ObjectiveResult
}

// Evaluate checks every objective against snapshot s at time at.
// It is a pure function of its inputs and therefore deterministic.
func Evaluate(s Snapshot, at time.Time, objectives []Objective) Report {
	r := Report{At: at, Results: make([]ObjectiveResult, 0, len(objectives))}
	overall := StatusPass
	anyKnown := false
	for _, o := range objectives {
		res := evaluateOne(s, o)
		r.Results = append(r.Results, res)
		if res.Status != StatusUnknown {
			anyKnown = true
		}
		if res.Status > overall {
			overall = res.Status
		}
	}
	if !anyKnown && len(objectives) > 0 {
		overall = StatusUnknown
	}
	r.Status = overall
	return r
}

func evaluateOne(s Snapshot, o Objective) ObjectiveResult {
	res := ObjectiveResult{Objective: o}
	if err := o.Validate(); err != nil {
		res.Status = StatusUnknown
		res.Reasons = []Reason{ReasonInsufficientData}
		return res
	}
	if s.Total < o.MinSamples || s.Total == 0 {
		res.Status = StatusUnknown
		res.Reasons = []Reason{ReasonInsufficientData}
		return res
	}

	q := o.Percentile / 100
	p, region, qerr := s.QuantileEx(q)
	res.Percentile, res.PercentileRegion = p, region
	if qerr != nil {
		res.Status = StatusUnknown
		res.Reasons = []Reason{ReasonInsufficientData}
		return res
	}

	res.ErrorRatio = float64(s.Errors) / float64(s.Total)
	res.BudgetUsed = res.ErrorRatio / o.ErrorBudget

	hard := false
	warn := false

	if p > o.LatencyLimit {
		hard = true
		res.Reasons = append(res.Reasons, ReasonPercentileBreach)
	} else if o.WarnFraction > 0 && p >= o.LatencyLimit*o.WarnFraction {
		warn = true
		res.Reasons = append(res.Reasons, ReasonPercentileNearLimit)
	}

	// Budget exhausted means observed error rate at or beyond the budget.
	// Conservatively account for a single potential unobserved failure in
	// a small window? No: evaluate on observed counts only; thresholds are
	// tunable and MinSamples gates noisy small samples.
	if res.ErrorRatio > o.ErrorBudget {
		hard = true
		res.Reasons = append(res.Reasons, ReasonErrorBudgetExhausted)
	} else if o.WarnFraction > 0 && res.BudgetUsed >= o.WarnFraction {
		warn = true
		res.Reasons = append(res.Reasons, ReasonErrorBudgetBurning)
	}
	if len(res.Reasons) == 0 {
		res.Reasons = []Reason{ReasonNone}
	}
	switch {
	case hard:
		res.Status = StatusFail
	case warn:
		res.Status = StatusWarn
	default:
		res.Status = StatusPass
	}
	return res
}
