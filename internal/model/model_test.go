package model

import (
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
)

func payload(s string) *commonpb.Payload { return &commonpb.Payload{Data: []byte(s)} }

func upsert(id, wft int64, fields map[string]*commonpb.Payload) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventId:   id,
		EventType: enumspb.EVENT_TYPE_UPSERT_WORKFLOW_SEARCH_ATTRIBUTES,
		Attributes: &historypb.HistoryEvent_UpsertWorkflowSearchAttributesEventAttributes{
			UpsertWorkflowSearchAttributesEventAttributes: &historypb.UpsertWorkflowSearchAttributesEventAttributes{
				WorkflowTaskCompletedEventId: wft,
				SearchAttributes:             &commonpb.SearchAttributes{IndexedFields: fields},
			},
		},
	}
}

func completed(id int64) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{EventId: id, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED}
}

func TestSearchAttributeState(t *testing.T) {
	h := &historypb.History{Events: []*historypb.HistoryEvent{
		{EventId: 1, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED, Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{
			WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
				SearchAttributes: &commonpb.SearchAttributes{IndexedFields: map[string]*commonpb.Payload{"Status": payload(`"new"`)}},
			},
		}},
		completed(4),
		upsert(5, 4, map[string]*commonpb.Payload{"Status": payload(`"new"`)}),      // same as start
		upsert(6, 4, map[string]*commonpb.Payload{"Status": payload(`null`)}),       // unset
		upsert(7, 4, map[string]*commonpb.Payload{"Status": payload(`"new"`)}),      // set again after unset
		upsert(8, 4, map[string]*commonpb.Payload{ChangeVersionKey: payload(`[]`)}), // GetVersion only
	}}
	ex := Normalize("wf", "run", h)
	if len(ex.Upserts) != 4 {
		t.Fatalf("got %d upserts", len(ex.Upserts))
	}
	want := []bool{true, false, false, false}
	for i, u := range ex.Upserts {
		if got := u.Unchanged(); got != want[i] {
			t.Errorf("upsert at event %d: Unchanged() = %v, want %v", u.EventID, got, want[i])
		}
		if u.WorkflowTask == nil || u.WorkflowTask.CompletedEventID != 4 {
			t.Errorf("upsert at event %d not linked to its Workflow Task", u.EventID)
		}
	}
	if n := len(ex.WorkflowTasks[0].Commands); n != 4 {
		t.Errorf("Workflow Task has %d commands, want 4", n)
	}
}
