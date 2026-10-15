package shape

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nexus-rpc/sdk-go/nexus"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	nexuspb "go.temporal.io/api/nexus/v1"
	"go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporalnexus"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/workloads"
)

const (
	nexusEndpoint = "tcopt-workloads-shape"
	nexusService  = "shape"
	// handlerIDPrefix names the Workflows the Nexus Operations start, so the
	// caller's Drive can find them and record their histories.
	handlerIDPrefix = "nexus-handler-"
	// goldenDir is where genhistories writes, relative to the repo root it
	// runs from. Drive uses it to record Nexus handler histories, which
	// genhistories doesn't follow on its own.
	goldenDir = "testdata/histories"
)

var (
	thinOp = temporalnexus.NewWorkflowRunOperation("thin", HandlerOneActivity,
		func(_ context.Context, id string, _ nexus.StartOperationOptions) (client.StartWorkflowOptions, error) {
			return client.StartWorkflowOptions{ID: handlerIDPrefix + id}, nil
		})
	richOp = temporalnexus.NewWorkflowRunOperation("rich", HandlerTwoActivities,
		func(_ context.Context, id string, _ nexus.StartOperationOptions) (client.StartWorkflowOptions, error) {
			return client.StartWorkflowOptions{ID: handlerIDPrefix + id}, nil
		})
)

func init() {
	run := func(golden string, wf any, args ...any) workloads.Run {
		return workloads.Run{Golden: golden, Workflow: wf, Args: args}
	}
	workloads.Add(workloads.Group{
		Name:    "shape",
		Workers: startWorkers,
		Runs: []workloads.Run{
			run("wf001_positive", OneActivity),
			run("wf001_negative", TwoActivities),
			run("wf002_positive", DelayThenActivity),
			run("wf002_negative", DelayThenTwoActivities),
			run("tmr005_positive", DelayThenTwoActivities),
			{Golden: "tmr005_negative", Workflow: WaitForSignalOrTimer, Drive: sendSignals("go", 1, 0, 300*time.Millisecond)},
			{Golden: "chd001_positive", Workflow: ParentOfThinChild, Children: true},
			{Golden: "chd001_negative", Workflow: ParentOfRichChild, Children: true},
			{Golden: "nex002_positive", Workflow: CallsOneNexusOperation, Args: []any{"thin"}, Drive: recordHandler("nex001_positive")},
			{Golden: "nex002_negative", Workflow: CallsNexusThenActivity, Args: []any{"rich"}, Drive: recordHandler("nex001_negative")},
			{Golden: "sig001_positive", Workflow: CollectSignals, Args: []any{5}, Drive: sendSignals("item", 5, 0, 0)},
			{Golden: "sig001_negative", Workflow: CollectSignals, Args: []any{3}, Drive: sendSignals("item", 3, 1200*time.Millisecond, 0), Timeout: 2 * time.Minute},
			{Golden: "sig002_positive", Workflow: WaitForSignal, Drive: sendSignals("go", 1, 0, 0)},
			{Golden: "sig002_negative", Workflow: WaitForSignal, Drive: lateSignalAndSignalWithStart("sig002_signalwithstart")},
			// genhistories records the last run of a Continue-As-New chain;
			// Drive records the first, which is the one that continued.
			{Golden: "can001_end", Workflow: ContinueAfterOneActivity, Args: []any{3}, Drive: recordFirstRun("can001_positive")},
			{Golden: "can001_large_end", Workflow: ContinueAfterManyActivities, Args: []any{2}, Drive: recordFirstRun("can001_negative"), Timeout: 2 * time.Minute},
		},
		Load: []workloads.LoadItem{
			{Weight: 40, Workflow: OneActivity},
			{Weight: 20, Workflow: DelayThenActivity},
			{Weight: 20, Workflow: ParentOfThinChild},
			{Weight: 20, Workflow: TwoActivities},
		},
	})
}

// startWorkers runs one Worker with every workload and the Nexus service,
// and creates the Nexus endpoint that targets it.
func startWorkers(c client.Client, taskQueue string) (func(), error) {
	ctx := context.Background()
	_, err := c.OperatorService().CreateNexusEndpoint(ctx, &operatorservice.CreateNexusEndpointRequest{
		Spec: &nexuspb.EndpointSpec{
			Name: nexusEndpoint,
			Target: &nexuspb.EndpointTarget{Variant: &nexuspb.EndpointTarget_Worker_{
				Worker: &nexuspb.EndpointTarget_Worker{Namespace: "default", TaskQueue: taskQueue},
			}},
		},
	})
	if _, exists := err.(*serviceerror.AlreadyExists); err != nil && !exists {
		return nil, fmt.Errorf("create Nexus endpoint: %w", err)
	}

	w := worker.New(c, taskQueue, worker.Options{})
	for _, wf := range []any{
		OneActivity, TwoActivities, DelayThenActivity, DelayThenTwoActivities, WaitForSignalOrTimer,
		ParentOfThinChild, ParentOfRichChild, ThinChild, RichChild,
		CallsOneNexusOperation, CallsNexusThenActivity, HandlerOneActivity, HandlerTwoActivities,
		CollectSignals, WaitForSignal, ContinueAfterOneActivity, ContinueAfterManyActivities,
	} {
		w.RegisterWorkflow(wf)
	}
	w.RegisterActivity(Step)
	svc := nexus.NewService(nexusService)
	if err := svc.Register(thinOp, richOp); err != nil {
		return nil, err
	}
	w.RegisterNexusService(svc)
	if err := w.Start(); err != nil {
		return nil, err
	}
	return w.Stop, nil
}

