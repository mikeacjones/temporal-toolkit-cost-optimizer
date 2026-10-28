package activity

import (
	"testing"

	failurepb "go.temporal.io/api/failure/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/detecttest"
)

func TestACT003(t *testing.T) {
	detecttest.Run(t, act003{}, []detecttest.Case{
		{History: "act003_positive", Want: [][]int64{{5, 6}}},
		{History: "act003_negative"},
		{History: "wf001_positive"}, // never retried
	})
}

func TestACT006(t *testing.T) {
	detecttest.Run(t, act006{}, []detecttest.Case{
		{History: "act006_positive", Want: [][]int64{{5, 6, 7}}, Saved: 4},
		{History: "act006_negative"},
	})
}

func TestLA003(t *testing.T) {
	detecttest.Run(t, la003{}, []detecttest.Case{
		{History: "la003_positive", Want: [][]int64{{4, 7, 10}}, Saved: 3},
		{History: "la003_negative"},
		{History: "wf001_positive"}, // no Local Activities
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
