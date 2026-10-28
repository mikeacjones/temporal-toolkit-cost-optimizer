package shape

import (
	"fmt"
	"time"

	enumspb "go.temporal.io/api/enums/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// StartWindow is how soon after start a Signal counts as part of the start
// for SIG-002.
const StartWindow = time.Second

func init() {
	detect.Register(sig002{})
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
