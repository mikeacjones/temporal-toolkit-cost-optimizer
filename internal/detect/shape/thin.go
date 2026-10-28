package shape

import (
	"fmt"

	enumspb "go.temporal.io/api/enums/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

func init() {
	detect.Register(wf001{})
	detect.Register(tmr005{})
	detect.Register(chd001{})
	detect.Register(nex002{})
}

// wf001 finds Workflows that only wrap one Activity. Child Workflows are
// left to CHD-001. Nexus handlers are skipped: a Nexus Operation, not a
// client, starts them, so a client-started Standalone Activity can't
// replace them.
type wf001 struct{}

func (wf001) ID() string                    { return "WF-001" }
func (wf001) Confidence() detect.Confidence { return detect.High }
func (wf001) Features() []string            { return []string{"standalone-activity"} }

func (wf001) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	if ex.Start.ParentWorkflowID != "" || startedByNexus(ex) || !onlyOneActivity(ex, c) {
		return nil
	}
	a := ex.Activities[0]
	return []detect.Match{{
		EventIDs: []int64{ex.Start.EventID, a.ScheduledEventID},
		Note:     fmt.Sprintf("the Workflow only runs Activity %s", a.Type),
		// A Standalone Activity replaces the Workflow start; the Activity
		// itself costs the same either way.
		ActionsSaved: startActions(c),
		Keys:         []string{ex.WorkflowType, a.Type},
	}}
}

// leadingTimer returns the Timer when it was the only command of the first
// Workflow Task and it fired.
func leadingTimer(ex *model.Execution) *model.Timer {
	if len(ex.WorkflowTasks) == 0 || len(ex.Timers) == 0 {
		return nil
	}
	first := ex.WorkflowTasks[0]
	if len(first.Commands) != 1 || first.Commands[0].GetEventType() != enumspb.EVENT_TYPE_TIMER_STARTED {
		return nil
	}
	t := ex.Timers[0]
	if t.WorkflowTask != first || !t.Fired {
		return nil
	}
	return t
}

// tmr005 finds Workflows whose first command is a Timer that delays the
// real work. A Timer canceled early is a wait, not a delay, so only fired
// Timers count.
type tmr005 struct{}

func (tmr005) ID() string                    { return "TMR-005" }
func (tmr005) Confidence() detect.Confidence { return detect.Medium }
func (tmr005) Features() []string            { return []string{"workflow-start-delay"} }

func (tmr005) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	if ex.Start.Delay > 0 {
		return nil
	}
	t := leadingTimer(ex)
	if t == nil {
		return nil
	}
	// A Signal or Update before the Timer fired suggests the Timer guards a
	// wait for it rather than delaying the start.
	for _, s := range ex.Signals {
		if s.EventID < t.EndEventID {
			return nil
		}
	}
	for _, u := range ex.Updates {
		if u.AcceptedEventID < t.EndEventID {
			return nil
		}
	}
	return []detect.Match{{
		EventIDs:     []int64{t.StartedEventID, t.EndEventID},
		Note:         fmt.Sprintf("the first command is a %s Timer that fired before any other work", t.Duration),
		ActionsSaved: eventActions(c, enumspb.EVENT_TYPE_TIMER_STARTED),
		Keys:         []string{ex.WorkflowType},
	}}
}

// chd001 finds Child Workflows that only run one Activity, which the parent
// could call directly.
type chd001 struct{}

func (chd001) ID() string                    { return "CHD-001" }
func (chd001) Confidence() detect.Confidence { return detect.High }
func (chd001) NeedsChildren() bool           { return true }

func (chd001) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	var out []detect.Match
	for _, ch := range ex.Children {
		child := ex.ChildExecutions[ch.InitiatedEventID]
		if child == nil || !onlyOneActivity(child, c) {
			continue
		}
		a := child.Activities[0]
		out = append(out, detect.Match{
			EventIDs: []int64{ch.InitiatedEventID},
			Note:     fmt.Sprintf("Child Workflow %s (%s) only runs Activity %s", ch.WorkflowType, ch.WorkflowID, a.Type),
			// Starting a child is billed at initiation; calling the Activity
			// from the parent costs what the child's Activity costs now.
			ActionsSaved: eventActions(c, enumspb.EVENT_TYPE_START_CHILD_WORKFLOW_EXECUTION_INITIATED),
			Keys:         []string{ch.WorkflowType, a.Type},
		})
	}
	return out
}

// nex002 finds Workflows that only call one Nexus Operation.
type nex002 struct{}

func (nex002) ID() string                    { return "NEX-002" }
func (nex002) Confidence() detect.Confidence { return detect.Advisory }
func (nex002) Features() []string            { return []string{"standalone-nexus-operation"} }

func (nex002) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	if ex.Start.ParentWorkflowID != "" || !finished(ex) || ex.Start.CronSchedule != "" {
		return nil
	}
	n := countOf(ex, c)
	if n.nexus != 1 || n.activities != 0 || n.timers != 0 || n.signals != 0 || n.updates != 0 ||
		n.children != 0 || n.billableUpserts != 0 || n.localActivityMarkers != 0 {
		return nil
	}
	op := ex.NexusOps[0]
	return []detect.Match{{
		EventIDs:     []int64{ex.Start.EventID, op.ScheduledEventID},
		Note:         fmt.Sprintf("the Workflow only calls Nexus Operation %s/%s on endpoint %s", op.Service, op.Operation, op.Endpoint),
		ActionsSaved: startActions(c),
		Keys:         []string{ex.WorkflowType, op.Service, op.Operation},
	}}
}
