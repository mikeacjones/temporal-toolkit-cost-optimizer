package shape

import (
	enumspb "go.temporal.io/api/enums/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// eventActions returns the Actions the billing rules give one event type,
// or 0 when there's no fixed count.
func eventActions(c *detect.Context, et enumspb.EventType) float64 {
	if c == nil || c.Rules == nil {
		return 0
	}
	r, ok := c.Rules.Rule(et)
	if !ok || r.ActionsFrom != "" {
		return 0
	}
	return r.Actions
}

// operationActions returns the Actions for an operation rule, such as
// signal_with_start, and whether the rules have one.
func operationActions(c *detect.Context, op string) (float64, bool) {
	if c == nil || c.Rules == nil {
		return 0, false
	}
	for _, r := range c.Rules.Rules {
		if r.Operation == op {
			return r.Actions, true
		}
	}
	return 0, false
}

func startActions(c *detect.Context) float64 {
	return eventActions(c, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED)
}

// finished reports whether the execution closed on its own, so its whole
// shape is known. Runs that continued as new, were terminated, or are still
// open are left out.
func finished(ex *model.Execution) bool {
	switch ex.Status {
	case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED:
		return true
	}
	return false
}

// counts summarizes what an execution did besides its own start.
type counts struct {
	activities, timers, signals, updates, children, nexus int
	billableUpserts                                       int
	localActivityMarkers                                  int
}

func countOf(ex *model.Execution, c *detect.Context) counts {
	n := counts{
		activities: len(ex.Activities), timers: len(ex.Timers), signals: len(ex.Signals),
		updates: len(ex.Updates), children: len(ex.Children), nexus: len(ex.NexusOps),
	}
	for _, u := range ex.Upserts {
		if c.Rules == nil || c.Rules.UpsertActions(u) > 0 {
			n.billableUpserts++
		}
	}
	for _, m := range ex.Markers {
		if isLocalActivityMarker(m.Name) {
			n.localActivityMarkers++
		}
	}
	return n
}

func isLocalActivityMarker(name string) bool {
	return name == "LocalActivity" || name == "core_local_activity"
}

// onlyOneActivity reports whether the execution ran exactly one Activity
// and nothing else that a Standalone Activity couldn't do: no Timers,
// Signals, Updates, Child Workflows, Nexus Operations, Local Activities, or
// billable Search Attribute upserts. Queries never reach history, so the
// guidance asks the user to check for them.
func onlyOneActivity(ex *model.Execution, c *detect.Context) bool {
	n := countOf(ex, c)
	return finished(ex) && n.activities == 1 && n.timers == 0 && n.signals == 0 && n.updates == 0 &&
		n.children == 0 && n.nexus == 0 && n.billableUpserts == 0 && n.localActivityMarkers == 0 &&
		ex.Start.CronSchedule == ""
}

// startedByNexus reports whether a Nexus Operation started the execution.
// A Workflow-backed Nexus Operation attaches a Nexus completion callback to
// the handler Workflow's start, so the result can go back to the caller.
// Links from the start event to a caller's NexusOperationScheduled event
// also count.
func startedByNexus(ex *model.Execution) bool {
	if len(ex.Events) == 0 {
		return false
	}
	first := ex.Events[0]
	for _, cb := range first.GetWorkflowExecutionStartedEventAttributes().GetCompletionCallbacks() {
		if cb.GetNexus() != nil {
			return true
		}
	}
	for _, l := range first.GetLinks() {
		if l.GetWorkflowEvent().GetEventRef().GetEventType() == enumspb.EVENT_TYPE_NEXUS_OPERATION_SCHEDULED {
			return true
		}
	}
	return false
}

// firstTaskScheduledID returns the event ID of the first
// WorkflowTaskScheduled event, or 0 when there's none.
func firstTaskScheduledID(ex *model.Execution) int64 {
	for _, ev := range ex.Events {
		if ev.GetEventType() == enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED {
			return ev.GetEventId()
		}
	}
	return 0
}

// fromSignalWithStart reports whether a Signal arrived with the start, as
// Signal-With-Start writes it: before the first Workflow Task is scheduled.
func fromSignalWithStart(ex *model.Execution, s *model.Signal) bool {
	first := firstTaskScheduledID(ex)
	return first == 0 || s.EventID < first
}
