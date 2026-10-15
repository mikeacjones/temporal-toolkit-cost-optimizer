package config

import (
	"testing"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/kb"
)

// Every check this package reports has reviewed guidance with a snippet.
func TestGuidance(t *testing.T) {
	for _, id := range []string{"CFG-001", "CFG-003", "CFG-004", "CFG-005", "CFG-006", "CFG-007", "CFG-008", "CFG-010", "STO-002"} {
		g, err := kb.GuidanceFor(id)
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		if g.Title == "" || g.WhyItCosts == "" || g.Fix == "" || g.Tradeoffs == "" || len(g.Docs) == 0 {
			t.Errorf("%s: guidance is missing a required field", id)
		}
		if len(g.SnippetRefs) == 0 {
			t.Errorf("%s: no snippet", id)
		}
		for sdk := range g.SnippetRefs {
			if g.Snippet(sdk) == "" {
				t.Errorf("%s: snippet for %s doesn't load", id, sdk)
			}
		}
	}
}
