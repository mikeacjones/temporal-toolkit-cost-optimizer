package shape

import (
	"fmt"
	"sort"
	"time"

	enumspb "go.temporal.io/api/enums/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// Thresholds for the Signal detectors.
const (
	// BurstWindow and BurstMin define a burst for SIG-001: BurstMin or more
	// Signals of the same name, from the same sender, within BurstWindow of
	// the first.
	BurstWindow = time.Second
	BurstMin    = 3
	// StartWindow is how soon after start a Signal counts as part of the
	// start for SIG-002.
	StartWindow = time.Second
	// SmallRunEvents and TargetRunEvents set CAN-001: a run that continued
	// as new with fewer than SmallRunEvents events is small, and the saving
	// assumes runs could grow to TargetRunEvents, well under the 51,200
	// event limit.
	SmallRunEvents  = 200
	TargetRunEvents = 2000
)

func init() {
	detect.Register(sig001{})
	detect.Register(sig002{})
	detect.Register(can001{})
}

// sig001 finds bursts of the same Signal that the sender could batch.
type sig001 struct{}

func (sig001) ID() string                    { return "SIG-001" }
func (sig001) Confidence() detect.Confidence { return detect.Medium }

func (sig001) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	type key struct{ name, identity string }
	groups := map[key][]*model.Signal{}
	var keys []key
	for _, s := range ex.Signals {
		if fromSignalWithStart(ex, s) {
			continue // billed with the start
		}
		k := key{s.Name, s.Sender()}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], s)
	}
	per := eventActions(c, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED)
	var out []detect.Match
	for _, k := range keys {
		sigs := groups[k]
		sort.Slice(sigs, func(i, j int) bool { return sigs[i].EventID < sigs[j].EventID })
		for i := 0; i < len(sigs); {
			j := i
			for j+1 < len(sigs) && sigs[j+1].Time.Sub(sigs[i].Time) <= BurstWindow {
				j++
			}
			if n := j - i + 1; n >= BurstMin {
				ids := make([]int64, 0, n)
				for _, s := range sigs[i : j+1] {
					ids = append(ids, s.EventID)
				}
				out = append(out, detect.Match{
					EventIDs: ids,
					Note: fmt.Sprintf("%d %q Signals from %s within %s", n, k.name, k.identity,
						sigs[j].Time.Sub(sigs[i].Time).Round(time.Millisecond)),
					ActionsSaved: float64(n-1) * per,
					Keys:         []string{k.name},
				})
			}
			i = j + 1
		}
	}
	return out
}

// sig002 finds a Signal sent right after start by the client that started
// the Workflow, which Signal-With-Start would combine. Update-With-Start
// bills the start and the Update separately, so Updates sent right after
// start save no Actions and aren't flagged.
type sig002 struct{}

func (sig002) ID() string                    { return "SIG-002" }
func (sig002) Confidence() detect.Confidence { return detect.Medium }

func (sig002) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	sws, ok := operationActions(c, "signal_with_start")
	if !ok {
		return nil
	}
	saved := startActions(c) + eventActions(c, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED) - sws
	if saved <= 0 || len(ex.Events) == 0 {
		return nil
	}
	start := ex.Events[0].GetEventTime().AsTime()
	for _, s := range ex.Signals {
		if fromSignalWithStart(ex, s) {
			return nil // already Signal-With-Start
		}
		if s.Time.Sub(start) > StartWindow {
			break
		}
		if s.Identity == "" || s.Identity != ex.Start.Identity {
			continue
		}
		return []detect.Match{{
			EventIDs: []int64{ex.Start.EventID, s.EventID},
			Note: fmt.Sprintf("%q Signal %s after start, from the client that started the Workflow",
				s.Name, s.Time.Sub(start).Round(time.Millisecond)),
			ActionsSaved: saved,
			Keys:         []string{ex.WorkflowType, s.Name},
		}}
	}
	return nil
}

// can001 finds runs that continue as new while their history is still
// small, so each run pays for a start it didn't need yet.
type can001 struct{}

func (can001) ID() string                    { return "CAN-001" }
func (can001) Confidence() detect.Confidence { return detect.Medium }

func (can001) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	n := len(ex.Events)
	if !ex.ContinuedAsNew || n == 0 || n >= SmallRunEvents {
		return nil
	}
	last := ex.Events[n-1]
	// Runs this size could carry on to TargetRunEvents, so only
	// n/TargetRunEvents of today's starts are needed.
	saved := startActions(c) * (1 - float64(n)/TargetRunEvents)
	return []detect.Match{{
		EventIDs:     []int64{last.GetEventId()},
		Note:         fmt.Sprintf("continued as new after %d events (%d bytes of history)", n, ex.HistoryBytes),
		ActionsSaved: saved,
		Keys:         []string{ex.WorkflowType},
	}}
}
