package sync

import (
	"context"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
)

func SyncWorkflow(ctx workflow.Context) error {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		HeartbeatTimeout:    2 * time.Second, // want CODE-003
	}
	ctx = workflow.WithActivityOptions(ctx, ao)
	return workflow.ExecuteActivity(ctx, SyncOne).Get(ctx, nil)
}

func SyncOne(ctx context.Context) error {
	activity.RecordHeartbeat(ctx, "started") // want CODE-003
	return nil
}
