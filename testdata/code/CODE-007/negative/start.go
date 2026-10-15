package api

import (
	"context"

	"go.temporal.io/sdk/client"
)

func startOne(ctx context.Context, c client.Client, id string) error {
	_, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: "orders"}, "OrderWorkflow")
	return err
}

// Signaling a different Workflow after a start is fine.
func startAndNotify(ctx context.Context, c client.Client, id, parent string) error {
	if _, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: "orders"}, "OrderWorkflow"); err != nil {
		return err
	}
	return c.SignalWorkflow(ctx, parent, "", "childStarted", id)
}
