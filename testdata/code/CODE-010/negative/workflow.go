package orders

import (
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

var OrderStatus = temporal.NewSearchAttributeKeyKeyword("OrderStatus")

type progress struct{ Done, Total int }

func OrderWorkflow(ctx workflow.Context) error {
	p := progress{}
	// Progress isn't in a Search Attribute or memo, so a Query is the way to
	// read it.
	_ = workflow.SetQueryHandler(ctx, "progress", func() (progress, error) {
		return p, nil
	})
	return workflow.UpsertTypedSearchAttributes(ctx, OrderStatus.ValueSet("paid"))
}
