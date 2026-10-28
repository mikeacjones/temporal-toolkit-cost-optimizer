// Package estimate runs detectors over a sample of executions and turns their
// matches into findings with prevalence and Actions saved.
package estimate

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// MaxEvidence caps how many examples a finding cites.
const MaxEvidence = 5

// SourceHistory marks findings from the history detectors (section 6.4).
const SourceHistory = "history"

// Finding is one detector's result for one Workflow Type.
type Finding struct {
	ID           string            `json:"id"`
	Title        string            `json:"title"`
	Source       string            `json:"source"`
	WorkflowType string            `json:"workflowType,omitempty"`
	Confidence   detect.Confidence `json:"confidence"`
	// Prevalence is how much of the sample matched.
	Prevalence               *Prevalence `json:"prevalence,omitempty"`
	ActionsSavedPerExecution Percentiles `json:"actionsSavedPerExecution"`
	ProjectedMonthly         *Projection `json:"projectedMonthly,omitempty"`
	Evidence                 []Evidence  `json:"evidence"`
	Guidance                 string      `json:"guidance"`
	// Features lists the kb/features.yaml IDs the fix needs, and
	// FeatureStatus says whether the user's SDKs have them, such as
	// "Needs Go SDK v1.49.0 or later; you have v1.44.0".
	Features      []string `json:"features,omitempty"`
	FeatureStatus []string `json:"featureStatus,omitempty"`
	// Keys are names from the evidence that the code locator ranks with.
	Keys []string `json:"keys,omitempty"`
}

// Prevalence is how many sampled executions matched, with a 95% Wilson
// interval on the matching share.
type Prevalence struct {
	Matched int     `json:"matched"`
	Sampled int     `json:"sampled"`
	Low     float64 `json:"low"`
	High    float64 `json:"high"`
}

// Share is Matched over Sampled.
func (p *Prevalence) Share() float64 {
	if p == nil || p.Sampled == 0 {
		return 0
	}
	return float64(p.Matched) / float64(p.Sampled)
}

// Percentiles of Actions per execution. Mean is set where it's useful,
// such as for the Actions each run costs today.
type Percentiles struct {
	P50  float64 `json:"p50"`
	P95  float64 `json:"p95"`
	Mean float64 `json:"mean,omitempty"`
}

// Summarize returns the p50, p95, and mean of xs.
func Summarize(xs []float64) Percentiles {
	p := percentiles(xs)
	if len(xs) > 0 {
		p.Mean = math.Round(sum(xs)/float64(len(xs))*10) / 10
	}
	return p
}

// Projection is what a finding would save in a month, scaling the sample to
// the month's executions.
type Projection struct {
	Executions int64   `json:"executions,omitempty"`
	Actions    float64 `json:"actions"`
	// ActiveGBHours and RetainedGBHours are storage savings, for storage
	// findings.
	ActiveGBHours   float64 `json:"activeGbHours,omitempty"`
	RetainedGBHours float64 `json:"retainedGbHours,omitempty"`
}

// Evidence cites the events in an execution that prove a finding.
type Evidence struct {
	WorkflowID string  `json:"workflowId,omitempty"`
	RunID      string  `json:"runId,omitempty"`
	EventIDs   []int64 `json:"eventIds,omitempty"`
	Note       string  `json:"note"`
}

// Input describes one Workflow Type's sample.
type Input struct {
	WorkflowType string
	Executions   []*model.Execution
	// MonthlyExecutions is the number of executions per month, or -1 when
	// unknown.
	MonthlyExecutions int64
	// Titles maps detector IDs to titles from the guidance.
	Titles map[string]string
}

