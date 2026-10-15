package activity

import (
	"fmt"
	"math"
	"sort"
	"time"

	enumspb "go.temporal.io/api/enums/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// Thresholds for the heartbeat and tiny-Activity detectors.
const (
	// HeartbeatThrottle is the share of the Heartbeat timeout the SDKs wait
	// between Heartbeats they send to the server.
	HeartbeatThrottle = 0.8
	// SuggestedHeartbeatTimeout is the Heartbeat timeout ACT-004 compares
	// against. Shorter timeouts on long Activities send many Heartbeats.
	SuggestedHeartbeatTimeout = time.Minute
	// MinHeartbeats is how many Heartbeats one Activity must send before
	// ACT-004 reports it.
	MinHeartbeats = 10
	// TinyActivityChain is how many tiny Activities in a row ACT-005 needs.
	TinyActivityChain = 5
	// TinyActivityDuration is the p50 duration under which an Activity counts
	// as tiny.
	TinyActivityDuration = 50 * time.Millisecond
)

func init() {
	detect.Register(act004{})
	detect.Register(act005{})
}

// act004 estimates Heartbeat Actions for Activities that heartbeat often
// relative to how long they run. Heartbeats aren't in history, so this
// assumes the code heartbeats continuously and the SDK sends one every
// HeartbeatThrottle × timeout, plus the first one at once.
type act004 struct{}

func (act004) ID() string                    { return "ACT-004" }
func (act004) Confidence() detect.Confidence { return detect.Advisory }

func (act004) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	per := heartbeatActions(c.Rules)
	var out []detect.Match
	for _, a := range ex.Activities {
		d := a.Duration()
		if a.HeartbeatTimeout <= 0 || a.HeartbeatTimeout >= SuggestedHeartbeatTimeout || d <= 0 {
			continue
		}
		now := estimatedHeartbeats(d, a.HeartbeatTimeout)
		if now < MinHeartbeats {
			continue
		}
		later := estimatedHeartbeats(d, SuggestedHeartbeatTimeout)
		out = append(out, detect.Match{
			EventIDs: []int64{a.ScheduledEventID, a.ClosedEventID},
			Note: fmt.Sprintf("%s ran %s with a %s Heartbeat timeout: about %d Heartbeats if it heartbeats throughout (estimate)",
				a.Type, d.Round(100*time.Millisecond), a.HeartbeatTimeout, now),
			ActionsSaved: float64(now-later) * per,
			Keys:         []string{a.Type},
		})
	}
	return out
}

// estimatedHeartbeats is the first Heartbeat plus one per throttle interval.
func estimatedHeartbeats(d, timeout time.Duration) int {
	interval := time.Duration(float64(timeout) * HeartbeatThrottle)
	if interval <= 0 {
		return 0
	}
	return 1 + int(d/interval)
}

// act005 finds runs of TinyActivityChain or more Activities scheduled one at
// a time, each right after the previous one completed, with nothing else
// happening between them, and a p50 duration under TinyActivityDuration.
type act005 struct{}

func (act005) ID() string                    { return "ACT-005" }
func (act005) Confidence() detect.Confidence { return detect.Advisory }

func (act005) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	var out []detect.Match
	var chain []*model.Activity
	flush := func() {
		if len(chain) >= TinyActivityChain && medianDuration(chain) < TinyActivityDuration {
			ids := make([]int64, len(chain))
			for i, a := range chain {
				ids[i] = a.ScheduledEventID
			}
			out = append(out, detect.Match{
				EventIDs: ids,
				Note: fmt.Sprintf("%d Activities in a row, one per Workflow Task, p50 %s (%s … %s)",
					len(chain), medianDuration(chain).Round(time.Millisecond), chain[0].Type, chain[len(chain)-1].Type),
				ActionsSaved: float64(len(chain)-1) * scheduleActions(c.Rules),
				Keys:         chainTypes(chain),
			})
		}
		chain = chain[:0]
	}
	for _, a := range ex.Activities {
		if !alone(a) {
			flush()
			continue
		}
		if n := len(chain); n > 0 && !follows(chain[n-1], a) {
			flush()
		}
		chain = append(chain, a)
	}
	flush()
	return out
}

// alone reports whether the Activity was the only command of its Workflow
// Task and succeeded on its first attempt.
func alone(a *model.Activity) bool {
	return a.WorkflowTask != nil && len(a.WorkflowTask.Commands) == 1 &&
		a.Outcome == enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED && a.Attempt == 1
}

// follows reports whether next was scheduled by the Workflow Task that
// handled prev's completion, with nothing else delivered in between.
func follows(prev, next *model.Activity) bool {
	if next.ScheduledEventID < prev.ClosedEventID {
		return false
	}
	for _, et := range next.WorkflowTask.After {
		if et != enumspb.EVENT_TYPE_ACTIVITY_TASK_STARTED && et != enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED {
			return false
		}
	}
	return true
}

func medianDuration(as []*model.Activity) time.Duration {
	ds := make([]time.Duration, len(as))
	for i, a := range as {
		ds[i] = a.Duration()
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[int(math.Ceil(float64(len(ds))/2))-1]
}

func chainTypes(as []*model.Activity) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range as {
		if !seen[a.Type] {
			seen[a.Type] = true
			out = append(out, a.Type)
		}
	}
	sort.Strings(out)
	return out
}
