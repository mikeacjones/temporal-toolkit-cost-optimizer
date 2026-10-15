// Package detect defines history detectors and keeps the registry of them.
// Each detector is a pure function of one normalized execution and a
// Context. Detectors live in subpackages, one per group, and register
// themselves in init; internal/detect/all imports every group.
package detect

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/billing"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// Confidence says how sure a finding is.
type Confidence string

const (
	// High means history proves the finding.
	High Confidence = "high"
	// Medium means the finding is likely.
	Medium Confidence = "medium"
	// Advisory means fixing it is a tradeoff the user should weigh.
	Advisory Confidence = "advisory"
)

// Context is what a detector may read besides the execution itself.
type Context struct {
	Rules *billing.Rules
	// RetentionHours is the Namespace's retention, or 0 when unknown.
	RetentionHours float64
	// Now is when the analysis ran; open executions are measured to it.
	Now time.Time
}

// Match is one occurrence of a pattern in one execution.
type Match struct {
	EventIDs []int64
	Note     string
	// ActionsSaved is the billable Actions the fix would remove from this
	// execution.
	ActionsSaved float64
	// ActiveGBHoursSaved and RetainedGBHoursSaved are the storage the fix
	// would remove from this execution, for storage detectors.
	ActiveGBHoursSaved   float64
	RetainedGBHoursSaved float64
	// Keys are names from the evidence, such as Search Attribute keys or
	// Activity types, that the code locator ranks candidates with.
	Keys []string
}

// Detector finds one pattern in one execution.
type Detector interface {
	ID() string
	Confidence() Confidence
	Detect(ex *model.Execution, c *Context) []Match
}

// ChildReader is implemented by detectors that read Child Workflow
// histories. The analysis then fetches children into
// Execution.ChildExecutions before calling Detect.
type ChildReader interface{ NeedsChildren() bool }

// PrevalenceFloor is implemented by detectors whose pattern only matters
// when it shows up in most executions, such as a Timer that's canceled
// nearly every time. Findings below the floor (0 to 1) are dropped.
type PrevalenceFloor interface{ MinPrevalence() float64 }

// FeatureUser is implemented by detectors whose fix needs a newer Temporal
// feature. The IDs refer to kb/features.yaml.
type FeatureUser interface{ Features() []string }

var (
	mu       sync.RWMutex
	registry = map[string]Detector{}
)

// Register adds a detector. It panics on a duplicate ID, which is a
// programming error.
func Register(d Detector) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[d.ID()]; dup {
		panic(fmt.Sprintf("detector %s registered twice", d.ID()))
	}
	registry[d.ID()] = d
}

// All returns every registered detector, sorted by ID.
func All() []Detector {
	mu.RLock()
	defer mu.RUnlock()
	ds := make([]Detector, 0, len(registry))
	for _, d := range registry {
		ds = append(ds, d)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].ID() < ds[j].ID() })
	return ds
}

// ByID returns the detectors with the given IDs, or all of them when ids is
// empty.
func ByID(ids []string) []Detector {
	if len(ids) == 0 {
		return All()
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []Detector
	for _, d := range All() {
		if want[d.ID()] {
			out = append(out, d)
		}
	}
	return out
}

// NeedsChildren reports whether any of the detectors reads Child Workflow
// histories.
func NeedsChildren(ds []Detector) bool {
	for _, d := range ds {
		if c, ok := d.(ChildReader); ok && c.NeedsChildren() {
			return true
		}
	}
	return false
}