// Run applies the detectors to every execution in the sample and returns one
// finding per detector that matched, largest projected saving first.
func Run(in Input, detectors []detect.Detector, c *detect.Context) []Finding {
	var out []Finding
	n := len(in.Executions)
	for _, d := range detectors {
		var perExec, active, retained []float64
		var evidence []Evidence
		keySet := map[string]bool{}
		for _, ex := range in.Executions {
			matches := d.Detect(ex, c)
			if len(matches) == 0 {
				continue
			}
			var saved, act, ret float64
			for i, m := range matches {
				saved += m.ActionsSaved
				act += m.ActiveGBHoursSaved
				ret += m.RetainedGBHoursSaved
				for _, k := range m.Keys {
					keySet[k] = true
				}
				if i == 0 && len(evidence) < MaxEvidence {
					evidence = append(evidence, Evidence{
						WorkflowID: ex.WorkflowID, RunID: ex.RunID,
						EventIDs: m.EventIDs, Note: m.Note,
					})
				}
			}
			perExec = append(perExec, saved)
			active = append(active, act)
			retained = append(retained, ret)
		}
		if len(perExec) == 0 {
			continue
		}
		prev := prevalence(len(perExec), n)
		if pf, ok := d.(detect.PrevalenceFloor); ok && prev.Share() < pf.MinPrevalence() {
			continue
		}

		f := Finding{
			ID:                       d.ID(),
			Title:                    in.Titles[d.ID()],
			Source:                   SourceHistory,
			WorkflowType:             in.WorkflowType,
			Confidence:               d.Confidence(),
			Prevalence:               &prev,
			ActionsSavedPerExecution: percentiles(perExec),
			Evidence:                 evidence,
			Guidance:                 d.ID(),
			Keys:                     sortedKeys(keySet),
		}
		if fu, ok := d.(detect.FeatureUser); ok {
			f.Features = fu.Features()
		}
		if in.MonthlyExecutions >= 0 && n > 0 {
			scale := float64(in.MonthlyExecutions) / float64(n)
			f.ProjectedMonthly = &Projection{
				Executions:      in.MonthlyExecutions,
				Actions:         math.Round(sum(perExec) * scale),
				ActiveGBHours:   round3(sum(active) * scale),
				RetainedGBHours: round3(sum(retained) * scale),
			}
		}
		out = append(out, f)
	}
	SortFindings(out)
	return out
}

// SortFindings orders findings by projected Actions saved, then by matched
// share.
func SortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i].ProjectedMonthly, fs[j].ProjectedMonthly
		if a != nil && b != nil && a.Actions != b.Actions {
			return a.Actions > b.Actions
		}
		if (a == nil) != (b == nil) {
			return a != nil
		}
		if a != nil && b != nil && a.ActiveGBHours+a.RetainedGBHours != b.ActiveGBHours+b.RetainedGBHours {
			return a.ActiveGBHours+a.RetainedGBHours > b.ActiveGBHours+b.RetainedGBHours
		}
		return fs[i].Prevalence.Share() > fs[j].Prevalence.Share()
	})
}

// prevalence computes the 95% Wilson score interval for k of n.
func prevalence(k, n int) Prevalence {
	p := Prevalence{Matched: k, Sampled: n}
	if n == 0 {
		return p
	}
	const z = 1.96
	nf := float64(n)
	phat := float64(k) / nf
	denom := 1 + z*z/nf
	center := (phat + z*z/(2*nf)) / denom
	half := z * math.Sqrt(phat*(1-phat)/nf+z*z/(4*nf*nf)) / denom
	p.Low = round3(math.Max(0, center-half))
	p.High = round3(math.Min(1, center+half))
	return p
}

func percentiles(xs []float64) Percentiles {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return Percentiles{P50: quantile(s, 0.5), P95: quantile(s, 0.95)}
}

// quantile uses the nearest-rank method on sorted values.
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := max(int(math.Ceil(q*float64(len(sorted))))-1, 0)
	return sorted[i]
}

func sum(xs []float64) float64 {
	var t float64
	for _, x := range xs {
		t += x
	}
	return t
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// FormatActions writes a count of Actions compactly: 2, 1,240, 410K, 8.2M.
func FormatActions(x float64) string {
	switch a := math.Abs(x); {
	case a >= 1e9:
		return trimZero(fmt.Sprintf("%.1f", x/1e9)) + "B"
	case a >= 1e6:
		return trimZero(fmt.Sprintf("%.1f", x/1e6)) + "M"
	case a >= 1e4:
		return trimZero(fmt.Sprintf("%.0f", x/1e3)) + "K"
	case a >= 100 || x == math.Trunc(x):
		return commas(int64(math.Round(x)))
	}
	return trimZero(fmt.Sprintf("%.1f", x))
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

func commas(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprint(n)
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
