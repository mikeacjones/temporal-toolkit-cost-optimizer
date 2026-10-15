package searchattr

import (
	"testing"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/detecttest"
)

func TestSA002(t *testing.T) {
	detecttest.Run(t, sa002{}, []detecttest.Case{
		{History: "sa002_positive", Want: [][]int64{{11, 12, 13}}, Saved: 2},
		{History: "sa002_negative"},
		{History: "sa_changeversion"},
		{History: "sa004_positive"},
	})
}

func TestSA004(t *testing.T) {
	detecttest.Run(t, sa004{}, []detecttest.Case{
		{History: "sa004_positive", Want: [][]int64{{11}}, Saved: 1},
		{History: "sa004_negative"},
		{History: "sa_changeversion"},
		{History: "sa002_positive"},
	})
}
