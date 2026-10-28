package activity

import (
	"testing"

	failurepb "go.temporal.io/api/failure/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/detecttest"
)

func TestACT001(t *testing.T) {
	detecttest.Run(t, act001{}, []detecttest.Case{
		// 12 attempts; a cap at 10 removes 2 retries.
		{History: "act001_positive", Want: [][]int64{{5, 6}}, Saved: 2},
		{History: "act001_negative"},
	})
}

func TestACT002(t *testing.T) {
	detecttest.Run(t, act002{}, []detecttest.Case{
		{History: "act002_positive", Want: [][]int64{{5, 6, 7}}, Saved: 3},
		{History: "act002_negative"},
		{History: "act001_positive"}, // succeeded in the end
	})
}

func TestACT003(t *testing.T) {
	detecttest.Run(t, act003{}, []detecttest.Case{
		{History: "act003_positive", Want: [][]int64{{5, 6}}},
		{History: "act003_negative"},
		{History: "act004_negative"}, // never retried
	})
}

func TestACT004(t *testing.T) {
	// 9.1 s at a 1 s timeout: 1 + 9.1/0.8 = 12 Heartbeats. At 60 s: 1.
	detecttest.Run(t, act004{}, []detecttest.Case{
		{History: "act004_positive", Want: [][]int64{{5, 7}}, Saved: 11},
		{History: "act004_negative"},
	})
}

func TestACT006(t *testing.T) {
	detecttest.Run(t, act006{}, []detecttest.Case{
		{History: "act006_positive", Want: [][]int64{{5, 6, 7}}, Saved: 4},
		{History: "act006_negative"},
	})
}

func TestLA001(t *testing.T) {
	detecttest.Run(t, la001{}, []detecttest.Case{
		{History: "la001_positive", Want: [][]int64{{7, 10}}, Saved: 4},
		{History: "la001_negative"},
		{History: "la003_negative"},
	})
}

func TestLA003(t *testing.T) {
	detecttest.Run(t, la003{}, []detecttest.Case{
		{History: "la003_positive", Want: [][]int64{{4, 7, 10}}, Saved: 3},
		{History: "la003_negative"},
		{History: "act004_negative"}, // no Local Activities
	})
}

func TestNotRegisteredMessages(t *testing.T) {
	for _, msg := range []string{
		"unable to find activityType=Ship. Supported types: [Step]",
		`Activity Type "Ship" is not registered with a worker. Known types are: Step`,
		"Activity function ship is not registered on this worker, available activities: step",
	} {
		if !notRegistered(failure(msg)) {
			t.Errorf("didn't match %q", msg)
		}
	}
	if notRegistered(failure("upstream unavailable")) {
		t.Error("matched an ordinary failure")
	}
}

func failure(msg string) *failurepb.Failure { return &failurepb.Failure{Message: msg} }
