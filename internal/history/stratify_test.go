package history

import (
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
)

func TestStratifyCoversEveryGroup(t *testing.T) {
	var pool []Ref
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// 900 short completed runs, 90 long ones, and 10 failures.
	for i := range 900 {
		pool = append(pool, Ref{WorkflowID: "c", Status: enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, HistoryLen: 11, StartTime: t0.Add(time.Duration(i) * time.Minute)})
	}
	for i := range 90 {
		pool = append(pool, Ref{WorkflowID: "l", Status: enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, HistoryLen: 400, StartTime: t0.Add(time.Duration(i) * time.Hour)})
	}
	for i := range 10 {
		pool = append(pool, Ref{WorkflowID: "f", Status: enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, HistoryLen: 30, StartTime: t0.Add(time.Duration(i) * time.Hour)})
	}

	got := Stratify(pool, 50)
	if len(got) != 50 {
		t.Fatalf("got %d refs, want 50", len(got))
	}
	byID := map[string]int{}
	for _, r := range got {
		byID[r.WorkflowID]++
	}
	if byID["f"] == 0 || byID["l"] == 0 {
		t.Errorf("rare groups missing from the sample: %v", byID)
	}
	if byID["c"] < byID["l"] {
		t.Errorf("the largest group should get the largest share: %v", byID)
	}
}

func TestStratifySmallPool(t *testing.T) {
	pool := []Ref{{WorkflowID: "a"}, {WorkflowID: "b"}}
	if got := Stratify(pool, 200); len(got) != 2 {
		t.Errorf("got %d, want the whole pool", len(got))
	}
}
