package orders

import (
	"time"

	"go.temporal.io/sdk/workflow"
)

type state struct{ ready bool }

func (s *state) waitReady(ctx workflow.Context) (bool, error) {
	return workflow.AwaitWithTimeout(ctx, 30*time.Second, func() bool { return s.ready }) // want CODE-002
}

func OrderWorkflow(ctx workflow.Context) error {
	s := &state{}
	_ = workflow.SetUpdateHandler(ctx, "approve", func(ctx workflow.Context) error {
		_, err := workflow.AwaitWithTimeout(ctx, time.Minute, func() bool { return s.ready }) // want CODE-002
		return err
	})
	_ = workflow.SetUpdateHandler(ctx, "wait", s.waitReady)
	_ = workflow.SetUpdateHandlerWithOptions(ctx, "poll", pollHandler, workflow.UpdateHandlerOptions{})

	sel := workflow.NewSelector(ctx)
	sel.AddReceive(workflow.GetSignalChannel(ctx, "go"), func(c workflow.ReceiveChannel, more bool) {
		c.Receive(ctx, nil)
		_, _ = workflow.AwaitWithOptions(ctx, workflow.AwaitOptions{Timeout: time.Second}, func() bool { return s.ready }) // want CODE-002
	})
	sel.Select(ctx)
	return nil
}

func pollHandler(ctx workflow.Context) error {
	_, err := workflow.AwaitWithTimeout(ctx, time.Second, func() bool { return true }) // want CODE-002
	return err
}
