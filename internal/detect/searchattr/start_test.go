package searchattr

import (
	"testing"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/detecttest"
)

func TestSA001(t *testing.T) {
	detecttest.Run(t, sa001{}, []detecttest.Case{
		{History: "sa001_positive", Want: [][]int64{{5}}, Saved: 1},
		{History: "sa001_negative"},
		{History: "sa001_changeversion"}, // TemporalChangeVersion isn't billed
		{History: "sa002_positive"},
	})
}

func TestSA003(t *testing.T) {
	detecttest.Run(t, sa003{}, []detecttest.Case{
		{History: "sa003_positive", Want: [][]int64{{11, 18}}, Saved: 1},
		{History: "sa003_negative"}, // the first value stayed for 3 s
		{History: "sa001_positive"},
		{History: "sa002_positive"},
	})
}
