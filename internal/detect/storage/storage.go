package storage

import (
	"bytes"
	"fmt"
	"slices"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"google.golang.org/protobuf/proto"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// Thresholds. Each one is documented in the detector's guidance file.
const (
	// LargePayload is the payload size STO-001 flags.
	LargePayload = 256 << 10
	// ReferenceBytes is what a claim-check reference costs instead of the
	// payload it replaces.
	ReferenceBytes = 256

	// LongRunning is how long a run must be open for STO-003.
	LongRunning = 48 * time.Hour
	// StillGrowing is how recent the last event must be for STO-003 to call
	// the history growing.
	StillGrowing = 24 * time.Hour
	// ContinueAsNewEvery is the boundary STO-003's estimate assumes the fix
	// uses.
	ContinueAsNewEvery = 24 * time.Hour
	// LargeHistory is the history size below which STO-003 and STO-004
	// don't bother.
	LargeHistory = 256 << 10

	// LongWait is the shortest wait STO-004 and STO-005 consider.
	LongWait = time.Hour
	// WaitShare is the share of a run's open time that one wait must take
	// for STO-004.
	WaitShare = 0.8
	// IdleAfterResult is how long a run must sit idle after its last result
	// for STO-005.
	IdleAfterResult = time.Hour

	// MarkerShare is the share of history bytes that Local Activity markers
	// must reach for STO-006.
	MarkerShare = 0.5
	// SmallMarker is the size of a Local Activity marker with a small
	// result; STO-006 counts the bytes above it as the saving.
	SmallMarker = 1 << 10

	// LargeHeader is the header size STO-007 flags, and RepeatedHeaders the
	// number of Activities that must carry the same one.
	LargeHeader     = 1 << 10
	RepeatedHeaders = 3
)

func init() {
	detect.Register(sto001{})
	detect.Register(sto003{})
	detect.Register(sto004{})
	detect.Register(sto005{})
	detect.Register(sto006{})
	detect.Register(sto007{})
}

const bytesPerGB = 1e9

// end is when the run closed, or c.Now while it's open.
func end(ex *model.Execution, c *detect.Context) time.Time {
	if !ex.CloseTime.IsZero() {
		return ex.CloseTime
	}
	return c.Now
}

// closed reports whether the run has closed, so its history sits in
// Retained Storage for the retention period.
func closed(ex *model.Execution) bool { return !ex.CloseTime.IsZero() }

// gbHours prices bytes that sit in history from `from` until the run ends:
//
//	active   = bytes / 1e9 × hours from `from` to close (or to Now while open)
//	retained = bytes / 1e9 × retention hours, once the run has closed
//
// Retained is 0 while the run is open or the retention is unknown.
func gbHours(bytes float64, from time.Time, ex *model.Execution, c *detect.Context) (active, retained float64) {
	if bytes <= 0 {
		return 0, 0
	}
	if h := end(ex, c).Sub(from).Hours(); h > 0 {
		active = bytes / bytesPerGB * h
	}
	if closed(ex) && c.RetentionHours > 0 {
		retained = bytes / bytesPerGB * c.RetentionHours
	}
	return active, retained
}

func lastEventTime(ex *model.Execution) time.Time {
	if len(ex.Events) == 0 {
		return ex.StartTime
	}
	return ex.Events[len(ex.Events)-1].GetEventTime().AsTime()
}

func mib(b float64) string { return fmt.Sprintf("%.1f MiB", b/(1<<20)) }

// sto001 finds inputs, results, Signals, and Updates over LargePayload. A
// claim check stores the data elsewhere and passes a ReferenceBytes
// reference, so each large payload saves its size less the reference.
type sto001 struct{}

func (sto001) ID() string                    { return "STO-001" }
func (sto001) Confidence() detect.Confidence { return detect.High }

func (sto001) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	var ids []int64
	var total, active, retained float64
	add := func(id int64, size int, at time.Time) {
		if size <= LargePayload {
			return
		}
		saved := float64(size - ReferenceBytes)
		a, r := gbHours(saved, at, ex, c)
		ids = append(ids, id)
		total += saved
		active += a
		retained += r
	}
	add(ex.Start.EventID, ex.Start.InputBytes, ex.StartTime)
	for _, a := range ex.Activities {
		add(a.ScheduledEventID, a.InputBytes, a.ScheduledTime)
		if a.ClosedEventID != 0 {
			add(a.ClosedEventID, a.ResultBytes, a.ClosedTime)
		}
	}
	for _, s := range ex.Signals {
		add(s.EventID, s.InputBytes, s.Time)
	}
	for _, u := range ex.Updates {
		add(u.AcceptedEventID, u.InputBytes, u.Time)
	}
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	return []detect.Match{{
		EventIDs:             ids,
		Note:                 fmt.Sprintf("%d payloads over %d KiB, %s in all", len(ids), LargePayload>>10, mib(total)),
		ActiveGBHoursSaved:   active,
		RetainedGBHoursSaved: retained,
	}}
}

