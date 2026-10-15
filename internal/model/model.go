// Package model turns a raw Event History into a timeline that detectors can
// read: commands grouped by the Workflow Task that produced them, and the
// Search Attribute values in effect after each upsert.
package model

import (
	"bytes"
	"sort"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
)

// ChangeVersionKey is the Search Attribute that GetVersion upserts. Temporal
// doesn't bill these upserts.
const ChangeVersionKey = "TemporalChangeVersion"

// Execution is one normalized Workflow Execution.
type Execution struct {
	WorkflowID   string
	RunID        string
	WorkflowType string
	StartTime    time.Time
	CloseTime    time.Time // zero while running
	Status       enumspb.WorkflowExecutionStatus

	Events        []*historypb.HistoryEvent
	WorkflowTasks []*WorkflowTask
	Upserts       []*Upsert

	// InitialSearchAttributes are the attributes set in the start request.
	InitialSearchAttributes map[string]*commonpb.Payload

	// The rest is built by buildTimeline; see timeline.go.
	Start      Start
	Activities []*Activity
	Timers     []*Timer
	Signals    []*Signal
	Updates    []*Update
	Children   []*Child
	Markers    []*Marker
	NexusOps   []*NexusOp
	// WorkflowTaskTimeouts and WorkflowTaskFailures are event IDs.
	WorkflowTaskTimeouts []int64
	WorkflowTaskFailures []int64
	// HistoryBytes is the encoded size of the whole history.
	HistoryBytes int
	// ContinuedAsNew is set when this run ended with Continue-As-New.
	ContinuedAsNew bool

	// ChildExecutions holds Child Workflow histories, keyed by the parent's
	// StartChildWorkflowExecutionInitiated event ID. The analysis fills it
	// only for detectors that ask for children.
	ChildExecutions map[int64]*Execution
}

// WorkflowTask is one completed Workflow Task and the command events it
// produced.
type WorkflowTask struct {
	// CompletedEventID is the ID of the WorkflowTaskCompleted event.
	CompletedEventID int64
	// Index is the position of this task among completed tasks, starting at 0.
	Index int
	// Commands are the command events whose WorkflowTaskCompletedEventId
	// points at this task, in event order.
	Commands []*historypb.HistoryEvent
	// After lists event types recorded between the previous completed task
	// and this one, such as Signals, Activity results, and fired Timers.
	After []enumspb.EventType
	// ScheduledTime, StartedTime, and CompletedTime come from the task's
	// three events.
	ScheduledTime, StartedTime, CompletedTime time.Time
	// NonfirstLocalActivityAttempts is the metering count of Local Activity
	// retries in this task.
	NonfirstLocalActivityAttempts uint32
}

// Upsert is one UpsertWorkflowSearchAttributes event with the Search
// Attribute state around it.
type Upsert struct {
	EventID      int64
	WorkflowTask *WorkflowTask
	// Fields are the attributes this upsert sets.
	Fields map[string]*commonpb.Payload
	// Before holds each field's value before this upsert. A missing key means
	// the attribute was unset.
	Before map[string]*commonpb.Payload
}

