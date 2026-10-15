package api

import (
	"context"

	"go.temporal.io/sdk/client"
)

func startAndSignal(ctx context.Context, c client.Client, orderID string) error {
	opts := client.StartWorkflowOptions{ID: "order-" + orderID, TaskQueue: "orders"}
	if _, err := c.ExecuteWorkflow(ctx, opts, "OrderWorkflow"); err != nil {
		return err
	}
	return c.SignalWorkflow(ctx, "order-"+orderID, "", "paid", nil) // want CODE-007
}

func startAndUpdate(ctx context.Context, c client.Client) error {
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{TaskQueue: "carts"}, "CartWorkflow")
	if err != nil {
		return err
	}
	_, err = c.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{ // want CODE-007
		WorkflowID:   run.GetID(),
		UpdateName:   "addItem",
		WaitForStage: client.WorkflowUpdateStageCompleted,
	})
	return err
}
