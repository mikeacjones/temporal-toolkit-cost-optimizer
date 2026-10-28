package activity

import (
	"fmt"
	"time"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// Thresholds for the heartbeat detector.
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
)

func init() {
	detect.Register(act004{})
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
