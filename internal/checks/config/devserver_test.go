package config

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/analyze"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/billing"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/history"
)

// TestSchedulesOnDevServer runs the Schedule checks against a real server.
// Set TCOPT_CFG_DEVSERVER to its address, for example localhost:17304.
func TestSchedulesOnDevServer(t *testing.T) {
	addr := os.Getenv("TCOPT_CFG_DEVSERVER")
	if addr == "" {
		t.Skip("set TCOPT_CFG_DEVSERVER to run against a dev server")
	}
	c, err := client.Dial(client.Options{HostPort: addr})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	action := func(wt string) *client.ScheduleWorkflowAction {
		return &client.ScheduleWorkflowAction{ID: wt + "-" + suffix, Workflow: wt, TaskQueue: "tcopt-cfg-test"}
	}
	opts := []client.ScheduleOptions{
		{ID: "poll-" + suffix, Spec: client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: time.Minute}}}, Action: action("PollWorkflow")},
		{ID: "remind-" + suffix, Spec: client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: 24 * time.Hour}}}, Action: action("ReminderWorkflow"), RemainingActions: 1},
		{ID: "old-" + suffix, Spec: client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: time.Hour}}}, Action: action("ReportWorkflow"), Paused: true},
	}
	for _, o := range opts {
		h, err := c.ScheduleClient().Create(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Delete(context.Background())
	}
	// Visibility catches up a moment after creation.
	time.Sleep(2 * time.Second)

	rules, err := billing.LoadRules()
	if err != nil {
		t.Fatal(err)
	}
	e := &analyze.Env{
		Reader: history.NewReader(c.WorkflowService(), "default", 100, ""),
		Rules:  rules, Window: 7 * 24 * time.Hour,
	}
	fs, err := New(Options{}).Collect(ctx, e)
	t.Logf("caveats: %v", err)
	seen := map[string]bool{}
	for _, f := range fs {
		seen[f.ID+"/"+f.WorkflowType] = true
		t.Logf("%s %s %+v %+v", f.ID, f.WorkflowType, f.ProjectedMonthly, f.Evidence)
	}
	for _, want := range []string{"CFG-007/PollWorkflow", "CFG-007/ReminderWorkflow", "CFG-008/ReportWorkflow"} {
		if !seen[want] {
			t.Errorf("missing %s", want)
		}
	}
}
