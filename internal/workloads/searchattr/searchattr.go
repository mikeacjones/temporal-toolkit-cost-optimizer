// Package searchattr holds the workloads for the Search Attribute detectors.
package searchattr

import (
	"context"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/workloads"
)

// Search Attributes the workloads set. The dev server must register them.
var (
	OrderStatus = temporal.NewSearchAttributeKeyKeyword("OrderStatus")
	Region      = temporal.NewSearchAttributeKeyKeyword("Region")
	Tier        = temporal.NewSearchAttributeKeyKeyword("Tier")
)

// Order is the input to every workload.
type Order struct {
	ID     string
	Region string
	Tier   string
}

func init() {
	order := Order{ID: "golden", Region: "us-east", Tier: "gold"}
	load := Order{ID: "load", Region: "eu-west", Tier: "silver"}
	startSA := client.StartWorkflowOptions{TypedSearchAttributes: StartSearchAttributes()}
	workloads.Add(workloads.Group{
		Name:             "searchattr",
		SearchAttributes: []string{"OrderStatus=Keyword", "Region=Keyword", "Tier=Keyword"},
		Register:         register,
		Runs: []workloads.Run{
			{Golden: "sa002_positive", Workflow: SeparateUpserts, Args: []any{order}},
			{Golden: "sa002_negative", Workflow: CombinedUpsert, Args: []any{order}},
			{Golden: "sa_changeversion", Workflow: VersionedUpsert, Args: []any{order}},
			{Golden: "sa004_positive", Workflow: RepeatedStatus, Args: []any{order}, Options: startSA},
			{Golden: "sa004_negative", Workflow: ChangingStatus, Args: []any{order}},
		},
		Load: []workloads.LoadItem{
			{Weight: 60, Workflow: SeparateUpserts, Args: []any{load}},
			{Weight: 20, Workflow: RepeatedStatus, Args: []any{load}, Options: startSA},
			{Weight: 10, Workflow: ChangingStatus, Args: []any{load}},
			{Weight: 10, Workflow: CombinedUpsert, Args: []any{load}},
		},
	})
}

func register(w worker.Worker) {
	w.RegisterWorkflow(SeparateUpserts)
	w.RegisterWorkflow(CombinedUpsert)
	w.RegisterWorkflow(VersionedUpsert)
	w.RegisterWorkflow(RepeatedStatus)
	w.RegisterWorkflow(ChangingStatus)
	w.RegisterActivity(Charge)
}

func activityCtx(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second})
}

// SeparateUpserts sends three upserts from one Workflow Task (SA-002).
func SeparateUpserts(ctx workflow.Context, o Order) error {
	if err := workflow.ExecuteActivity(activityCtx(ctx), Charge, o).Get(ctx, nil); err != nil {
		return err
	}
	_ = workflow.UpsertTypedSearchAttributes(ctx, OrderStatus.ValueSet("paid"))
	_ = workflow.UpsertTypedSearchAttributes(ctx, Region.ValueSet(o.Region))
	_ = workflow.UpsertTypedSearchAttributes(ctx, Tier.ValueSet(o.Tier))
	return nil
}

// CombinedUpsert sets the same attributes in one upsert. Nothing to flag.
func CombinedUpsert(ctx workflow.Context, o Order) error {
	if err := workflow.ExecuteActivity(activityCtx(ctx), Charge, o).Get(ctx, nil); err != nil {
		return err
	}
	return workflow.UpsertTypedSearchAttributes(ctx,
		OrderStatus.ValueSet("paid"), Region.ValueSet(o.Region), Tier.ValueSet(o.Tier))
}

// VersionedUpsert calls GetVersion and then upserts once in the same Workflow
// Task. The TemporalChangeVersion upsert isn't billed, so nothing to flag.
func VersionedUpsert(ctx workflow.Context, o Order) error {
	if err := workflow.ExecuteActivity(activityCtx(ctx), Charge, o).Get(ctx, nil); err != nil {
		return err
	}
	v := workflow.GetVersion(ctx, "status-change", workflow.DefaultVersion, 1)
	status := "paid"
	if v == 1 {
		status = "settled"
	}
	return workflow.UpsertTypedSearchAttributes(ctx, OrderStatus.ValueSet(status))
}

// RepeatedStatus upserts a status it already set at start (SA-004), then
// upserts a new one.
func RepeatedStatus(ctx workflow.Context, o Order) error {
	if err := workflow.ExecuteActivity(activityCtx(ctx), Charge, o).Get(ctx, nil); err != nil {
		return err
	}
	_ = workflow.UpsertTypedSearchAttributes(ctx, OrderStatus.ValueSet("pending"))
	if err := workflow.ExecuteActivity(activityCtx(ctx), Charge, o).Get(ctx, nil); err != nil {
		return err
	}
	return workflow.UpsertTypedSearchAttributes(ctx, OrderStatus.ValueSet("paid"))
}

// ChangingStatus upserts a different status after each Activity. Nothing to
// flag.
func ChangingStatus(ctx workflow.Context, o Order) error {
	for _, s := range []string{"charging", "paid"} {
		if err := workflow.ExecuteActivity(activityCtx(ctx), Charge, o).Get(ctx, nil); err != nil {
			return err
		}
		_ = workflow.UpsertTypedSearchAttributes(ctx, OrderStatus.ValueSet(s))
	}
	return nil
}

// Charge stands in for a payment call.
func Charge(_ context.Context, _ Order) error { return nil }

// StartSearchAttributes are the attributes set in the start request for
// RepeatedStatus, so its first upsert repeats a value.
func StartSearchAttributes() temporal.SearchAttributes {
	return temporal.NewSearchAttributes(OrderStatus.ValueSet("pending"))
}
