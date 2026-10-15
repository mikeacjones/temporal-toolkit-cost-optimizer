package metrics

import (
	"fmt"
	"testing"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/kb"
)

func TestEveryCheckHasGuidance(t *testing.T) {
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("MET-%03d", i)
		g, err := kb.GuidanceFor(id)
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		if g.Title == "" || g.WhyItCosts == "" || g.Fix == "" || g.Tradeoffs == "" || g.Body == "" {
			t.Errorf("%s: guidance is missing a required field", id)
		}
		for sdk := range g.SnippetRefs {
			if g.Snippet(sdk) == "" {
				t.Errorf("%s: snippet for %s doesn't resolve", id, sdk)
			}
		}
	}
}
