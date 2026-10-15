package storage

import (
	"context"
	"strings"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/workloads"
)

// SignalName is the Signal DoneButWaiting and RaceAndReturn wait for.
const SignalName = "done"

func init() {
	workloads.Add(workloads.Group{
		Name:    "storage",
		Workers: workers,
		Runs: []workloads.Run{
			{Golden: "sto001_positive", Workflow: LargeResult},
			{Golden: "sto001_negative", Workflow: SmallResult},
			{Golden: "sto003_positive", Workflow: GrowingHistory, LeaveOpen: true, Settle: 5 * time.Second},
			{Golden: "sto003_negative", Workflow: SmallOpen, LeaveOpen: true, Settle: 2 * time.Second},
			{Golden: "sto004_positive", Workflow: LongWaitAfterWork, LeaveOpen: true, Settle: 5 * time.Second},
			{Golden: "sto004_negative", Workflow: SmallLongWait, LeaveOpen: true, Settle: 2 * time.Second},
			{Golden: "sto005_positive", Workflow: DoneButWaiting, Drive: signal, LeaveOpen: true, Settle: 3 * time.Second},
			{Golden: "sto005_negative", Workflow: RaceAndReturn, Drive: signal},
			{Golden: "sto006_positive", Workflow: ManyLocalActivities},
			{Golden: "sto006_negative", Workflow: OneSmallLocalActivity},
			{Golden: "sto007_positive", Workflow: LargeHeaderWorkflow},
			{Golden: "sto007_negative", Workflow: NoHeaderWorkflow},
		},
		Load: []workloads.LoadItem{
			{Weight: 50, Workflow: LargeResult},
			{Weight: 50, Workflow: LargeHeaderWorkflow},
		},
	})
}

// workers starts a Worker with an interceptor that adds a large tracing
// header to Activity calls from LargeHeaderWorkflow only.
func workers(c client.Client, taskQueue string) (func(), error) {
	w := worker.New(c, taskQueue, worker.Options{Interceptors: []interceptor.WorkerInterceptor{&headerInterceptor{}}})
	for _, wf := range []any{LargeResult, SmallResult, GrowingHistory, SmallOpen, LongWaitAfterWork, SmallLongWait,
		DoneButWaiting, RaceAndReturn, ManyLocalActivities, OneSmallLocalActivity, LargeHeaderWorkflow, NoHeaderWorkflow} {
		w.RegisterWorkflow(wf)
	}
	w.RegisterActivity(Fetch)
	if err := w.Start(); err != nil {
		return nil, err
	}
	return w.Stop, nil
}

func signal(ctx context.Context, c client.Client, run client.WorkflowRun) error {
	time.Sleep(500 * time.Millisecond)
	return c.SignalWorkflow(ctx, run.GetID(), run.GetRunID(), SignalName, nil)
}

func actx(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second})
}

func fetchN(ctx workflow.Context, n, size int) error {
	for range n {
		if err := workflow.ExecuteActivity(actx(ctx), Fetch, size).Get(ctx, nil); err != nil {
			return err
		}
	}
	return nil
}

func waitForever(ctx workflow.Context) error {
	workflow.GetSignalChannel(ctx, "never").Receive(ctx, nil)
	return nil
}

// LargeResult returns a 300 KiB Activity result (STO-001).
func LargeResult(ctx workflow.Context) error { return fetchN(ctx, 1, 300<<10) }

// SmallResult returns a small one. Nothing to flag.
func SmallResult(ctx workflow.Context) error { return fetchN(ctx, 1, 1<<10) }

// GrowingHistory builds about 400 KiB of history and stays open (STO-003).
func GrowingHistory(ctx workflow.Context) error {
	if err := fetchN(ctx, 20, 20<<10); err != nil {
		return err
	}
	return waitForever(ctx)
}

// SmallOpen stays open with a small history. Nothing to flag.
func SmallOpen(ctx workflow.Context) error {
	if err := fetchN(ctx, 1, 1<<10); err != nil {
		return err
	}
	return waitForever(ctx)
}

