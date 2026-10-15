package timer

import (
	"fmt"
	"sort"
	"time"

	enumspb "go.temporal.io/api/enums/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// Thresholds. Each one is documented in the detector's guidance file.
const (
	// PollingMinRepeats is how many Timer-then-same-Activity repeats make a
	// polling loop (TMR-001).
	PollingMinRepeats = 3
	// ShortTimer is the duration below which a Timer counts as very short
	// (TMR-002).
	ShortTimer = time.Second
	// CanceledTimerFloor is the share of sampled executions that must cancel
	// a Timer before TMR-003 reports it.
	CanceledTimerFloor = 0.5
	// DebounceFloor is the share of sampled executions that must start a
	// Timer right after a Signal before TMR-004 reports it.
	DebounceFloor = 0.8
)

func init() {
	detect.Register(tmr001{})
	detect.Register(tmr002{})
	detect.Register(tmr003{})
	detect.Register(tmr004{})
}

// timerActions is what one TimerStarted event costs.
func timerActions(c *detect.Context) float64 {
	if r, ok := c.Rules.Rule(enumspb.EVENT_TYPE_TIMER_STARTED); ok {
		return r.Actions
	}
	return 0
}

// tmr001 finds polling loops: a Timer, then the same Activity type, over and
// over. Polling with Activity retries costs 1 Action per poll instead of 2,
// so the saving is the loop's Timers.
type tmr001 struct{}

func (tmr001) ID() string                    { return "TMR-001" }
func (tmr001) Confidence() detect.Confidence { return detect.Medium }

func (tmr001) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	// Walk Timers and Activities in event order, pairing each fired Timer
	// with the next Activity scheduled after it.
	type cmd struct {
		id    int64
		timer *model.Timer
		act   *model.Activity
	}
	var cmds []cmd
	for _, t := range ex.Timers {
		cmds = append(cmds, cmd{id: t.StartedEventID, timer: t})
	}
	for _, a := range ex.Activities {
		cmds = append(cmds, cmd{id: a.ScheduledEventID, act: a})
	}
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].id < cmds[j].id })

	pairs := map[string][]int64{} // Activity type -> Timer started event IDs
	var order []string
	for i := 0; i+1 < len(cmds); i++ {
		t, next := cmds[i].timer, cmds[i+1].act
		if t == nil || next == nil || !t.Fired {
			continue
		}
		if _, ok := pairs[next.Type]; !ok {
			order = append(order, next.Type)
		}
		pairs[next.Type] = append(pairs[next.Type], t.StartedEventID)
	}
	var out []detect.Match
	for _, typ := range order {
		ids := pairs[typ]
		if len(ids) < PollingMinRepeats {
			continue
		}
		out = append(out, detect.Match{
			EventIDs:     ids,
			Note:         fmt.Sprintf("%d Timers, each followed by Activity %s", len(ids), typ),
			ActionsSaved: float64(len(ids)) * timerActions(c),
			Keys:         []string{typ},
		})
	}
	return out
}

// tmr002 finds Timers shorter than ShortTimer.
type tmr002 struct{}

func (tmr002) ID() string                    { return "TMR-002" }
func (tmr002) Confidence() detect.Confidence { return detect.High }

func (tmr002) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	var ids []int64
	for _, t := range ex.Timers {
		if t.Duration > 0 && t.Duration < ShortTimer {
			ids = append(ids, t.StartedEventID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return []detect.Match{{
		EventIDs:     ids,
		Note:         fmt.Sprintf("%d Timers shorter than %s", len(ids), ShortTimer),
		ActionsSaved: float64(len(ids)) * timerActions(c),
	}}
}

// tmr003 finds Timers whose wait something else ended: Timers that were
// canceled, and Timers still pending when a closed run ended. Both are what
// AwaitWithTimeout, a condition with a timeout, or a Selector race against
// a Timer leave behind when the other side wins. The Go SDK only cancels
// the AwaitWithTimeout Timer behind an SDK flag that's off by default in
// v1.49, so most Go histories show the second shape.
type tmr003 struct{}

func (tmr003) ID() string                    { return "TMR-003" }
func (tmr003) Confidence() detect.Confidence { return detect.High }
func (tmr003) MinPrevalence() float64        { return CanceledTimerFloor }

func (tmr003) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	var ids []int64
	var canceled, abandoned int
	for _, t := range ex.Timers {
		switch {
		case t.Canceled:
			ids = append(ids, t.StartedEventID, t.EndEventID)
			canceled++
		case !t.Fired && !ex.CloseTime.IsZero():
			ids = append(ids, t.StartedEventID)
			abandoned++
		}
	}
	n := canceled + abandoned
	if n == 0 {
		return nil
	}
	return []detect.Match{{
		EventIDs:     ids,
		Note:         fmt.Sprintf("%d Timers ended by something else: %d canceled, %d still pending at close", n, canceled, abandoned),
		ActionsSaved: float64(n) * timerActions(c),
	}}
}

// tmr004 finds Timers started in the Workflow Task that handled a Signal:
// the shape of debouncing Signals with a Timer.
type tmr004 struct{}

func (tmr004) ID() string                    { return "TMR-004" }
func (tmr004) Confidence() detect.Confidence { return detect.Medium }
func (tmr004) MinPrevalence() float64        { return DebounceFloor }

func (tmr004) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	var ids []int64
	for _, t := range ex.Timers {
		if t.WorkflowTask == nil {
			continue
		}
		for _, et := range t.WorkflowTask.After {
			if et == enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED {
				ids = append(ids, t.StartedEventID)
				break
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for _, s := range ex.Signals {
		if !seen[s.Name] {
			seen[s.Name] = true
			names = append(names, s.Name)
		}
	}
	return []detect.Match{{
		EventIDs:     ids,
		Note:         fmt.Sprintf("%d Timers started right after a Signal", len(ids)),
		ActionsSaved: float64(len(ids)) * timerActions(c),
		Keys:         names,
	}}
}
