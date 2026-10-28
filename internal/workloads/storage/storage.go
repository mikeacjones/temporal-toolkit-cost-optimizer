package storage

import (
	"context"
	"strings"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/workloads"
)

func init() {
	workloads.Add(workloads.Group{
		Name:    "storage",
		Workers: workers,
		Runs: []workloads.Run{
			{Golden: "sto001_positive", Workflow: LargeResult},
			{Golden: "sto001_negative", Workflow: SmallResult},
		},
		Load: []workloads.LoadItem{
			{Weight: 100, Workflow: LargeResult},
		},
	})
}

func workers(c client.Client, taskQueue string) (func(), error) {
	w := worker.New(c, taskQueue, worker.Options{})
	w.RegisterWorkflow(LargeResult)
	w.RegisterWorkflow(SmallResult)
	w.RegisterActivity(Fetch)
	if err := w.Start(); err != nil {
		return nil, err
	}
	return w.Stop, nil
}

func fetch(ctx workflow.Context, size int) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second})
	return workflow.ExecuteActivity(ctx, Fetch, size).Get(ctx, nil)
}

// LargeResult returns a 300 KiB Activity result (STO-001).
func LargeResult(ctx workflow.Context) error { return fetch(ctx, 300<<10) }

// SmallResult returns a small one. Nothing to flag.
func SmallResult(ctx workflow.Context) error { return fetch(ctx, 1<<10) }

// Fetch returns a string of the given size.
func Fetch(_ context.Context, size int) (string, error) { return strings.Repeat("x", size), nil }
