package model

import (
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"
	historypb "go.temporal.io/api/history/v1"
	"google.golang.org/protobuf/proto"
)

// Start describes how the execution started.
type Start struct {
	EventID  int64
	Identity string
	// InputBytes is the encoded size of the input payloads.
	InputBytes int
	// HeaderBytes is the encoded size of the start header, such as tracing
	// context.
	HeaderBytes int
	// ParentWorkflowID is set for Child Workflows.
	ParentWorkflowID string
	// ContinuedFromRunID is set when this run came from Continue-As-New.
	ContinuedFromRunID string
	FirstRunID         string
	CronSchedule       string
	// Delay is the start delay (FirstWorkflowTaskBackoff).
	Delay time.Duration
	// Attempt is the Workflow retry attempt, starting at 1.
	Attempt int32
}

// Activity is one scheduled Activity and what became of it.
type Activity struct {
	ScheduledEventID int64
	StartedEventID   int64
	ClosedEventID    int64 // 0 while open
	ActivityID       string
	Type             string
	TaskQueue        string
	WorkflowTask     *WorkflowTask // the task that scheduled it

	ScheduledTime, StartedTime, ClosedTime time.Time

	// Attempt is the attempt recorded on the started event: the total
	// number of attempts. 0 when the Activity never started.
	Attempt int32
	// Outcome is the closing event type, or EVENT_TYPE_UNSPECIFIED while open.
	Outcome enumspb.EventType
	// LastFailure is the failure from the previous attempt (on the started
	// event), and Failure is the final failure, when there is one.
	LastFailure *failurepb.Failure
	Failure     *failurepb.Failure

	RetryPolicy                                                   *commonpb.RetryPolicy
	HeartbeatTimeout, StartToCloseTimeout, ScheduleToCloseTimeout time.Duration
	InputBytes, ResultBytes, HeaderBytes                          int
	StartedIdentity                                               string
}

// Duration is the time from the last attempt's start to close, or 0 when
// it's still open or never started.
func (a *Activity) Duration() time.Duration {
	if a.StartedTime.IsZero() || a.ClosedTime.IsZero() {
		return 0
	}
	return a.ClosedTime.Sub(a.StartedTime)
}

// Timer is one started Timer.
type Timer struct {
	StartedEventID int64
	EndEventID     int64 // fired or canceled event, 0 while pending
	TimerID        string
	Duration       time.Duration
	WorkflowTask   *WorkflowTask
	StartTime      time.Time
	EndTime        time.Time
	Fired          bool
	Canceled       bool
	// CanceledBy is the Workflow Task that canceled it.
	CanceledBy *WorkflowTask
}

// Signal is one Signal the execution received.
type Signal struct {
	EventID     int64
	Name        string
	Time        time.Time
	Identity    string
	InputBytes  int
	HeaderBytes int
	// ExternalWorkflowID is the sending Workflow, for Signals sent from
	// another Workflow. Their Identity is the server's ("history-service"),
	// shared by every sending Workflow.
	ExternalWorkflowID string
}

// Sender identifies who sent the Signal: the sending Workflow when there is
// one, otherwise the client identity.
func (s *Signal) Sender() string {
	if s.ExternalWorkflowID != "" {
		return "workflow:" + s.ExternalWorkflowID
	}
	return s.Identity
}

// Update is one accepted Update.
type Update struct {
	AcceptedEventID  int64
	CompletedEventID int64
	UpdateID         string
	Name             string
	Identity         string
	Time             time.Time
	InputBytes       int
}

// Child is one Child Workflow the execution started.
type Child struct {
	InitiatedEventID int64
	ClosedEventID    int64
	WorkflowID       string
	RunID            string // from the started event
	WorkflowType     string
	WorkflowTask     *WorkflowTask
	Outcome          enumspb.EventType
	InitiatedTime    time.Time
	ClosedTime       time.Time
}

// Marker is one MarkerRecorded event, such as a Local Activity result or a
// version marker.
type Marker struct {
	EventID      int64
	Name         string
	WorkflowTask *WorkflowTask
	Bytes        int // encoded size of the whole event
	Failed       bool
}

