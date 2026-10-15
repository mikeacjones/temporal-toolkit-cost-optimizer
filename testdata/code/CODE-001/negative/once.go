package dashboard

import (
	"context"

	"go.temporal.io/sdk/client"
)

// A one-off Query from a CLI command is fine.
func printStatus(ctx context.Context, c client.Client, id string) {
	_, _ = c.QueryWorkflow(ctx, id, "", "status")
}

// Describe in a loop isn't an Action.
func pollDescribe(ctx context.Context, c client.Client, ids []string) {
	for _, id := range ids {
		_, _ = c.DescribeWorkflowExecution(ctx, id, "")
	}
}
