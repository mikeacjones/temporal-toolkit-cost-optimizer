package shape

import (
	"testing"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/detecttest"
)

func TestWF001(t *testing.T) {
	detecttest.Run(t, wf001{}, []detecttest.Case{
		{History: "wf001_positive", Want: [][]int64{{1, 5}}, Saved: 1},
		{History: "wf001_negative"},
		{History: "tmr005_one_activity"},        // a Timer first
		{History: "chd001_positive"},            // the parent does more; the child is CHD-001's case
		{History: "nexus_handler_one_activity"}, // a Nexus Operation started it
		{History: "sig002_positive"},            // receives a Signal
	})
}

func TestTMR005(t *testing.T) {
	detecttest.Run(t, tmr005{}, []detecttest.Case{
		{History: "tmr005_positive", Want: [][]int64{{5, 6}}, Saved: 1},
		{History: "tmr005_one_activity", Want: [][]int64{{5, 6}}, Saved: 1},
		{History: "tmr005_negative"}, // the Timer lost a race with a Signal
		{History: "wf001_positive"},
	})
}

func TestCHD001(t *testing.T) {
	detecttest.Run(t, chd001{}, []detecttest.Case{
		{History: "chd001_positive", Want: [][]int64{{11}}, Saved: 2},
		{History: "chd001_negative"},
		{History: "wf001_positive"},
	})
}

func TestNEX002(t *testing.T) {
	detecttest.Run(t, nex002{}, []detecttest.Case{
		{History: "nex002_positive", Want: [][]int64{{1, 5}}, Saved: 1},
		{History: "nex002_negative"},
		{History: "wf001_positive"},
	})
}

func TestSIG002(t *testing.T) {
	detecttest.Run(t, sig002{}, []detecttest.Case{
		{History: "sig002_positive", Want: [][]int64{{1, 3}}, Saved: 1},
		{History: "sig002_negative"},        // 2 s after start
		{History: "sig002_signalwithstart"}, // already combined
		{History: "wf001_positive"},
	})
}
