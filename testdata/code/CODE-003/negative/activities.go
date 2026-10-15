package sync

import (
	"context"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

func SyncWorkflow(ctx workflow.Context) error {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: time.Hour,
		HeartbeatTimeout:    time.Minute,
	}
	ctx = workflow.WithActivityOptions(ctx, ao)
	return workflow.ExecuteActivity(ctx, SyncAll).Get(ctx, nil)
}

func SyncAll(ctx context.Context, items []string) error {
	for i := range items {
		// Heartbeating as work progresses is what Heartbeats are for.
		activity.RecordHeartbeat(ctx, i)
	}
	return nil
}
