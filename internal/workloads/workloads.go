// Package workloads keeps a registry of small Workflows that produce known
// cost patterns. Each detector group registers its own Group in a
// subpackage; internal/workloads/all imports them. tools/genhistories runs
// the groups against a dev server to record golden histories, and its
// -load mode fills a Namespace for end-to-end runs.
package workloads

import (
	"context"
	"sort"
	"sync"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// Group is one detector group's workloads.
type Group struct {
	// Name is short and unique, such as "searchattr". Its Task Queue is
	// "tcopt-workloads-<Name>".
	Name string
	// SearchAttributes lists custom Search Attributes the dev server must
	// register, as "Name=Type".
	SearchAttributes []string
	// Register adds the group's Workflows and Activities to a Worker. Leave
	// it nil when Workers is set.
	Register func(w worker.Worker)
	// Workers starts the group's Workers itself, for patterns that need
	// more than one, such as a Worker missing an Activity registration. It
	// returns a function that stops them.
	Workers func(c client.Client, taskQueue string) (stop func(), err error)
	Runs    []Run
	Load    []LoadItem
}

// TaskQueue returns the group's Task Queue.
func (g Group) TaskQueue() string { return "tcopt-workloads-" + g.Name }

// Run records one golden history.
type Run struct {
	// Golden is the file name under testdata/histories, without .json.
	Golden   string
	Workflow any
	Args     []any
	Options  client.StartWorkflowOptions
	// Drive runs while the Workflow does, to send Signals or Updates. It
	// may be nil.
	Drive func(ctx context.Context, c client.Client, run client.WorkflowRun) error
	// ExpectFailure accepts a failed, timed-out, or terminated Workflow.
	ExpectFailure bool
	// LeaveOpen records the history without waiting for the Workflow to
	// close, after Settle has passed. The Workflow is then terminated.
	LeaveOpen bool
	Settle    time.Duration
	// Children also records each Child Workflow's history as
	// <Golden>.child-<initiated event ID>.json.
	Children bool
	// Timeout bounds the run; the default is one minute.
	Timeout time.Duration
}

// LoadItem is one kind of execution for -load mode, picked with
// probability Weight over the sum of all weights.
type LoadItem struct {
	Weight   int
	Workflow any
	Args     []any
	Options  client.StartWorkflowOptions
}

var (
	mu     sync.Mutex
	groups = map[string]Group{}
)

// Add registers a group. It panics on a duplicate name.
func Add(g Group) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := groups[g.Name]; dup {
		panic("workload group " + g.Name + " registered twice")
	}
	groups[g.Name] = g
}

// Groups returns every registered group, sorted by name.
func Groups() []Group {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Group, 0, len(groups))
	for _, g := range groups {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
