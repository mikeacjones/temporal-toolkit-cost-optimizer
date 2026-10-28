package activity

import (
	enumspb "go.temporal.io/api/enums/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/billing"
)

// workflowTaskHeartbeatActions is what one Workflow Task heartbeat costs,
// from the not_in_history rules, since a heartbeat has no event of its own.
func workflowTaskHeartbeatActions(r *billing.Rules) float64 {
	a, _ := r.OperationActions("workflow_task_heartbeat")
	return a
}

// retryActions returns the Actions billed for n retries of an Activity,
// from the rule for ActivityTaskStarted.attempt.
func retryActions(r *billing.Rules, n int32) float64 {
	if n <= 0 || r == nil {
		return 0
	}
	if rule, ok := r.Rule(enumspb.EVENT_TYPE_ACTIVITY_TASK_STARTED); ok && rule.ActionsFrom == "attempt_minus_one" {
		return float64(n)
	}
	return 0
}
