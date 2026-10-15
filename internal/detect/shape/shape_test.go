package shape

import (
	"testing"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/detecttest"
)

func TestWF001(t *testing.T) {
	detecttest.Run(t, wf001{}, []detecttest.Case{
		{History: "wf001_positive", Want: [][]int64{{1, 5}}, Saved: 1},
		{History: "wf001_negative"},
		{History: "wf002_positive"},  // a Timer first: WF-002's case
		{History: "chd001_positive"}, // the parent does more; the child is CHD-001's case
		{History: "nex001_positive"}, // a Nexus handler: NEX-001's case
		{History: "can001_positive"}, // continued as new
		{History: "sig002_positive"}, // receives a Signal
	})
}

func TestWF002(t *testing.T) {
	detecttest.Run(t, wf002{}, []detecttest.Case{
		{History: "wf002_positive", Want: [][]int64{{5, 10}}, Saved: 2},
		{History: "wf002_negative"},
		{History: "wf001_positive"},
		{History: "tmr005_negative"},
	})
}

func TestTMR005(t *testing.T) {
	detecttest.Run(t, tmr005{}, []detecttest.Case{
		{History: "tmr005_positive", Want: [][]int64{{5, 6}}, Saved: 1},
		{History: "tmr005_negative"}, // the Timer lost a race with a Signal
		{History: "wf002_positive"},  // left to WF-002
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

func TestNEX001(t *testing.T) {
	detecttest.Run(t, nex001{}, []detecttest.Case{
		{History: "nex001_positive", Want: [][]int64{{1, 5}}, Saved: 1},
		{History: "nex001_negative"}, // the handler does real work
		{History: "wf001_positive"},  // not started by Nexus
	})
}

func TestNEX002(t *testing.T) {
	detecttest.Run(t, nex002{}, []detecttest.Case{
		{History: "nex002_positive", Want: [][]int64{{1, 5}}, Saved: 1},
		{History: "nex002_negative"},
		{History: "wf001_positive"},
	})
}

func TestSIG001(t *testing.T) {
	detecttest.Run(t, sig001{}, []detecttest.Case{
		{History: "sig001_positive", Want: [][]int64{{3, 4, 5, 6, 7}}, Saved: 4},
		{History: "sig001_negative"}, // 1.2 s apart
		{History: "sig002_positive"},
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

func TestCAN001(t *testing.T) {
	detecttest.Run(t, can001{}, []detecttest.Case{
		{History: "can001_positive", Want: [][]int64{{11}}, Saved: 1 - 11.0/TargetRunEvents},
		{History: "can001_negative"}, // 245 events before continuing
		{History: "can001_end"},      // the last run completes
	})
}
