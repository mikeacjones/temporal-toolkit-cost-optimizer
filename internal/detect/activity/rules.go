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

// scheduleActions returns the Actions billed for scheduling one Activity.
func scheduleActions(r *billing.Rules) float64 {
	if r == nil {
		return 0
	}
	if rule, ok := r.Rule(enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED); ok {
		return rule.Actions
	}
	return 0
}

// heartbeatActions returns the Actions billed per Activity Heartbeat that
// reaches the server. Heartbeats aren't in history, so the rule sits in
// not_in_history.
func heartbeatActions(r *billing.Rules) float64 {
	if r == nil {
		return 0
	}
	for _, op := range r.NotInHistory {
		if op.Operation == "activity_heartbeat" {
			return op.Actions
		}
	}
	return 0
}

// laRetryActions returns the Actions billed for n Local Activity attempts
// recorded in Workflow Task metering.
func laRetryActions(r *billing.Rules, n uint32) float64 {
	if n == 0 || r == nil {
		return 0
	}
	if rule, ok := r.Rule(enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED); ok && rule.ActionsFrom == "nonfirst_local_activity_execution_attempts" {
		return float64(n)
	}
	return 0
}
