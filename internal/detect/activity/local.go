package activity

import (
	"encoding/json"
	"fmt"
	"slices"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// localActivityMarkers are the marker names SDKs record for Local Activity
// results: Go and Java use LocalActivity, and the Core-based SDKs use
// core_local_activity.
var localActivityMarkers = []string{"LocalActivity", "core_local_activity"}

func init() {
	detect.Register(la001{})
	detect.Register(la003{})
}

// la001 finds Local Activity retries that Temporal bills: attempts recorded
// in Workflow Task metering, which happen once a Local Activity's retries
// outlast the first Workflow Task.
type la001 struct{}

func (la001) ID() string                    { return "LA-001" }
func (la001) Confidence() detect.Confidence { return detect.High }

func (la001) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	var ids []int64
	var attempts uint32
	for _, wt := range ex.WorkflowTasks {
		if wt.NonfirstLocalActivityAttempts > 0 {
			ids = append(ids, wt.CompletedEventID)
			attempts += wt.NonfirstLocalActivityAttempts
		}
	}
	if attempts == 0 {
		return nil
	}
	return []detect.Match{{
		EventIDs: ids,
		Note: fmt.Sprintf("%d Local Activity attempts ran after the first Workflow Task, across %d Workflow Tasks",
			attempts, len(ids)),
		ActionsSaved: laRetryActions(c.Rules, attempts),
		Keys:         localActivityKeys(ex),
	}}
}

// la003 finds Local Activities that outlast the Workflow Task timeout. The
// SDK keeps the Workflow Task alive by completing it with no commands and
// asking for a new one at once, and each of those heartbeats is an Action.
// Workflow Task timeouts before a Local Activity marker are the same
// problem when the SDK couldn't heartbeat in time.
type la003 struct{}

func (la003) ID() string                    { return "LA-003" }
func (la003) Confidence() detect.Confidence { return detect.High }

func (la003) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	if !hasLocalActivity(ex) {
		return nil
	}
	var heartbeats []int64
	for i, wt := range ex.WorkflowTasks[:max(len(ex.WorkflowTasks)-1, 0)] {
		next := ex.WorkflowTasks[i+1]
		// A heartbeat completes with no commands and the next task follows
		// with nothing delivered in between.
		if len(wt.Commands) == 0 && len(next.After) == 0 && markerAfter(ex, wt.CompletedEventID) {
			heartbeats = append(heartbeats, wt.CompletedEventID)
		}
	}
	var timeouts []int64
	for _, id := range ex.WorkflowTaskTimeouts {
		if markerAfter(ex, id) {
			timeouts = append(timeouts, id)
		}
	}
	if len(heartbeats) == 0 && len(timeouts) == 0 {
		return nil
	}
	note := fmt.Sprintf("a Local Activity outlasted the Workflow Task timeout: %d Workflow Task heartbeats", len(heartbeats))
	if len(timeouts) > 0 {
		note += fmt.Sprintf(" and %d Workflow Task timeouts", len(timeouts))
	}
	return []detect.Match{{
		EventIDs:     append(heartbeats, timeouts...),
		Note:         note,
		ActionsSaved: float64(len(heartbeats)) * workflowTaskHeartbeatActions(c.Rules),
		Keys:         localActivityKeys(ex),
	}}
}

func hasLocalActivity(ex *model.Execution) bool {
	return slices.ContainsFunc(ex.Markers, func(m *model.Marker) bool {
		return slices.Contains(localActivityMarkers, m.Name)
	})
}

// markerAfter reports whether a Local Activity marker comes after an event.
func markerAfter(ex *model.Execution, eventID int64) bool {
	return slices.ContainsFunc(ex.Markers, func(m *model.Marker) bool {
		return m.EventID > eventID && slices.Contains(localActivityMarkers, m.Name)
	})
}

// localActivityKeys returns the Local Activity types from Go and Java
// markers, which carry the type in their details.
func localActivityKeys(ex *model.Execution) []string {
	seen := map[string]bool{}
	var out []string
	for _, ev := range ex.Events {
		if ev.GetEventType() != enumspb.EVENT_TYPE_MARKER_RECORDED {
			continue
		}
		a := ev.GetMarkerRecordedEventAttributes()
		if !slices.Contains(localActivityMarkers, a.GetMarkerName()) {
			continue
		}
		if t := markerActivityType(a.GetDetails()); t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// markerActivityType reads the ActivityType from a Go or Java Local
// Activity marker's "data" detail, which is JSON. Core-based SDKs encode
// markers differently; they return "".
func markerActivityType(details map[string]*commonpb.Payloads) string {
	p := details["data"].GetPayloads()
	if len(p) == 0 {
		return ""
	}
	var data struct{ ActivityType string }
	if err := json.Unmarshal(p[0].GetData(), &data); err != nil {
		return ""
	}
	return data.ActivityType
}
