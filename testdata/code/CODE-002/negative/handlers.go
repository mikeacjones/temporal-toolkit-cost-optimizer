package orders

import (
	"time"

	"go.temporal.io/sdk/workflow"
)

func OrderWorkflow(ctx workflow.Context) error {
	ready := false
	// A wait without a timeout in a handler starts no Timer.
	_ = workflow.SetUpdateHandler(ctx, "approve", func(ctx workflow.Context) error {
		return workflow.Await(ctx, func() bool { return ready })
	})
	// A wait with a timeout in the main Workflow body is a deliberate Timer.
	_, err := workflow.AwaitWithTimeout(ctx, time.Hour, func() bool { return ready })
	return err
}
