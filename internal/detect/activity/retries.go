package activity

import (
	"fmt"
	"strings"

	enumspb "go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

func init() {
	detect.Register(act003{})
	detect.Register(act006{})
}

// act003 finds Activities with no limit on attempts or total time that
// retried at least once. It's a risk, not a saving: nothing stops the next
// outage from retrying forever.
type act003 struct{}

func (act003) ID() string                    { return "ACT-003" }
func (act003) Confidence() detect.Confidence { return detect.Advisory }

func (act003) Detect(ex *model.Execution, _ *detect.Context) []detect.Match {
	var out []detect.Match
	for _, a := range ex.Activities {
		if a.Attempt < 2 || a.RetryPolicy == nil || a.RetryPolicy.GetMaximumAttempts() != 0 || a.ScheduleToCloseTimeout != 0 {
			continue
		}
		out = append(out, detect.Match{
			EventIDs: []int64{a.ScheduledEventID, a.StartedEventID},
			Note:     fmt.Sprintf("%s retried (%d attempts) with no maximum attempts and no schedule-to-close timeout", a.Type, a.Attempt),
			Keys:     []string{a.Type},
		})
	}
	return out
}

// act006 finds Activities whose earlier attempt failed because the Worker
// that picked it up didn't have the Activity registered, then succeeded.
type act006 struct{}

func (act006) ID() string                    { return "ACT-006" }
func (act006) Confidence() detect.Confidence { return detect.Medium }

func (act006) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	var out []detect.Match
	for _, a := range ex.Activities {
		if a.Outcome != enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED || a.Attempt < 2 || !notRegistered(a.LastFailure) {
			continue
		}
		out = append(out, detect.Match{
			EventIDs:     []int64{a.ScheduledEventID, a.StartedEventID, a.ClosedEventID},
			Note:         fmt.Sprintf("%s reached a Worker that doesn't register it, then succeeded on attempt %d", a.Type, a.Attempt),
			ActionsSaved: retryActions(c.Rules, a.Attempt-1),
			Keys:         []string{a.Type},
		})
	}
	return out
}

// notRegistered matches each SDK's error for an Activity type the Worker
// doesn't have.
func notRegistered(f *failurepb.Failure) bool {
	if f == nil {
		return false
	}
	if f.GetApplicationFailureInfo().GetType() == "ActivityNotRegisteredError" {
		return true
	}
	msg := strings.ToLower(f.GetMessage())
	return strings.Contains(msg, "unable to find activitytype=") || // Go
		strings.Contains(msg, "is not registered") // Java, Python, TypeScript, .NET
}