// BillableKeys returns the attribute names the upsert sets, sorted, without
// TemporalChangeVersion.
func (u *Upsert) BillableKeys() []string {
	keys := make([]string, 0, len(u.Fields))
	for k := range u.Fields {
		if k != ChangeVersionKey {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// Unchanged reports whether every attribute the upsert sets, other than
// TemporalChangeVersion, already had the same value.
func (u *Upsert) Unchanged() bool {
	keys := u.BillableKeys()
	if len(keys) == 0 {
		return false
	}
	for _, k := range keys {
		prev, ok := u.Before[k]
		if !ok || !PayloadEqual(prev, u.Fields[k]) {
			return false
		}
	}
	return true
}

// PayloadEqual compares two Search Attribute payloads by their encoded bytes.
// Search Attributes are always JSON-encoded by the SDKs, never by a codec, so
// equal values have equal bytes.
func PayloadEqual(a, b *commonpb.Payload) bool {
	if a == nil || b == nil {
		return a == b
	}
	return bytes.Equal(a.GetData(), b.GetData())
}

// Normalize builds an Execution from a history. It never fails: unknown or
// truncated histories produce a partial timeline.
func Normalize(workflowID, runID string, h *historypb.History) *Execution {
	ex := &Execution{
		WorkflowID:              workflowID,
		RunID:                   runID,
		Events:                  h.GetEvents(),
		InitialSearchAttributes: map[string]*commonpb.Payload{},
		Status:                  enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
	}

	tasks := map[int64]*WorkflowTask{}
	var pending []enumspb.EventType
	var wtScheduled, wtStarted time.Time
	current := map[string]*commonpb.Payload{}

	for _, ev := range ex.Events {
		switch ev.GetEventType() {
		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED:
			attrs := ev.GetWorkflowExecutionStartedEventAttributes()
			ex.WorkflowType = attrs.GetWorkflowType().GetName()
			ex.StartTime = ev.GetEventTime().AsTime()
			for k, v := range attrs.GetSearchAttributes().GetIndexedFields() {
				ex.InitialSearchAttributes[k] = v
				current[k] = v
			}
			continue
		case enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED:
			wtScheduled = ev.GetEventTime().AsTime()
			continue
		case enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED:
			wtStarted = ev.GetEventTime().AsTime()
			continue
		case enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED:
			wt := &WorkflowTask{
				CompletedEventID:              ev.GetEventId(),
				Index:                         len(ex.WorkflowTasks),
				After:                         pending,
				ScheduledTime:                 wtScheduled,
				StartedTime:                   wtStarted,
				CompletedTime:                 ev.GetEventTime().AsTime(),
				NonfirstLocalActivityAttempts: ev.GetWorkflowTaskCompletedEventAttributes().GetMeteringMetadata().GetNonfirstLocalActivityExecutionAttempts(),
			}
			pending = nil
			tasks[wt.CompletedEventID] = wt
			ex.WorkflowTasks = append(ex.WorkflowTasks, wt)
			continue
		}

		if status, ok := closeStatus[ev.GetEventType()]; ok {
			ex.Status = status
			ex.CloseTime = ev.GetEventTime().AsTime()
		}

		if wftID := commandTaskID(ev); wftID != 0 {
			if wt := tasks[wftID]; wt != nil {
				wt.Commands = append(wt.Commands, ev)
			}
		} else {
			pending = append(pending, ev.GetEventType())
		}

		if ev.GetEventType() == enumspb.EVENT_TYPE_UPSERT_WORKFLOW_SEARCH_ATTRIBUTES {
			attrs := ev.GetUpsertWorkflowSearchAttributesEventAttributes()
			u := &Upsert{
				EventID:      ev.GetEventId(),
				WorkflowTask: tasks[attrs.GetWorkflowTaskCompletedEventId()],
				Fields:       attrs.GetSearchAttributes().GetIndexedFields(),
				Before:       map[string]*commonpb.Payload{},
			}
			for k, v := range u.Fields {
				if prev, ok := current[k]; ok {
					u.Before[k] = prev
				}
				if isUnset(v) {
					delete(current, k)
				} else {
					current[k] = v
				}
			}
			ex.Upserts = append(ex.Upserts, u)
		}
	}
	buildTimeline(ex, h, tasks)
	return ex
}

// isUnset reports whether a Search Attribute payload removes the attribute.
// The SDKs send a nil JSON value to unset one.
func isUnset(p *commonpb.Payload) bool {
	d := p.GetData()
	return len(d) == 0 || string(d) == "null"
}

var closeStatus = map[enumspb.EventType]enumspb.WorkflowExecutionStatus{
	enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED:        enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED,
	enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_FAILED:           enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
	enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_TIMED_OUT:        enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
	enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CANCELED:         enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED,
	enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_TERMINATED:       enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED,
	enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CONTINUED_AS_NEW: enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW,
}

// commandTaskID returns the WorkflowTaskCompleted event ID for events that a
// Workflow Task command produced, and 0 for everything else.
func commandTaskID(ev *historypb.HistoryEvent) int64 {
	switch ev.GetEventType() {
	case enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED:
		return ev.GetActivityTaskScheduledEventAttributes().GetWorkflowTaskCompletedEventId()
	case enumspb.EVENT_TYPE_TIMER_STARTED:
		return ev.GetTimerStartedEventAttributes().GetWorkflowTaskCompletedEventId()
	case enumspb.EVENT_TYPE_TIMER_CANCELED:
		return ev.GetTimerCanceledEventAttributes().GetWorkflowTaskCompletedEventId()
	case enumspb.EVENT_TYPE_ACTIVITY_TASK_CANCEL_REQUESTED:
		return ev.GetActivityTaskCancelRequestedEventAttributes().GetWorkflowTaskCompletedEventId()
	case enumspb.EVENT_TYPE_MARKER_RECORDED:
		return ev.GetMarkerRecordedEventAttributes().GetWorkflowTaskCompletedEventId()
	case enumspb.EVENT_TYPE_UPSERT_WORKFLOW_SEARCH_ATTRIBUTES:
		return ev.GetUpsertWorkflowSearchAttributesEventAttributes().GetWorkflowTaskCompletedEventId()
	case enumspb.EVENT_TYPE_WORKFLOW_PROPERTIES_MODIFIED:
		return ev.GetWorkflowPropertiesModifiedEventAttributes().GetWorkflowTaskCompletedEventId()
	case enumspb.EVENT_TYPE_START_CHILD_WORKFLOW_EXECUTION_INITIATED:
		return ev.GetStartChildWorkflowExecutionInitiatedEventAttributes().GetWorkflowTaskCompletedEventId()
	case enumspb.EVENT_TYPE_SIGNAL_EXTERNAL_WORKFLOW_EXECUTION_INITIATED:
		return ev.GetSignalExternalWorkflowExecutionInitiatedEventAttributes().GetWorkflowTaskCompletedEventId()
	case enumspb.EVENT_TYPE_REQUEST_CANCEL_EXTERNAL_WORKFLOW_EXECUTION_INITIATED:
		return ev.GetRequestCancelExternalWorkflowExecutionInitiatedEventAttributes().GetWorkflowTaskCompletedEventId()
	case enumspb.EVENT_TYPE_NEXUS_OPERATION_SCHEDULED:
		return ev.GetNexusOperationScheduledEventAttributes().GetWorkflowTaskCompletedEventId()
	case enumspb.EVENT_TYPE_NEXUS_OPERATION_CANCEL_REQUESTED:
		return ev.GetNexusOperationCancelRequestedEventAttributes().GetWorkflowTaskCompletedEventId()
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED:
		return ev.GetWorkflowExecutionCompletedEventAttributes().GetWorkflowTaskCompletedEventId()
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_FAILED:
		return ev.GetWorkflowExecutionFailedEventAttributes().GetWorkflowTaskCompletedEventId()
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CANCELED:
		return ev.GetWorkflowExecutionCanceledEventAttributes().GetWorkflowTaskCompletedEventId()
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CONTINUED_AS_NEW:
		return ev.GetWorkflowExecutionContinuedAsNewEventAttributes().GetWorkflowTaskCompletedEventId()
	}
	return 0
}
