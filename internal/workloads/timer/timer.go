package timer

import (
	"context"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/workloads"
)

// SignalName is the Signal the Signal-driven workloads wait for.
const SignalName = "update"

func init() {
	workloads.Add(workloads.Group{
		Name:     "timer",
		Register: register,
		Runs: []workloads.Run{
			{Golden: "tmr001_positive", Workflow: PollingLoop},
			{Golden: "tmr001_negative", Workflow: PollOnce},
			{Golden: "tmr002_positive", Workflow: ShortSleep},
			{Golden: "tmr002_negative", Workflow: LongerSleep},
			{Golden: "tmr003_positive", Workflow: WaitWithTimeout, Drive: signalAfter(500*time.Millisecond, 1)},
			{Golden: "tmr003_negative", Workflow: WaitNoTimeout, Drive: signalAfter(500*time.Millisecond, 1)},
			{Golden: "tmr004_positive", Workflow: DebounceSignals, Drive: signalAfter(1500*time.Millisecond, 2)},
			{Golden: "tmr004_negative", Workflow: BufferSignals, Drive: signalAfter(500*time.Millisecond, 2)},
		},
		Load: []workloads.LoadItem{
			{Weight: 50, Workflow: PollingLoop},
			{Weight: 50, Workflow: ShortSleep},
		},
	})
}

func register(w worker.Worker) {
	for _, wf := range []any{PollingLoop, PollOnce, ShortSleep, LongerSleep, WaitWithTimeout, WaitNoTimeout, DebounceSignals, BufferSignals} {
		w.RegisterWorkflow(wf)
	}
	w.RegisterActivity(CheckStatus)
	w.RegisterActivity(Apply)
}

// signalAfter sends n Signals, one every gap.
func signalAfter(gap time.Duration, n int) func(context.Context, client.Client, client.WorkflowRun) error {
	return func(ctx context.Context, c client.Client, run client.WorkflowRun) error {
		for range n {
			time.Sleep(gap)
			if err := c.SignalWorkflow(ctx, run.GetID(), run.GetRunID(), SignalName, "change"); err != nil {
				return err
			}
		}
		return nil
	}
}

func actx(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second})
}

// PollingLoop sleeps, then checks an external status, three times (TMR-001).
func PollingLoop(ctx workflow.Context) error {
	for range 3 {
		if err := workflow.Sleep(ctx, time.Second); err != nil {
			return err
		}
		if err := workflow.ExecuteActivity(actx(ctx), CheckStatus).Get(ctx, nil); err != nil {
			return err
		}
	}
	return nil
}

// PollOnce waits once, then checks once. Nothing to flag.
func PollOnce(ctx workflow.Context) error {
	if err := workflow.Sleep(ctx, time.Second); err != nil {
		return err
	}
	return workflow.ExecuteActivity(actx(ctx), CheckStatus).Get(ctx, nil)
}

// ShortSleep starts a 200 ms Timer (TMR-002).
func ShortSleep(ctx workflow.Context) error { return workflow.Sleep(ctx, 200*time.Millisecond) }

// LongerSleep starts a Timer of a second and a half. Nothing to flag.
func LongerSleep(ctx workflow.Context) error { return workflow.Sleep(ctx, 1500*time.Millisecond) }

func onSignal(ctx workflow.Context, got *int) {
	ch := workflow.GetSignalChannel(ctx, SignalName)
	workflow.Go(ctx, func(ctx workflow.Context) {
		for {
			var s string
			ch.Receive(ctx, &s)
			*got++
		}
	})
}

// WaitWithTimeout waits for a Signal with a timeout it never needs, so the
// SDK starts a Timer and then cancels it, then carries on (TMR-003). If the
// Workflow completed in the same Workflow Task, the SDK would drop the
// cancel command, so it runs an Activity after the wait.
func WaitWithTimeout(ctx workflow.Context) error {
	got := 0
	onSignal(ctx, &got)
	if _, err := workflow.AwaitWithTimeout(ctx, 30*time.Second, func() bool { return got > 0 }); err != nil {
		return err
	}
	return workflow.ExecuteActivity(actx(ctx), Apply, "after wait").Get(ctx, nil)
}

// WaitNoTimeout waits for the same Signal without a timeout. Nothing to
// flag.
func WaitNoTimeout(ctx workflow.Context) error {
	got := 0
	onSignal(ctx, &got)
	if err := workflow.Await(ctx, func() bool { return got > 0 }); err != nil {
		return err
	}
	return workflow.ExecuteActivity(actx(ctx), Apply, "after wait").Get(ctx, nil)
}

// DebounceSignals starts a Timer after each Signal before acting on it
// (TMR-004).
func DebounceSignals(ctx workflow.Context) error {
	ch := workflow.GetSignalChannel(ctx, SignalName)
	for range 2 {
		var s string
		ch.Receive(ctx, &s)
		if err := workflow.Sleep(ctx, time.Second); err != nil {
			return err
		}
		if err := workflow.ExecuteActivity(actx(ctx), Apply, s).Get(ctx, nil); err != nil {
			return err
		}
	}
	return nil
}

// BufferSignals acts on each Signal as it arrives. Nothing to flag.
func BufferSignals(ctx workflow.Context) error {
	ch := workflow.GetSignalChannel(ctx, SignalName)
	for range 2 {
		var s string
		ch.Receive(ctx, &s)
		if err := workflow.ExecuteActivity(actx(ctx), Apply, s).Get(ctx, nil); err != nil {
			return err
		}
	}
	return nil
}

// CheckStatus stands in for a call to an external system.
func CheckStatus(context.Context) (string, error) { return "pending", nil }

// Apply stands in for acting on a change.
func Apply(context.Context, string) error { return nil }