func act(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second})
}

// Step stands in for one call to an external system.
func Step(_ context.Context, name string) (string, error) { return "done " + name, nil }

// OneActivity only wraps one Activity (WF-001).
func OneActivity(ctx workflow.Context) (string, error) {
	var out string
	err := workflow.ExecuteActivity(act(ctx), Step, "one").Get(ctx, &out)
	return out, err
}

// TwoActivities runs two Activities in sequence. Nothing to flag.
func TwoActivities(ctx workflow.Context) error {
	for _, s := range []string{"a", "b"} {
		if err := workflow.ExecuteActivity(act(ctx), Step, s).Get(ctx, nil); err != nil {
			return err
		}
	}
	return nil
}

// DelayThenActivity waits on a Timer, then runs one Activity (WF-002).
func DelayThenActivity(ctx workflow.Context) error {
	if err := workflow.Sleep(ctx, time.Second); err != nil {
		return err
	}
	return workflow.ExecuteActivity(act(ctx), Step, "later").Get(ctx, nil)
}

// DelayThenTwoActivities starts with a Timer, then does real work
// (TMR-005).
func DelayThenTwoActivities(ctx workflow.Context) error {
	if err := workflow.Sleep(ctx, time.Second); err != nil {
		return err
	}
	return TwoActivities(ctx)
}

// WaitForSignalOrTimer races a Signal against a Timer as its first command.
// The Signal wins and the Timer is canceled: a wait, not a delay.
func WaitForSignalOrTimer(ctx workflow.Context) error {
	tctx, cancel := workflow.WithCancel(ctx)
	timer := workflow.NewTimer(tctx, time.Minute)
	ch := workflow.GetSignalChannel(ctx, "go")
	sel := workflow.NewSelector(ctx)
	sel.AddFuture(timer, func(workflow.Future) {})
	sel.AddReceive(ch, func(c workflow.ReceiveChannel, _ bool) { c.Receive(ctx, nil); cancel() })
	sel.Select(ctx)
	return TwoActivities(ctx)
}

// ThinChild only wraps one Activity.
func ThinChild(ctx workflow.Context) (string, error) { return OneActivity(ctx) }

// RichChild does more than one thing.
func RichChild(ctx workflow.Context) error { return TwoActivities(ctx) }

// ParentOfThinChild runs an Activity through a Child Workflow (CHD-001).
func ParentOfThinChild(ctx workflow.Context) error {
	if err := workflow.ExecuteActivity(act(ctx), Step, "parent").Get(ctx, nil); err != nil {
		return err
	}
	return workflow.ExecuteChildWorkflow(ctx, ThinChild).Get(ctx, nil)
}

// ParentOfRichChild runs a child that does real work. Nothing to flag.
func ParentOfRichChild(ctx workflow.Context) error {
	if err := workflow.ExecuteActivity(act(ctx), Step, "parent").Get(ctx, nil); err != nil {
		return err
	}
	return workflow.ExecuteChildWorkflow(ctx, RichChild).Get(ctx, nil)
}

func nexusCall(ctx workflow.Context, op string) error {
	nc := workflow.NewNexusClient(nexusEndpoint, nexusService)
	id := workflow.GetInfo(ctx).WorkflowExecution.ID
	return nc.ExecuteOperation(ctx, op, id, workflow.NexusOperationOptions{ScheduleToCloseTimeout: time.Minute}).Get(ctx, nil)
}

// CallsOneNexusOperation only calls one Nexus Operation (NEX-002).
func CallsOneNexusOperation(ctx workflow.Context, op string) error { return nexusCall(ctx, op) }

// CallsNexusThenActivity calls a Nexus Operation and runs an Activity.
// Nothing to flag.
func CallsNexusThenActivity(ctx workflow.Context, op string) error {
	if err := nexusCall(ctx, op); err != nil {
		return err
	}
	return workflow.ExecuteActivity(act(ctx), Step, "after").Get(ctx, nil)
}

// HandlerOneActivity backs a Nexus Operation and only runs one Activity
// (NEX-001).
func HandlerOneActivity(ctx workflow.Context, _ string) (string, error) { return OneActivity(ctx) }

