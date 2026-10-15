package kb

import (
	"testing"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	_ "github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/all"
)

func TestEveryDetectorHasGuidance(t *testing.T) {
	for _, d := range detect.All() {
		g, err := GuidanceFor(d.ID())
		if err != nil {
			t.Errorf("%s: %v", d.ID(), err)
			continue
		}
		if g.Title == "" || g.WhyItCosts == "" || g.Fix == "" || g.Tradeoffs == "" {
			t.Errorf("%s: guidance is missing a required field", d.ID())
		}
		if g.Snippet("go") == "" {
			t.Errorf("%s: no Go snippet", d.ID())
		}
	}
}
