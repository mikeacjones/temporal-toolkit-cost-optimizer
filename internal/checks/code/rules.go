// Package code runs the static code checks (CODE-001 to CODE-010, PLAN.md
// section 6.9) over a user's repo. Each rule is described in
// kb/checks/<ID>.yaml; the Go matchers here read Go source with go/parser and
// go/ast, without a model and without building the code.
package code

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	kbdata "github.com/mikeacjones/temporal-toolkit-cost-optimizer/kb"
)

// Rule is one kb/checks/<ID>.yaml file. Only the fields the Go matchers use
// are typed; the rest documents the rule for later languages.
type Rule struct {
	ID         string                    `yaml:"id"`
	Title      string                    `yaml:"title"`
	Confidence string                    `yaml:"confidence"`
	Summary    string                    `yaml:"summary"`
	Match      map[string]any            `yaml:"match"`
	Languages  map[string]map[string]any `yaml:"languages"`
	Parameters map[string]float64        `yaml:"parameters"`
	Features   []string                  `yaml:"features"`
	// KnownIssues is used by CODE-008.
	KnownIssues []KnownIssue `yaml:"known_issues"`
}

// KnownIssue is an SDK release with a confirmed cost bug.
type KnownIssue struct {
	ID          string            `yaml:"id"`
	Description string            `yaml:"description"`
	SDKs        []string          `yaml:"sdks"`
	FixedIn     map[string]string `yaml:"fixed_in"`
	Confirmed   bool              `yaml:"confirmed"`
	Source      string            `yaml:"source"`
}

// GoStatus is the rule's Go status: implemented, later, or not-applicable.
func (r Rule) GoStatus() string {
	s, _ := r.Languages["go"]["status"].(string)
	return s
}

// names returns a string list from the Match section, for name lists such
// as CODE-004's encrypt_names.
func (r Rule) names(key string) []string {
	raw, _ := r.Match[key].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, strings.ToLower(s))
		}
	}
	return out
}

var (
	rulesOnce sync.Once
	rules     map[string]Rule
	rulesErr  error
)

// LoadRules reads the embedded rules, keyed by ID.
func LoadRules() (map[string]Rule, error) {
	rulesOnce.Do(func() { rules, rulesErr = loadRules(kbdata.FS) })
	return rules, rulesErr
}

func loadRules(fsys fs.FS) (map[string]Rule, error) {
	entries, err := fs.ReadDir(fsys, "checks")
	if err != nil {
		return nil, err
	}
	out := map[string]Rule{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join("checks", e.Name()))
		if err != nil {
			return nil, err
		}
		var r Rule
		if err := yaml.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("checks/%s: %w", e.Name(), err)
		}
		if want := strings.TrimSuffix(e.Name(), ".yaml"); r.ID != want {
			return nil, fmt.Errorf("checks/%s: id %q doesn't match the file name", e.Name(), r.ID)
		}
		switch r.Confidence {
		case "high", "medium", "advisory":
		default:
			return nil, fmt.Errorf("checks/%s: unknown confidence %q", e.Name(), r.Confidence)
		}
		out[r.ID] = r
	}
	return out, nil
}

// RuleIDs returns the rule IDs, sorted.
func RuleIDs() []string {
	rs, _ := LoadRules()
	ids := make([]string, 0, len(rs))
	for id := range rs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
