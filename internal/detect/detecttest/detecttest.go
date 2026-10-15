// Package detecttest loads golden histories and checks detectors against
// them. Every detector has a positive and a negative golden history in
// testdata/histories.
package detecttest

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"go.temporal.io/sdk/client"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/billing"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// Root returns the module root, found by walking up to go.mod.
func Root(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// Golden loads testdata/histories/<name>.json and normalizes it. When a
// file named <name>.child-<initiatedEventID>.json exists, it's attached as
// that Child Workflow's history.
func Golden(t testing.TB, name string) *model.Execution {
	t.Helper()
	dir := filepath.Join(Root(t), "testdata", "histories")
	ex := load(t, filepath.Join(dir, name+".json"), name)
	children, _ := filepath.Glob(filepath.Join(dir, name+".child-*.json"))
	for _, path := range children {
		var id int64
		base := filepath.Base(path)
		if _, err := fmt.Sscanf(base[len(name)+len(".child-"):], "%d.json", &id); err != nil {
			t.Fatalf("%s: child file name needs the initiated event ID", base)
		}
		if ex.ChildExecutions == nil {
			ex.ChildExecutions = map[int64]*model.Execution{}
		}
		ex.ChildExecutions[id] = load(t, path, base)
	}
	return ex
}

func load(t testing.TB, path, id string) *model.Execution {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h, err := client.HistoryFromJSON(f, client.HistoryJSONOptions{})
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return model.Normalize("golden-"+id, "run", h)
}

// Context returns a detector context with the embedded billing rules, a
// 30-day retention, and Now set to the given time, or the current
// time when now is zero.
func Context(t testing.TB, now time.Time) *detect.Context {
	t.Helper()
	r, err := billing.LoadRules()
	if err != nil {
		t.Fatal(err)
	}
	if now.IsZero() {
		now = time.Now()
	}
	return &detect.Context{Rules: r, RetentionHours: 30 * 24, Now: now}
}

// Case is one detector run against one golden history.
type Case struct {
	History string
	// Want lists the event IDs of each expected match, in order. Nil means
	// no matches.
	Want [][]int64
	// Saved is the total ActionsSaved across matches.
	Saved float64
}

// Run checks a detector against golden histories.
func Run(t *testing.T, d detect.Detector, cases []Case) {
	t.Helper()
	c := Context(t, time.Time{})
	for _, tc := range cases {
		t.Run(d.ID()+"/"+tc.History, func(t *testing.T) {
			got := d.Detect(Golden(t, tc.History), c)
			if len(got) != len(tc.Want) {
				t.Fatalf("got %d matches %+v, want %d", len(got), got, len(tc.Want))
			}
			var saved float64
			for i, m := range got {
				if !slices.Equal(m.EventIDs, tc.Want[i]) {
					t.Errorf("match %d: event IDs %v, want %v", i, m.EventIDs, tc.Want[i])
				}
				saved += m.ActionsSaved
			}
			if saved != tc.Saved {
				t.Errorf("saved %v Actions, want %v", saved, tc.Saved)
			}
		})
	}
}