// HandlerTwoActivities backs a Nexus Operation with real work.
func HandlerTwoActivities(ctx workflow.Context, _ string) (string, error) {
	return "", TwoActivities(ctx)
}

// CollectSignals waits for n "item" Signals.
func CollectSignals(ctx workflow.Context, n int) error {
	ch := workflow.GetSignalChannel(ctx, "item")
	for range n {
		ch.Receive(ctx, nil)
	}
	return workflow.ExecuteActivity(act(ctx), Step, "batch").Get(ctx, nil)
}

// WaitForSignal waits for one "go" Signal, then runs an Activity.
func WaitForSignal(ctx workflow.Context) error {
	workflow.GetSignalChannel(ctx, "go").Receive(ctx, nil)
	return workflow.ExecuteActivity(act(ctx), Step, "go").Get(ctx, nil)
}

// ContinueAfterOneActivity continues as new after every Activity, left
// times (CAN-001).
func ContinueAfterOneActivity(ctx workflow.Context, left int) error {
	if err := workflow.ExecuteActivity(act(ctx), Step, "tick").Get(ctx, nil); err != nil {
		return err
	}
	if left <= 1 {
		return nil
	}
	return workflow.NewContinueAsNewError(ctx, ContinueAfterOneActivity, left-1)
}

// ContinueAfterManyActivities continues as new after 40 Activities, which
// makes a run large enough to be worth its start.
func ContinueAfterManyActivities(ctx workflow.Context, left int) error {
	for i := range 40 {
		if err := workflow.ExecuteActivity(act(ctx), Step, fmt.Sprint(i)).Get(ctx, nil); err != nil {
			return err
		}
	}
	if left <= 1 {
		return nil
	}
	return workflow.NewContinueAsNewError(ctx, ContinueAfterManyActivities, left-1)
}

// sendSignals waits for delay, then sends n Signals spaced gap apart.
func sendSignals(name string, n int, gap, delay time.Duration) func(context.Context, client.Client, client.WorkflowRun) error {
	return func(ctx context.Context, c client.Client, run client.WorkflowRun) error {
		time.Sleep(delay)
		for i := range n {
			if i > 0 {
				time.Sleep(gap)
			}
			if err := c.SignalWorkflow(ctx, run.GetID(), run.GetRunID(), name, i); err != nil {
				return err
			}
		}
		return nil
	}
}

// lateSignalAndSignalWithStart signals the started run 2 s after start, too
// late to count as start-then-signal. It also starts a second execution with
// Signal-With-Start and records that one as golden.
func lateSignalAndSignalWithStart(golden string) func(context.Context, client.Client, client.WorkflowRun) error {
	return func(ctx context.Context, c client.Client, run client.WorkflowRun) error {
		if err := sendSignals("go", 1, 0, 2*time.Second)(ctx, c, run); err != nil {
			return err
		}
		wr, err := c.SignalWithStartWorkflow(ctx, run.GetID()+"-sws", "go", 0,
			client.StartWorkflowOptions{TaskQueue: "tcopt-workloads-shape"}, WaitForSignal)
		if err != nil {
			return err
		}
		if err := wr.Get(ctx, nil); err != nil {
			return err
		}
		h, err := fetch(ctx, c, wr.GetID(), wr.GetRunID())
		if err != nil {
			return err
		}
		return write(golden, h)
	}
}

// recordHandler waits for the caller to finish, then records the history of
// the Workflow its Nexus Operation started.
func recordHandler(golden string) func(context.Context, client.Client, client.WorkflowRun) error {
	return func(ctx context.Context, c client.Client, run client.WorkflowRun) error {
		if err := run.Get(ctx, nil); err != nil {
			return err
		}
		h, err := fetch(ctx, c, handlerIDPrefix+run.GetID(), "")
		if err != nil {
			return err
		}
		return write(golden, h)
	}
}

// recordFirstRun waits for a Continue-As-New chain to finish, then records
// its first run.
func recordFirstRun(golden string) func(context.Context, client.Client, client.WorkflowRun) error {
	return func(ctx context.Context, c client.Client, run client.WorkflowRun) error {
		first := run.GetRunID()
		if err := run.Get(ctx, nil); err != nil {
			return err
		}
		h, err := fetch(ctx, c, run.GetID(), first)
		if err != nil {
			return err
		}
		return write(golden, h)
	}
}

func fetch(ctx context.Context, c client.Client, id, runID string) (*historypb.History, error) {
	h := &historypb.History{}
	iter := c.GetWorkflowHistory(ctx, id, runID, false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for iter.HasNext() {
		ev, err := iter.Next()
		if err != nil {
			return nil, err
		}
		h.Events = append(h.Events, ev)
	}
	return h, nil
}

func write(golden string, h *historypb.History) error {
	b, err := protojson.MarshalOptions{Indent: "  "}.Marshal(h)
	if err != nil {
		return err
	}
	path := filepath.Join(goldenDir, golden+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", path)
	return nil
}
