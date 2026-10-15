// Package kb reads the embedded knowledge base: billing rules and fix
// guidance.
package kb

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	kbdata "github.com/mikeacjones/temporal-toolkit-cost-optimizer/kb"
)

// Guidance is one detector's reviewed fix guidance.
type Guidance struct {
	ID          string            `yaml:"id"`
	Title       string            `yaml:"title"`
	WhyItCosts  string            `yaml:"why_it_costs"`
	Fix         string            `yaml:"fix"`
	Tradeoffs   string            `yaml:"tradeoffs"`
	Docs        []DocRef          `yaml:"docs"`
	SnippetRefs map[string]string `yaml:"snippets"`
	Locator     struct {
		SDKCalls map[string][]string `yaml:"sdk_calls"`
	} `yaml:"locator"`

	// Body is the Markdown after the front matter.
	Body string `yaml:"-"`
}

// DocRef points at a page and heading in the knowledge base.
type DocRef struct {
	KB string `yaml:"kb"`
}

// Snippet returns the before-and-after snippet for an SDK, or "" when there
// isn't one.
func (g *Guidance) Snippet(sdk string) string {
	ref, ok := g.SnippetRefs[sdk]
	if !ok {
		return ""
	}
	b, err := fs.ReadFile(kbdata.FS, path.Join("guidance", ref))
	if err != nil {
		return ""
	}
	return string(b)
}

var (
	loadOnce sync.Once
	guidance map[string]*Guidance
	loadErr  error
)

// AllGuidance returns every guidance file, keyed by detector ID.
func AllGuidance() (map[string]*Guidance, error) {
	loadOnce.Do(func() { guidance, loadErr = loadGuidance(kbdata.FS) })
	return guidance, loadErr
}

// GuidanceFor returns the guidance for one detector ID.
func GuidanceFor(id string) (*Guidance, error) {
	all, err := AllGuidance()
	if err != nil {
		return nil, err
	}
	g, ok := all[id]
	if !ok {
		return nil, fmt.Errorf("no guidance for %s", id)
	}
	return g, nil
}

// GuidanceIDs returns the IDs that have guidance, sorted.
func GuidanceIDs() []string {
	all, _ := AllGuidance()
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func loadGuidance(fsys fs.FS) (map[string]*Guidance, error) {
	entries, err := fs.ReadDir(fsys, "guidance")
	if err != nil {
		return nil, err
	}
	out := map[string]*Guidance{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join("guidance", e.Name()))
		if err != nil {
			return nil, err
		}
		g, err := parseGuidance(b)
		if err != nil {
			return nil, fmt.Errorf("guidance/%s: %w", e.Name(), err)
		}
		if want := strings.TrimSuffix(e.Name(), ".md"); g.ID != want {
			return nil, fmt.Errorf("guidance/%s: id %q doesn't match the file name", e.Name(), g.ID)
		}
		out[g.ID] = g
	}
	return out, nil
}

func parseGuidance(b []byte) (*Guidance, error) {
	const sep = "---\n"
	if !bytes.HasPrefix(b, []byte(sep)) {
		return nil, fmt.Errorf("missing front matter")
	}
	rest := b[len(sep):]
	end := bytes.Index(rest, []byte("\n"+sep))
	if end < 0 {
		return nil, fmt.Errorf("unterminated front matter")
	}
	g := &Guidance{}
	if err := yaml.Unmarshal(rest[:end+1], g); err != nil {
		return nil, err
	}
	g.Body = strings.TrimSpace(string(rest[end+1+len(sep):]))
	return g, nil
}
