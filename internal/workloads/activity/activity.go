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
			// ACT-001: twelve attempts; a few retries.
			{Golden: "act001_positive", Workflow: CallWithRetry, Args: []any{RetryInput{Activity: "Flaky", FailUntil: 12, Interval: ms(20)}}},
			{Golden: "act001_negative", Workflow: CallWithRetry, Args: []any{RetryInput{Activity: "Flaky", FailUntil: 3, Interval: ms(20)}}},
			// ACT-002: the same error on every attempt, against an error that changes.
			{Golden: "act002_positive", Workflow: CallWithRetry, Args: []any{RetryInput{Activity: "Reject", MaxAttempts: 4, Interval: ms(20)}}},
			{Golden: "act002_negative", Workflow: CallWithRetry, Args: []any{RetryInput{Activity: "Varying", MaxAttempts: 4, Interval: ms(20)}}},
			// ACT-003: unlimited attempts, against a capped policy.
			{Golden: "act003_positive", Workflow: CallWithRetry, Args: []any{RetryInput{Activity: "Flaky", FailUntil: 3, Interval: ms(20)}}},
			{Golden: "act003_negative", Workflow: CallWithRetry, Args: []any{RetryInput{Activity: "Flaky", FailUntil: 3, MaxAttempts: 5, Interval: ms(20)}}},
			// ACT-004: a 9 s Activity that heartbeats with a 1 s timeout, against a short one.
			{Golden: "act004_positive", Workflow: Heartbeating, Args: []any{9 * time.Second, time.Second}, Timeout: 2 * time.Minute},
			{Golden: "act004_negative", Workflow: Heartbeating, Args: []any{500 * time.Millisecond, time.Second}},
			// ACT-005: six tiny Activities one after another, against six in parallel.
			{Golden: "act005_positive", Workflow: TinySteps, Args: []any{6, false}},
			{Golden: "act005_negative", Workflow: TinySteps, Args: []any{6, true}},
			// ACT-006: only a Worker without Ship polls until the Activity
			// has failed; then a Worker with Ship joins.
			{Golden: "act006_positive", Workflow: ShipOrder, Drive: joinShipWorker, Timeout: 2 * time.Minute},
			{Golden: "act006_negative", Workflow: CallWithRetry, Args: []any{RetryInput{Activity: "Flaky", FailUntil: 2, Interval: ms(20)}}},
			// LA-001: a Local Activity that keeps failing past the first
			// Workflow Task, so its retries are billed; against one whose
			// retries all fit in the first Workflow Task, which aren't.
			{Golden: "la001_positive", Workflow: LocalRetry, Args: []any{8, 500 * time.Millisecond},
				Options: client.StartWorkflowOptions{WorkflowTaskTimeout: 2 * time.Second}, Timeout: 2 * time.Minute},
			{Golden: "la001_negative", Workflow: LocalRetry, Args: []any{3, 20 * time.Millisecond},
				Options: client.StartWorkflowOptions{WorkflowTaskTimeout: 2 * time.Second}},
			// LA-003: a 6 s Local Activity against a 2 s Workflow Task timeout.
			{Golden: "la003_positive", Workflow: LocalSlow, Args: []any{6 * time.Second},
				Options: client.StartWorkflowOptions{WorkflowTaskTimeout: 2 * time.Second}, Timeout: 2 * time.Minute},
			{Golden: "la003_negative", Workflow: LocalSlow, Args: []any{100 * time.Millisecond},
				Options: client.StartWorkflowOptions{WorkflowTaskTimeout: 2 * time.Second}},
		},
		Load: []workloads.LoadItem{
			{Weight: 30, Workflow: CallWithRetry, Args: []any{RetryInput{Activity: "Flaky", FailUntil: 12, Interval: ms(20)}}},
			{Weight: 20, Workflow: CallWithRetry, Args: []any{RetryInput{Activity: "Reject", MaxAttempts: 4, Interval: ms(20)}}},
			{Weight: 30, Workflow: TinySteps, Args: []any{6, false}},
			{Weight: 20, Workflow: LocalRetry, Args: []any{8, 500 * time.Millisecond},
				Options: client.StartWorkflowOptions{WorkflowTaskTimeout: 2 * time.Second}},
		},
	})
}

// startWorkers starts the group's main Worker. It registers every Workflow
// and Activity except Ship, which ACT-006's second Worker adds.
func startWorkers(c client.Client, tq string) (func(), error) {
	w := worker.New(c, tq, worker.Options{})
	w.RegisterWorkflow(CallWithRetry)
	w.RegisterWorkflow(Heartbeating)
	w.RegisterWorkflow(TinySteps)
	w.RegisterWorkflow(ShipOrder)
	w.RegisterWorkflow(LocalRetry)
	w.RegisterWorkflow(LocalSlow)
	w.RegisterActivity(Flaky)
	w.RegisterActivity(Reject)
	w.RegisterActivity(Varying)
	w.RegisterActivity(HeartbeatFor)
	w.RegisterActivity(Step)
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

// Reject always fails with the same error, like a declined card.
func Reject(context.Context, int) error {
	return temporal.NewApplicationError("card declined: invalid card number", "PaymentDeclined")
}

// Varying always fails, with a message that changes per attempt.
func Varying(ctx context.Context, _ int) error {
	return fmt.Errorf("timeout talking to upstream after %d ms", 100*activity.GetInfo(ctx).Attempt)
}

// Heartbeating runs one Activity that heartbeats every 100 ms.
func Heartbeating(ctx workflow.Context, d, heartbeatTimeout time.Duration) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: d + 30*time.Second,
		HeartbeatTimeout:    heartbeatTimeout,
	})
	return workflow.ExecuteActivity(ctx, HeartbeatFor, d).Get(ctx, nil)
}

// HeartbeatFor heartbeats every 100 ms for d.
func HeartbeatFor(ctx context.Context, d time.Duration) error {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		activity.RecordHeartbeat(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

// TinySteps runs n tiny Activities, one after another or all at once.
func TinySteps(ctx workflow.Context, n int, parallel bool) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second})
	var fs []workflow.Future
	for i := range n {
		f := workflow.ExecuteActivity(ctx, Step, i)
		if parallel {
			fs = append(fs, f)
			continue
		}
		if err := f.Get(ctx, nil); err != nil {
			return err
		}
	}
	for _, f := range fs {
		if err := f.Get(ctx, nil); err != nil {
			return err
		}
	}
	return nil
}

// Step does almost nothing.
func Step(_ context.Context, i int) (int, error) { return i + 1, nil }

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

// LocalRetry runs a Local Activity that fails until failUntil, retrying
// every interval.
func LocalRetry(ctx workflow.Context, failUntil int, interval time.Duration) error {
	ctx = workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: interval, BackoffCoefficient: 1, MaximumInterval: interval,
		},
	})
	return workflow.ExecuteLocalActivity(ctx, LocalFlaky, failUntil).Get(ctx, nil)
}

// LocalFlaky fails until failUntil.
func LocalFlaky(ctx context.Context, failUntil int) error {
	if a := int(activity.GetInfo(ctx).Attempt); a < failUntil {
		return fmt.Errorf("cache miss on attempt %d", a)
	}
	return nil
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
