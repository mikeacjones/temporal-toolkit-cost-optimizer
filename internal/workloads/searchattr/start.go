package searchattr

import (
	"time"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/workloads"
)

// Workloads for SA-001 (upsert at start) and SA-003 (upserts in consecutive
// Workflow Tasks).
func init() {
	order := Order{ID: "golden", Region: "us-east", Tier: "gold"}
	workloads.Add(workloads.Group{
		Name:             "searchattr-start",
		SearchAttributes: []string{"OrderStatus=Keyword", "Region=Keyword", "Tier=Keyword"},
		Register: func(w worker.Worker) {
			w.RegisterWorkflow(UpsertAtStart)
			w.RegisterWorkflow(UpsertAfterWork)
			w.RegisterWorkflow(VersionAtStart)
			w.RegisterWorkflow(StatusAroundQuickStep)
			w.RegisterWorkflow(StatusAroundLongWait)
			w.RegisterActivity(Charge)
		},
		Runs: []workloads.Run{
			{Golden: "sa001_positive", Workflow: UpsertAtStart, Args: []any{order}},
			{Golden: "sa001_negative", Workflow: UpsertAfterWork, Args: []any{order}},
			{Golden: "sa001_changeversion", Workflow: VersionAtStart, Args: []any{order}},
			{Golden: "sa003_positive", Workflow: StatusAroundQuickStep, Args: []any{order}},
			{Golden: "sa003_negative", Workflow: StatusAroundLongWait, Args: []any{order}},
		},
	})
}

// UpsertAtStart sets attributes in its first Workflow Task, before anything
// could change them (SA-001).
func UpsertAtStart(ctx workflow.Context, o Order) error {
	_ = workflow.UpsertTypedSearchAttributes(ctx, Region.ValueSet(o.Region), Tier.ValueSet(o.Tier))
	return workflow.ExecuteActivity(activityCtx(ctx), Charge, o).Get(ctx, nil)
}

// UpsertAfterWork sets an attribute from an Activity's outcome. Nothing to
// flag.
func UpsertAfterWork(ctx workflow.Context, o Order) error {
	if err := workflow.ExecuteActivity(activityCtx(ctx), Charge, o).Get(ctx, nil); err != nil {
		return err
	}
	return workflow.UpsertTypedSearchAttributes(ctx, OrderStatus.ValueSet("paid"))
}

// VersionAtStart only writes TemporalChangeVersion in its first Workflow
// Task, which isn't billed. Nothing to flag.
func VersionAtStart(ctx workflow.Context, o Order) error {
	workflow.GetVersion(ctx, "start-change", workflow.DefaultVersion, 1)
	return workflow.ExecuteActivity(activityCtx(ctx), Charge, o).Get(ctx, nil)
}

// StatusAroundQuickStep upserts a status, runs a quick Activity, and
// upserts again a moment later (SA-003).
func StatusAroundQuickStep(ctx workflow.Context, o Order) error {
	if err := workflow.ExecuteActivity(activityCtx(ctx), Charge, o).Get(ctx, nil); err != nil {
		return err
	}
	_ = workflow.UpsertTypedSearchAttributes(ctx, OrderStatus.ValueSet("charging"))
	if err := workflow.ExecuteActivity(activityCtx(ctx), Charge, o).Get(ctx, nil); err != nil {
		return err
	}
	return workflow.UpsertTypedSearchAttributes(ctx, OrderStatus.ValueSet("paid"))
}

// StatusAroundLongWait upserts a status that stays visible for a while
// before the next one. Nothing to flag.
func StatusAroundLongWait(ctx workflow.Context, o Order) error {
	if err := workflow.ExecuteActivity(activityCtx(ctx), Charge, o).Get(ctx, nil); err != nil {
		return err
	}
	_ = workflow.UpsertTypedSearchAttributes(ctx, OrderStatus.ValueSet("waiting"))
	if err := workflow.Sleep(ctx, 3*time.Second); err != nil {
		return err
	}
	return workflow.UpsertTypedSearchAttributes(ctx, OrderStatus.ValueSet("paid"))
}