// NexusOp is one scheduled Nexus Operation.
type NexusOp struct {
	ScheduledEventID int64
	ClosedEventID    int64
	Endpoint         string
	Service          string
	Operation        string
	WorkflowTask     *WorkflowTask
	Outcome          enumspb.EventType
}

// PayloadsBytes is the encoded size of a Payloads message.
func PayloadsBytes(p *commonpb.Payloads) int {
	if p == nil {
		return 0
	}
	return proto.Size(p)
}

// HeaderBytes is the encoded size of a Header.
func HeaderBytes(h *commonpb.Header) int {
	if h == nil {
		return 0
	}
	return proto.Size(h)
}

func dur(d interface{ AsDuration() time.Duration }) time.Duration {
	if d == nil {
		return 0
	}
	return d.AsDuration()
}

// buildTimeline fills the per-primitive views of an execution.
func buildTimeline(ex *Execution, h *historypb.History, tasks map[int64]*WorkflowTask) {
	ex.HistoryBytes = proto.Size(h)
	acts := map[int64]*Activity{}
	timers := map[int64]*Timer{}
	children := map[int64]*Child{}
	nexus := map[int64]*NexusOp{}
	updates := map[string]*Update{}

	for _, ev := range ex.Events {
		id, at := ev.GetEventId(), ev.GetEventTime().AsTime()
		switch ev.GetEventType() {
		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED:
			a := ev.GetWorkflowExecutionStartedEventAttributes()
			ex.Start = Start{
				EventID: id, Identity: a.GetIdentity(),
				InputBytes: PayloadsBytes(a.GetInput()), HeaderBytes: HeaderBytes(a.GetHeader()),
				ParentWorkflowID:   a.GetParentWorkflowExecution().GetWorkflowId(),
				ContinuedFromRunID: a.GetContinuedExecutionRunId(), FirstRunID: a.GetFirstExecutionRunId(),
				CronSchedule: a.GetCronSchedule(), Delay: dur(a.GetFirstWorkflowTaskBackoff()), Attempt: a.GetAttempt(),
			}
		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_CONTINUED_AS_NEW:
			ex.ContinuedAsNew = true
		case enumspb.EVENT_TYPE_WORKFLOW_TASK_TIMED_OUT:
			ex.WorkflowTaskTimeouts = append(ex.WorkflowTaskTimeouts, id)
		case enumspb.EVENT_TYPE_WORKFLOW_TASK_FAILED:
			ex.WorkflowTaskFailures = append(ex.WorkflowTaskFailures, id)

		case enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED:
			a := ev.GetActivityTaskScheduledEventAttributes()
			act := &Activity{
				ScheduledEventID: id, ActivityID: a.GetActivityId(), Type: a.GetActivityType().GetName(),
				TaskQueue: a.GetTaskQueue().GetName(), WorkflowTask: tasks[a.GetWorkflowTaskCompletedEventId()],
				ScheduledTime: at, RetryPolicy: a.GetRetryPolicy(),
				HeartbeatTimeout: dur(a.GetHeartbeatTimeout()), StartToCloseTimeout: dur(a.GetStartToCloseTimeout()),
				ScheduleToCloseTimeout: dur(a.GetScheduleToCloseTimeout()),
				InputBytes:             PayloadsBytes(a.GetInput()), HeaderBytes: HeaderBytes(a.GetHeader()),
			}
			acts[id] = act
			ex.Activities = append(ex.Activities, act)
		case enumspb.EVENT_TYPE_ACTIVITY_TASK_STARTED:
			a := ev.GetActivityTaskStartedEventAttributes()
			if act := acts[a.GetScheduledEventId()]; act != nil {
				act.StartedEventID, act.StartedTime = id, at
				act.Attempt, act.LastFailure, act.StartedIdentity = a.GetAttempt(), a.GetLastFailure(), a.GetIdentity()
			}
		case enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED:
			a := ev.GetActivityTaskCompletedEventAttributes()
			if act := acts[a.GetScheduledEventId()]; act != nil {
				act.closeWith(ev)
				act.ResultBytes = PayloadsBytes(a.GetResult())
			}
		case enumspb.EVENT_TYPE_ACTIVITY_TASK_FAILED:
			a := ev.GetActivityTaskFailedEventAttributes()
			if act := acts[a.GetScheduledEventId()]; act != nil {
				act.closeWith(ev)
				act.Failure = a.GetFailure()
			}
		case enumspb.EVENT_TYPE_ACTIVITY_TASK_TIMED_OUT:
			a := ev.GetActivityTaskTimedOutEventAttributes()
			if act := acts[a.GetScheduledEventId()]; act != nil {
				act.closeWith(ev)
				act.Failure = a.GetFailure()
			}
		case enumspb.EVENT_TYPE_ACTIVITY_TASK_CANCELED:
			if act := acts[ev.GetActivityTaskCanceledEventAttributes().GetScheduledEventId()]; act != nil {
				act.closeWith(ev)
			}

		case enumspb.EVENT_TYPE_TIMER_STARTED:
			a := ev.GetTimerStartedEventAttributes()
			t := &Timer{
				StartedEventID: id, TimerID: a.GetTimerId(), Duration: dur(a.GetStartToFireTimeout()),
				WorkflowTask: tasks[a.GetWorkflowTaskCompletedEventId()], StartTime: at,
			}
			timers[id] = t
			ex.Timers = append(ex.Timers, t)
		case enumspb.EVENT_TYPE_TIMER_FIRED:
			if t := timers[ev.GetTimerFiredEventAttributes().GetStartedEventId()]; t != nil {
				t.EndEventID, t.EndTime, t.Fired = id, at, true
			}
		case enumspb.EVENT_TYPE_TIMER_CANCELED:
			a := ev.GetTimerCanceledEventAttributes()
			if t := timers[a.GetStartedEventId()]; t != nil {
				t.EndEventID, t.EndTime, t.Canceled = id, at, true
				t.CanceledBy = tasks[a.GetWorkflowTaskCompletedEventId()]
			}

		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED:
			a := ev.GetWorkflowExecutionSignaledEventAttributes()
			ex.Signals = append(ex.Signals, &Signal{
				EventID: id, Name: a.GetSignalName(), Time: at, Identity: a.GetIdentity(),
				InputBytes: PayloadsBytes(a.GetInput()), HeaderBytes: HeaderBytes(a.GetHeader()),
				ExternalWorkflowID: a.GetExternalWorkflowExecution().GetWorkflowId(),
			})

		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_UPDATE_ACCEPTED:
			a := ev.GetWorkflowExecutionUpdateAcceptedEventAttributes()
			req := a.GetAcceptedRequest()
			u := &Update{
				AcceptedEventID: id, UpdateID: req.GetMeta().GetUpdateId(), Name: req.GetInput().GetName(),
				Identity: req.GetMeta().GetIdentity(), Time: at, InputBytes: PayloadsBytes(req.GetInput().GetArgs()),
			}
			updates[u.UpdateID] = u
			ex.Updates = append(ex.Updates, u)
		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_UPDATE_COMPLETED:
			if u := updates[ev.GetWorkflowExecutionUpdateCompletedEventAttributes().GetMeta().GetUpdateId()]; u != nil {
				u.CompletedEventID = id
			}

		case enumspb.EVENT_TYPE_START_CHILD_WORKFLOW_EXECUTION_INITIATED:
			a := ev.GetStartChildWorkflowExecutionInitiatedEventAttributes()
			c := &Child{
				InitiatedEventID: id, WorkflowID: a.GetWorkflowId(), WorkflowType: a.GetWorkflowType().GetName(),
				WorkflowTask: tasks[a.GetWorkflowTaskCompletedEventId()], InitiatedTime: at,
			}
			children[id] = c
			ex.Children = append(ex.Children, c)
		case enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_STARTED:
			a := ev.GetChildWorkflowExecutionStartedEventAttributes()
			if c := children[a.GetInitiatedEventId()]; c != nil {
				c.RunID = a.GetWorkflowExecution().GetRunId()
			}
		case enumspb.EVENT_TYPE_START_CHILD_WORKFLOW_EXECUTION_FAILED:
			a := ev.GetStartChildWorkflowExecutionFailedEventAttributes()
			if c := children[a.GetInitiatedEventId()]; c != nil {
				c.ClosedEventID, c.ClosedTime, c.Outcome = id, at, ev.GetEventType()
			}
		case enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_COMPLETED,
			enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_FAILED,
			enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_CANCELED,
			enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_TIMED_OUT,
			enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_TERMINATED:
			if c := children[childInitiatedID(ev)]; c != nil {
				c.ClosedEventID, c.ClosedTime, c.Outcome = id, at, ev.GetEventType()
			}

		case enumspb.EVENT_TYPE_MARKER_RECORDED:
			a := ev.GetMarkerRecordedEventAttributes()
			ex.Markers = append(ex.Markers, &Marker{
				EventID: id, Name: a.GetMarkerName(), WorkflowTask: tasks[a.GetWorkflowTaskCompletedEventId()],
				Bytes: proto.Size(ev), Failed: a.GetFailure() != nil,
			})

		case enumspb.EVENT_TYPE_NEXUS_OPERATION_SCHEDULED:
			a := ev.GetNexusOperationScheduledEventAttributes()
			op := &NexusOp{
				ScheduledEventID: id, Endpoint: a.GetEndpoint(), Service: a.GetService(), Operation: a.GetOperation(),
				WorkflowTask: tasks[a.GetWorkflowTaskCompletedEventId()],
			}
			nexus[id] = op
			ex.NexusOps = append(ex.NexusOps, op)
		case enumspb.EVENT_TYPE_NEXUS_OPERATION_COMPLETED,
			enumspb.EVENT_TYPE_NEXUS_OPERATION_FAILED,
			enumspb.EVENT_TYPE_NEXUS_OPERATION_CANCELED,
			enumspb.EVENT_TYPE_NEXUS_OPERATION_TIMED_OUT:
			if op := nexus[nexusScheduledID(ev)]; op != nil {
				op.ClosedEventID, op.Outcome = id, ev.GetEventType()
			}
		}
	}
}

