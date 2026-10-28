package activity

import (
	"context"
	"errors"
	"fmt"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/workloads"
)

// taskQueue matches workloads.Group.TaskQueue for this group. ACT-006's
// Drive starts a second Worker on it.
const taskQueue = "tcopt-workloads-activity"

func init() {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	workloads.Add(workloads.Group{
		Name:    "activity",
		Workers: startWorkers,
		Runs: []workloads.Run{
			// ACT-003: unlimited attempts, against a capped policy.
			{Golden: "act003_positive", Workflow: CallWithRetry, Args: []any{RetryInput{Activity: "Flaky", FailUntil: 3, Interval: ms(20)}}},
			{Golden: "act003_negative", Workflow: CallWithRetry, Args: []any{RetryInput{Activity: "Flaky", FailUntil: 3, MaxAttempts: 5, Interval: ms(20)}}},
			// ACT-006: only a Worker without Ship polls until the Activity
			// has failed; then a Worker with Ship joins.
			{Golden: "act006_positive", Workflow: ShipOrder, Drive: joinShipWorker, Timeout: 2 * time.Minute},
			{Golden: "act006_negative", Workflow: CallWithRetry, Args: []any{RetryInput{Activity: "Flaky", FailUntil: 2, Interval: ms(20)}}},
			// LA-003: a 6 s Local Activity against a 2 s Workflow Task timeout.
			{Golden: "la003_positive", Workflow: LocalSlow, Args: []any{6 * time.Second},
				Options: client.StartWorkflowOptions{WorkflowTaskTimeout: 2 * time.Second}, Timeout: 2 * time.Minute},
			{Golden: "la003_negative", Workflow: LocalSlow, Args: []any{100 * time.Millisecond},
				Options: client.StartWorkflowOptions{WorkflowTaskTimeout: 2 * time.Second}},
		},
		Load: []workloads.LoadItem{
			{Weight: 30, Workflow: CallWithRetry, Args: []any{RetryInput{Activity: "Flaky", FailUntil: 12, Interval: ms(20)}}},
			{Weight: 20, Workflow: LocalSlow, Args: []any{6 * time.Second},
				Options: client.StartWorkflowOptions{WorkflowTaskTimeout: 2 * time.Second}},
		},
	})
}

// startWorkers starts the group's main Worker. It registers every Workflow
// and Activity except Ship, which ACT-006's second Worker adds.
func startWorkers(c client.Client, tq string) (func(), error) {
	w := worker.New(c, tq, worker.Options{})
	w.RegisterWorkflow(CallWithRetry)
	w.RegisterWorkflow(ShipOrder)
	w.RegisterWorkflow(LocalSlow)
	w.RegisterActivity(Flaky)
	if err := w.Start(); err != nil {
		return nil, err
	}
	return w.Stop, nil
}

// RetryInput picks an Activity and its retry policy. MaxAttempts 0 means
// unlimited.
type RetryInput struct {
	Activity    string
	FailUntil   int
	MaxAttempts int32
	Interval    time.Duration
}

// CallWithRetry runs one Activity with a short retry interval and completes
// whether or not it succeeds.
func CallWithRetry(ctx workflow.Context, in RetryInput) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    in.Interval,
			BackoffCoefficient: 1,
			MaximumInterval:    in.Interval,
			MaximumAttempts:    in.MaxAttempts,
		},
	})
	err := workflow.ExecuteActivity(ctx, in.Activity, in.FailUntil).Get(ctx, nil)
	if err != nil {
		workflow.GetLogger(ctx).Info("Activity gave up", "error", err)
	}
	return nil
}

// Flaky fails with a different message on each attempt until failUntil.
func Flaky(ctx context.Context, failUntil int) error {
	if a := int(activity.GetInfo(ctx).Attempt); a < failUntil {
		return fmt.Errorf("upstream unavailable on attempt %d", a)
	}
	return nil
}

// ShipOrder calls Ship, which only the second Worker registers.
func ShipOrder(ctx workflow.Context) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: 200 * time.Millisecond, BackoffCoefficient: 1},
	})
	return workflow.ExecuteActivity(ctx, Ship).Get(ctx, nil)
}

// Ship stands in for a shipping call.
func Ship(context.Context) error { return nil }

// joinShipWorker waits until Ship has failed at least once on the main
// Worker, then starts a Worker that has it, and stops that Worker once the
// Workflow completes.
func joinShipWorker(ctx context.Context, c client.Client, run client.WorkflowRun) error {
	for {
		desc, err := c.DescribeWorkflowExecution(ctx, run.GetID(), run.GetRunID())
		if err != nil {
			return err
		}
		pending := desc.GetPendingActivities()
		if len(pending) > 0 && pending[0].GetAttempt() >= 3 {
			break
		}
		if desc.GetWorkflowExecutionInfo().GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
			return errors.New("ShipOrder closed before Ship failed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	w := worker.New(c, taskQueue, worker.Options{})
	w.RegisterActivity(Ship)
	if err := w.Start(); err != nil {
		return err
	}
	defer w.Stop()
	return run.Get(ctx, nil)
}

// LocalSlow runs a Local Activity that sleeps for d.
func LocalSlow(ctx workflow.Context, d time.Duration) error {
	ctx = workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{StartToCloseTimeout: d + 10*time.Second})
	return workflow.ExecuteLocalActivity(ctx, Sleep, d).Get(ctx, nil)
}

// Sleep waits for d.
func Sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
