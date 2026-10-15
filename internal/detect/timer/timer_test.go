package timer

import (
	"testing"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/detecttest"
)

func TestTMR001(t *testing.T) {
	detecttest.Run(t, tmr001{}, []detecttest.Case{
		{History: "tmr001_positive", Want: [][]int64{{5, 16, 27}}, Saved: 3},
		{History: "tmr001_negative"},
		// Two Timer-then-Activity pairs aren't a loop yet.
		{History: "tmr004_positive"},
	})
}

func TestTMR002(t *testing.T) {
	detecttest.Run(t, tmr002{}, []detecttest.Case{
		{History: "tmr002_positive", Want: [][]int64{{5}}, Saved: 1},
		{History: "tmr002_negative"},
		{History: "tmr001_positive"},
	})
}

func TestTMR003(t *testing.T) {
	detecttest.Run(t, tmr003{}, []detecttest.Case{
		// Go's AwaitWithTimeout leaves the Timer pending when the condition wins.
		{History: "tmr003_positive", Want: [][]int64{{5}}, Saved: 1},
		{History: "tmr003_negative"},
		// A Selector race that cancels the Timer when the Signal wins.
		{History: "sto005_negative", Want: [][]int64{{5, 10}}, Saved: 1},
		{History: "tmr001_positive"},
	})
}

func TestTMR004(t *testing.T) {
	detecttest.Run(t, tmr004{}, []detecttest.Case{
		{History: "tmr004_positive", Want: [][]int64{{9, 24}}, Saved: 2},
		{History: "tmr004_negative"},
		// The Timer starts before the Signal arrives, not after it.
		{History: "tmr003_positive"},
	})
}
