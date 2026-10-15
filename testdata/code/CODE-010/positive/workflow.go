package orders

import (
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

var OrderStatus = temporal.NewSearchAttributeKeyKeyword("OrderStatus")

func OrderWorkflow(ctx workflow.Context) error {
	status := "new"
	_ = workflow.SetQueryHandler(ctx, "status", func() (string, error) { // want CODE-010
		return status, nil
	})
	status = "paid"
	return workflow.UpsertTypedSearchAttributes(ctx, OrderStatus.ValueSet(status))
}