// sto003 finds runs open longer than LongRunning, whose history is still
// growing and over LargeHistory, with no Continue-As-New in the chain.
//
// Assuming the history grew evenly over the T hours it's been open, its
// Active Storage so far is bytes × T / 2. Continuing as new every B hours
// keeps only the current chunk active, about bytes × B / 2 over the same
// time, so the estimate is:
//
//	active saved = bytes / 1e9 × (T − B) / 2
//
// The earlier runs move to Retained Storage, where the whole history would
// end up anyway once the run closes, so retained isn't counted either way.
type sto003 struct{}

func (sto003) ID() string                    { return "STO-003" }
func (sto003) Confidence() detect.Confidence { return detect.High }

func (sto003) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	if closed(ex) || ex.Start.ContinuedFromRunID != "" || ex.HistoryBytes < LargeHistory {
		return nil
	}
	open := c.Now.Sub(ex.StartTime)
	if open < LongRunning || c.Now.Sub(lastEventTime(ex)) > StillGrowing {
		return nil
	}
	saved := float64(ex.HistoryBytes) / bytesPerGB * (open - ContinueAsNewEvery).Hours() / 2
	return []detect.Match{{
		EventIDs: []int64{ex.Start.EventID, ex.Events[len(ex.Events)-1].GetEventId()},
		Note: fmt.Sprintf("open %.1f days with %s of history and %d events, no Continue-As-New",
			open.Hours()/24, mib(float64(ex.HistoryBytes)), len(ex.Events)),
		ActiveGBHoursSaved: saved,
	}}
}

// pendingTimer returns the latest Timer that hasn't fired or been canceled.
func pendingTimer(ex *model.Execution) *model.Timer {
	for i := len(ex.Timers) - 1; i >= 0; i-- {
		if t := ex.Timers[i]; !t.Fired && !t.Canceled {
			return t
		}
	}
	return nil
}

// resultAfter returns the last Signal, Update, or Activity result recorded
// after a time, or 0 when there isn't one.
func resultAfter(ex *model.Execution, after time.Time) (int64, time.Time) {
	var id int64
	var at time.Time
	note := func(eid int64, t time.Time) {
		if t.After(after) && eid > id {
			id, at = eid, t
		}
	}
	for _, s := range ex.Signals {
		note(s.EventID, s.Time)
	}
	for _, u := range ex.Updates {
		note(u.AcceptedEventID, u.Time)
	}
	for _, a := range ex.Activities {
		if a.Outcome == enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED {
			note(a.ClosedEventID, a.ClosedTime)
		}
	}
	return id, at
}

// sto004 finds open runs that spend most of their time waiting, on a
// pending Timer or just idle, with a history over LargeHistory behind them.
// Continuing as new before the wait, or completing and starting the next
// step with a start delay, moves that history out of Active Storage for the
// length of the wait:
//
//	active saved = bytes / 1e9 × wait hours
//
// The wait is the pending Timer's duration, or the idle time so far.
type sto004 struct{}

func (sto004) ID() string                    { return "STO-004" }
func (sto004) Confidence() detect.Confidence { return detect.Medium }

func (sto004) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	if closed(ex) || ex.HistoryBytes < LargeHistory {
		return nil
	}
	open := c.Now.Sub(ex.StartTime)
	idle := c.Now.Sub(lastEventTime(ex))
	if open <= 0 || idle < LongWait || idle.Seconds() < WaitShare*open.Seconds() {
		return nil
	}
	wait := idle
	ids := []int64{ex.Events[len(ex.Events)-1].GetEventId()}
	if t := pendingTimer(ex); t != nil {
		// A result that arrived after the Timer started means the work is
		// done; that's STO-005.
		if id, _ := resultAfter(ex, t.StartTime); id != 0 {
			return nil
		}
		wait = max(wait, t.Duration)
		ids = []int64{t.StartedEventID}
	}
	return []detect.Match{{
		EventIDs:           ids,
		Note:               fmt.Sprintf("waiting %.1f hours with %s of history open", wait.Hours(), mib(float64(ex.HistoryBytes))),
		ActiveGBHoursSaved: float64(ex.HistoryBytes) / bytesPerGB * wait.Hours(),
	}}
}

// sto005 finds open runs whose work is done, a Signal, Update, or Activity
// result having arrived after a long Timer started, but which stay open
// until that Timer fires. Returning when the result arrives closes the run
// that much earlier:
//
//	active saved = bytes / 1e9 × hours from the result until the Timer fires
type sto005 struct{}

func (sto005) ID() string                    { return "STO-005" }
func (sto005) Confidence() detect.Confidence { return detect.High }

