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

func PaymentWorkflow(ctx workflow.Context) error {
	ao := workflow.ActivityOptions{StartToCloseTimeout: time.Minute}
	actx := workflow.WithActivityOptions(ctx, ao)
	if err := workflow.ExecuteActivity(actx, ChargeCard, "https://psp.example").Get(ctx, nil); err != nil { // want CODE-009
		return err
	}
	unlimited := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second},
	})
	return workflow.ExecuteActivity(unlimited, "NotifyWebhook").Get(ctx, nil) // want CODE-009
}
