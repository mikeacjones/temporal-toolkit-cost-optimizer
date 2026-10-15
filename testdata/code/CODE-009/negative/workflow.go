package payments

import (
	"context"
	"net/http"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

func ChargeCard(ctx context.Context, url string) error {
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func ComputeTotal(ctx context.Context) (int, error) { return 42, nil }

func PaymentWorkflow(ctx workflow.Context) error {
	limited := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 5},
	})
	if err := workflow.ExecuteActivity(limited, ChargeCard, "https://psp.example").Get(ctx, nil); err != nil {
		return err
	}
	// No limit, but the Activity only computes; an outage can't stall it.
	local := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
	return workflow.ExecuteActivity(local, ComputeTotal).Get(ctx, nil)
}
