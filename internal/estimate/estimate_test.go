package estimate

import (
	"testing"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// fixed is a detector that saves a set number of Actions on chosen
// executions.
type fixed map[string]float64

func (fixed) ID() string                    { return "T-001" }
func (fixed) Confidence() detect.Confidence { return detect.High }
func (f fixed) Detect(ex *model.Execution, _ *detect.Context) []detect.Match {
	if s, ok := f[ex.WorkflowID]; ok {
		return []detect.Match{{EventIDs: []int64{1}, ActionsSaved: s}}
	}
	return nil
}

func TestRun(t *testing.T) {
	var exs []*model.Execution
	for _, id := range []string{"a", "b", "c", "d"} {
		exs = append(exs, &model.Execution{WorkflowID: id})
	}
	in := Input{
		WorkflowType:      "OrderWorkflow",
		Executions:        exs,
		MonthlyExecutions: 1_000_000,
	}
	fs := Run(in, []detect.Detector{fixed{"a": 2, "b": 4}}, &detect.Context{})
	if len(fs) != 1 {
		t.Fatalf("got %d findings", len(fs))
	}
	f := fs[0]
	if f.Prevalence.Matched != 2 || f.Prevalence.Sampled != 4 {
		t.Errorf("prevalence %+v", f.Prevalence)
	}
	if f.Prevalence.Low >= 0.5 || f.Prevalence.High <= 0.5 {
		t.Errorf("interval %+v should contain 0.5", f.Prevalence)
	}
	if f.ActionsSavedPerExecution.P50 != 2 || f.ActionsSavedPerExecution.P95 != 4 {
		t.Errorf("percentiles %+v", f.ActionsSavedPerExecution)
	}
	// 6 Actions over 4 sampled executions, scaled to a million: 1.5M Actions.
	if f.ProjectedMonthly.Actions != 1_500_000 {
		t.Errorf("projection %+v", f.ProjectedMonthly)
	}
}

func TestRunUnknownVolume(t *testing.T) {
	in := Input{Executions: []*model.Execution{{WorkflowID: "a"}}, MonthlyExecutions: -1}
	fs := Run(in, []detect.Detector{fixed{"a": 1}}, &detect.Context{})
	if fs[0].ProjectedMonthly != nil {
		t.Error("expected no projection without a monthly volume")
	}
}

func TestFormatActions(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 2: "2", 0.5: "0.5", 1240: "1,240", 41_000: "41K", 8_200_000: "8.2M", 3_000_000: "3M"} {
		if got := FormatActions(in); got != want {
			t.Errorf("FormatActions(%v) = %q, want %q", in, got, want)
		}
	}
}
