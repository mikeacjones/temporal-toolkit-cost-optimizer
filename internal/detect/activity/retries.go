package activity

import (
	"fmt"
	"strings"

	enumspb "go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// Thresholds for the retry detectors.
const (
	// RetryStormAttempts is the attempt count above which ACT-001 calls an
	// Activity's retries a storm. A cap at this many attempts would remove
	// the retries past it.
	RetryStormAttempts = 10
	// HopelessMinAttempts is how many attempts ACT-002 needs before it calls
	// identical failures hopeless.
	HopelessMinAttempts = 3
)

func init() {
	detect.Register(act001{})
	detect.Register(act002{})
	detect.Register(act003{})
	detect.Register(act006{})
}

// act001 finds Activities that ran more than RetryStormAttempts attempts.
type act001 struct{}

func (act001) ID() string                    { return "ACT-001" }
func (act001) Confidence() detect.Confidence { return detect.High }
func (act001) Features() []string            { return []string{"next-retry-delay", "activity-pause"} }

func (act001) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	var out []detect.Match
	for _, a := range ex.Activities {
		if a.Attempt <= RetryStormAttempts {
			continue
		}
		// Capping attempts at the threshold removes the retries past it.
		out = append(out, detect.Match{
			EventIDs:     []int64{a.ScheduledEventID, a.StartedEventID},
			Note:         fmt.Sprintf("%s ran %d attempts (%d retries)", a.Type, a.Attempt, a.Attempt-1),
			ActionsSaved: retryActions(c.Rules, a.Attempt-RetryStormAttempts),
			Keys:         []string{a.Type},
		})
	}
	return out
}

// act002 finds Activities that failed with the same error on their last two
// attempts and then gave up: retrying didn't change anything.
type act002 struct{}

func (act002) ID() string                    { return "ACT-002" }
func (act002) Confidence() detect.Confidence { return detect.Medium }
func (act002) Features() []string            { return []string{"next-retry-delay"} }

func (act002) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	var out []detect.Match
	for _, a := range ex.Activities {
		if a.Outcome != enumspb.EVENT_TYPE_ACTIVITY_TASK_FAILED || a.Attempt < HopelessMinAttempts {
			continue
		}
		if !sameFailure(a.LastFailure, a.Failure) {
			continue
		}
		kind := failureType(a.Failure)
		out = append(out, detect.Match{
			EventIDs: []int64{a.ScheduledEventID, a.StartedEventID, a.ClosedEventID},
			Note: fmt.Sprintf("%s failed %d times with the same %s error: %q", a.Type, a.Attempt, kind,
				truncate(a.Failure.GetMessage(), 80)),
			// Non-retryable, it would have stopped after the first attempt.
			ActionsSaved: retryActions(c.Rules, a.Attempt-1),
			Keys:         []string{a.Type, kind},
		})
	}
	return out
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

func sameFailure(a, b *failurepb.Failure) bool {
	if a == nil || b == nil {
		return false
	}
	return failureType(a) == failureType(b) && a.GetMessage() == b.GetMessage()
}

func failureType(f *failurepb.Failure) string {
	if t := f.GetApplicationFailureInfo().GetType(); t != "" {
		return t
	}
	if f.GetTimeoutFailureInfo() != nil {
		return "timeout"
	}
	return "untyped"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