// LongWaitAfterWork builds about 400 KiB of history, then sleeps a day
// (STO-004).
func LongWaitAfterWork(ctx workflow.Context) error {
	if err := fetchN(ctx, 20, 20<<10); err != nil {
		return err
	}
	return workflow.Sleep(ctx, 24*time.Hour)
}

// SmallLongWait sleeps a day with a small history behind it. Nothing to
// flag.
func SmallLongWait(ctx workflow.Context) error {
	if err := fetchN(ctx, 1, 1<<10); err != nil {
		return err
	}
	return workflow.Sleep(ctx, 24*time.Hour)
}

// DoneButWaiting starts a day-long Timer, does its work when the Signal
// arrives, and then still waits for the Timer (STO-005).
func DoneButWaiting(ctx workflow.Context) error {
	deadline := workflow.NewTimer(ctx, 24*time.Hour)
	workflow.GetSignalChannel(ctx, SignalName).Receive(ctx, nil)
	if err := fetchN(ctx, 1, 1<<10); err != nil {
		return err
	}
	return deadline.Get(ctx, nil)
}

// RaceAndReturn races the same Timer against the Signal, cancels the Timer
// when the Signal wins, and returns. Nothing to flag.
func RaceAndReturn(ctx workflow.Context) error {
	tctx, cancel := workflow.WithCancel(ctx)
	deadline := workflow.NewTimer(tctx, 24*time.Hour)
	done := false
	sel := workflow.NewSelector(ctx)
	sel.AddFuture(deadline, func(workflow.Future) {})
	sel.AddReceive(workflow.GetSignalChannel(ctx, SignalName), func(c workflow.ReceiveChannel, _ bool) {
		c.Receive(ctx, nil)
		done = true
	})
	sel.Select(ctx)
	if done {
		cancel()
		return fetchN(ctx, 1, 1<<10)
	}
	return nil
}

func lactx(ctx workflow.Context) workflow.Context {
	return workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{StartToCloseTimeout: 5 * time.Second})
}

// ManyLocalActivities records ten Local Activities with 5 KiB results, so
// markers dominate the history (STO-006). This shows an existing pattern;
// tcopt never suggests Local Activities to save cost.
func ManyLocalActivities(ctx workflow.Context) error {
	for range 10 {
		if err := workflow.ExecuteLocalActivity(lactx(ctx), Fetch, 5<<10).Get(ctx, nil); err != nil {
			return err
		}
	}
	return nil
}

// OneSmallLocalActivity records one small marker. Nothing to flag.
func OneSmallLocalActivity(ctx workflow.Context) error {
	return workflow.ExecuteLocalActivity(lactx(ctx), Fetch, 16).Get(ctx, nil)
}

// LargeHeaderWorkflow runs four Activities; the interceptor gives each the
// same 2 KiB header (STO-007).
func LargeHeaderWorkflow(ctx workflow.Context) error { return fetchN(ctx, 4, 64) }

// NoHeaderWorkflow runs the same Activities without it. Nothing to flag.
func NoHeaderWorkflow(ctx workflow.Context) error { return fetchN(ctx, 4, 64) }

// Fetch returns a string of the given size.
func Fetch(_ context.Context, size int) (string, error) { return strings.Repeat("x", size), nil }

type headerInterceptor struct {
	interceptor.WorkerInterceptorBase
}

func (*headerInterceptor) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &headerInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}}
}

type headerInbound struct {
	interceptor.WorkflowInboundInterceptorBase
}

func (h *headerInbound) Init(out interceptor.WorkflowOutboundInterceptor) error {
	return h.Next.Init(&headerOutbound{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: out}})
}

type headerOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
}

var bigHeader, _ = converter.GetDefaultDataConverter().ToPayload(strings.Repeat("t", 2<<10))

func (o *headerOutbound) ExecuteActivity(ctx workflow.Context, activityType string, args ...any) workflow.Future {
	if workflow.GetInfo(ctx).WorkflowType.Name == "LargeHeaderWorkflow" {
		if h := interceptor.WorkflowHeader(ctx); h != nil {
			h["trace-baggage"] = bigHeader
		}
	}
	return o.Next.ExecuteActivity(ctx, activityType, args...)
}