func (sto005) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	if closed(ex) {
		return nil
	}
	t := pendingTimer(ex)
	if t == nil || t.Duration < LongWait {
		return nil
	}
	resultID, resultAt := resultAfter(ex, t.StartTime)
	if resultID == 0 || c.Now.Sub(lastEventTime(ex)) < IdleAfterResult {
		return nil
	}
	fires := t.StartTime.Add(t.Duration)
	hours := fires.Sub(resultAt).Hours()
	if hours <= 0 {
		return nil
	}
	return []detect.Match{{
		EventIDs: []int64{t.StartedEventID, resultID},
		Note: fmt.Sprintf("result recorded at event %d, but Timer %s keeps the run open %.1f more hours",
			resultID, t.TimerID, hours),
		ActiveGBHoursSaved: float64(ex.HistoryBytes) / bytesPerGB * hours,
	}}
}

// localActivityMarkers are the marker names Local Activities record.
var localActivityMarkers = []string{"LocalActivity", "core_local_activity"}

// sto006 finds histories where Local Activity markers make up more than
// MarkerShare of the bytes. The saving counts each marker's bytes above
// SmallMarker, which is what smaller results, or regular Activities for work
// that doesn't fit Local Activities, would remove:
//
//	saved bytes = Σ max(marker bytes − SmallMarker, 0), priced with gbHours
type sto006 struct{}

func (sto006) ID() string                    { return "STO-006" }
func (sto006) Confidence() detect.Confidence { return detect.Medium }

func (sto006) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	if ex.HistoryBytes == 0 {
		return nil
	}
	var markerBytes, excess float64
	var ids []int64
	var active, retained float64
	for _, m := range ex.Markers {
		if !slices.Contains(localActivityMarkers, m.Name) {
			continue
		}
		markerBytes += float64(m.Bytes)
		if over := float64(m.Bytes - SmallMarker); over > 0 {
			excess += over
			ids = append(ids, m.EventID)
			at := ex.StartTime
			if m.WorkflowTask != nil {
				at = m.WorkflowTask.CompletedTime
			}
			a, r := gbHours(over, at, ex, c)
			active += a
			retained += r
		}
	}
	if markerBytes < MarkerShare*float64(ex.HistoryBytes) || len(ids) == 0 {
		return nil
	}
	return []detect.Match{{
		EventIDs: ids,
		Note: fmt.Sprintf("Local Activity markers are %.0f%% of %s of history",
			100*markerBytes/float64(ex.HistoryBytes), mib(float64(ex.HistoryBytes))),
		ActiveGBHoursSaved:   active,
		RetainedGBHoursSaved: retained,
	}}
}

// sto007 finds the same header of LargeHeader or more on RepeatedHeaders or
// more Activities. Trimming it, or storing it once, saves every copy but
// one:
//
//	saved bytes = (copies − 1) × header bytes, priced with gbHours from each
//	copy's Activity
type sto007 struct{}

func (sto007) ID() string                    { return "STO-007" }
func (sto007) Confidence() detect.Confidence { return detect.Medium }

func (sto007) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	scheduled := map[int64]*historypb.HistoryEvent{}
	for _, ev := range ex.Events {
		if ev.GetEventType() == enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED {
			scheduled[ev.GetEventId()] = ev
		}
	}
	type group struct {
		header []byte
		acts   []*model.Activity
	}
	var groups []*group
	for _, a := range ex.Activities {
		if a.HeaderBytes < LargeHeader {
			continue
		}
		ev := scheduled[a.ScheduledEventID]
		h, err := proto.MarshalOptions{Deterministic: true}.Marshal(ev.GetActivityTaskScheduledEventAttributes().GetHeader())
		if err != nil {
			continue
		}
		found := false
		for _, g := range groups {
			if bytes.Equal(g.header, h) {
				g.acts = append(g.acts, a)
				found = true
				break
			}
		}
		if !found {
			groups = append(groups, &group{header: h, acts: []*model.Activity{a}})
		}
	}
	var out []detect.Match
	for _, g := range groups {
		if len(g.acts) < RepeatedHeaders {
			continue
		}
		var ids []int64
		var active, retained float64
		for i, a := range g.acts {
			ids = append(ids, a.ScheduledEventID)
			if i == 0 {
				continue // one copy stays
			}
			ac, r := gbHours(float64(a.HeaderBytes), a.ScheduledTime, ex, c)
			active += ac
			retained += r
		}
		out = append(out, detect.Match{
			EventIDs: ids,
			Note: fmt.Sprintf("the same %.1f KiB header on %d Activity inputs",
				float64(g.acts[0].HeaderBytes)/1024, len(g.acts)),
			ActiveGBHoursSaved:   active,
			RetainedGBHoursSaved: retained,
		})
	}
	return out
}