func (a *Activity) closeWith(ev *historypb.HistoryEvent) {
	a.ClosedEventID, a.ClosedTime, a.Outcome = ev.GetEventId(), ev.GetEventTime().AsTime(), ev.GetEventType()
}

func childInitiatedID(ev *historypb.HistoryEvent) int64 {
	switch ev.GetEventType() {
	case enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_COMPLETED:
		return ev.GetChildWorkflowExecutionCompletedEventAttributes().GetInitiatedEventId()
	case enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_FAILED:
		return ev.GetChildWorkflowExecutionFailedEventAttributes().GetInitiatedEventId()
	case enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_CANCELED:
		return ev.GetChildWorkflowExecutionCanceledEventAttributes().GetInitiatedEventId()
	case enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_TIMED_OUT:
		return ev.GetChildWorkflowExecutionTimedOutEventAttributes().GetInitiatedEventId()
	case enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_TERMINATED:
		return ev.GetChildWorkflowExecutionTerminatedEventAttributes().GetInitiatedEventId()
	}
	return 0
}

func nexusScheduledID(ev *historypb.HistoryEvent) int64 {
	switch ev.GetEventType() {
	case enumspb.EVENT_TYPE_NEXUS_OPERATION_COMPLETED:
		return ev.GetNexusOperationCompletedEventAttributes().GetScheduledEventId()
	case enumspb.EVENT_TYPE_NEXUS_OPERATION_FAILED:
		return ev.GetNexusOperationFailedEventAttributes().GetScheduledEventId()
	case enumspb.EVENT_TYPE_NEXUS_OPERATION_CANCELED:
		return ev.GetNexusOperationCanceledEventAttributes().GetScheduledEventId()
	case enumspb.EVENT_TYPE_NEXUS_OPERATION_TIMED_OUT:
		return ev.GetNexusOperationTimedOutEventAttributes().GetScheduledEventId()
	}
	return 0
}
