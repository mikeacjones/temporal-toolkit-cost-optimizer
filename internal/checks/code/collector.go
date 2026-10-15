package code

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/analyze"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/estimate"
)

// collector runs the code checks as part of an analysis.
type collector struct{ paths []string }

// New returns a collector that scans the given repo paths. With no paths it
// returns no findings.
func New(repoPaths []string) analyze.Collector { return &collector{paths: repoPaths} }

func (c *collector) Name() string { return "Code" }

// Collect scans each repo. Evidence paths are prefixed with the repo's
// directory name when there's more than one repo, so they stay distinct.
func (c *collector) Collect(ctx context.Context, _ *analyze.Env) ([]estimate.Finding, error) {
	merged := map[string]*estimate.Finding{}
	var order []string
	for _, root := range c.paths {
		info, err := os.Stat(root)
		if err != nil {
			return nil, fmt.Errorf("repo %s: %w", root, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("repo %s isn't a directory", root)
		}
		fs, err := Scan(ctx, root)
		if err != nil {
			return nil, fmt.Errorf("repo %s: %w", root, err)
		}
		for _, f := range fs {
			if len(c.paths) > 1 {
				prefix := filepath.Base(filepath.Clean(root))
				for i := range f.Evidence {
					if f.Evidence[i].File != "" {
						f.Evidence[i].File = prefix + "/" + f.Evidence[i].File
					}
				}
			}
			if m, ok := merged[f.ID]; ok {
				m.Evidence = append(m.Evidence, f.Evidence...)
				m.Keys = append(m.Keys, f.Keys...)
				continue
			}
			f := f
			merged[f.ID] = &f
			order = append(order, f.ID)
		}
	}
	out := make([]estimate.Finding, 0, len(order))
	for _, id := range order {
		out = append(out, *merged[id])
	}
	return out, nil
}
