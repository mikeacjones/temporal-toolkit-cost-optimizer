package dashboard

import (
	"context"
	"net/http"
	"time"

	"go.temporal.io/sdk/client"
)

func pollStatus(ctx context.Context, c client.Client, ids []string) {
	ticker := time.NewTicker(5 * time.Second)
	for range ticker.C {
		for _, id := range ids {
			_, _ = c.QueryWorkflow(ctx, id, "", "status") // want CODE-001
		}
	}
}

func statusHandler(c client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, _ := c.QueryWorkflowWithOptions(r.Context(), &client.QueryWorkflowWithOptionsRequest{ // want CODE-001
			WorkflowID: r.URL.Query().Get("id"),
			QueryType:  "progress",
		})
		_ = v
	}
}

func register(mux *http.ServeMux, c client.Client) {
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = c.QueryWorkflow(r.Context(), "order-1", "", "status") // want CODE-001
	})
}
