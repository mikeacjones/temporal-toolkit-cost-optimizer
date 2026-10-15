// Package analyze runs one analysis end to end: rank Workflow Types, sample
// and fetch histories, run the detectors, and estimate savings. The headless
// command calls it directly; the Analysis Workflow will call the same steps
// from Activities.
package analyze

import (
	"context"
	"fmt"
	"math"
	"time"

	enumspb "go.temporal.io/api/enums/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/billing"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	_ "github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect/all" // registers every detector
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/estimate"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/features"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/history"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/kb"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// Options control one analysis.
type Options struct {
	Profile string
	// Types to analyze. Empty means the top TopTypes from the ranking.
	Types    []string
	TopTypes int
	// Window is how far back to look for executions.
	Window time.Duration
	// SampleSize is the number of executions per type.
	SampleSize int
	Detectors  []string
	Parallel   int
	// Collectors run the checks that read something other than history:
	// Namespace config, Cloud metrics, and code. Each one that fails adds a
	// caveat instead of stopping the analysis.
	Collectors []Collector
	// MaxChildren caps the Child Workflow histories fetched per Workflow
	// Type for detectors that read them.
	MaxChildren int
	// RepoPaths are the user's repos. tcopt reads their lockfiles to say
	// whether a fix's newer feature is available in the SDK they use.
	RepoPaths []string
	// IncludePreview keeps findings whose only fix is a pre-release
	// feature. They're hidden by default.
	IncludePreview bool
	// Caveats are notes for the report from whoever set up the analysis,
	// such as checks that were left out.
	Caveats []string
}

// Caveater is implemented by collectors that have notes for the report
// even when they succeed, such as how little metric data they saw.
type Caveater interface{ Caveats() []string }

// Collector produces findings from a source other than sampled histories.
type Collector interface {
	// Name is shown in progress and caveats, such as "Namespace config".
	Name() string
	Collect(ctx context.Context, env *Env) ([]estimate.Finding, error)
}

// Env is what a collector can read: the Namespace, the history analysis so
// far, and the billing rules.
type Env struct {
	Reader   *history.Reader
	Rules    *billing.Rules
	Window   time.Duration
	Types    []TypeSummary
	Findings []estimate.Finding
	// Titles maps check IDs to titles from the guidance.
	Titles map[string]string
}

// Progress reports what the analysis is doing.
type Progress struct {
	Stage        string // "rank", "sample", "fetch", "children", "detect", "collect"
	WorkflowType string
	Done, Total  int
}

// TypeSummary describes one analyzed Workflow Type.
type TypeSummary struct {
	WorkflowType string `json:"workflowType"`
	// ExecutionsInWindow is the exact count, or -1 when counting failed.
	ExecutionsInWindow int64 `json:"executionsInWindow"`
	// MonthlyExecutions scales the window's count to 30 days.
	MonthlyExecutions int64 `json:"monthlyExecutions"`
	Sampled           int   `json:"sampled"`
	FetchErrors       int   `json:"fetchErrors"`
	// ActionsPerExecution is what the sampled histories show each run costs
	// today, from the billing rules.
	ActionsPerExecution estimate.Percentiles `json:"actionsPerExecution"`
	// MonthlyActions scales the sample's mean to MonthlyExecutions, or is -1
	// when the volume is unknown.
	MonthlyActions float64 `json:"monthlyActions"`
	// ActionsSavedPerMonth sums the findings' projections.
	ActionsSavedPerMonth float64 `json:"actionsSavedPerMonth"`
}

// Report is the result of one analysis.
type Report struct {
	Tool            string             `json:"tool"`
	GeneratedAt     time.Time          `json:"generatedAt"`
	Profile         string             `json:"profile"`
	Namespace       string             `json:"namespace"`
	WindowDays      float64            `json:"windowDays"`
	BillingSnapshot string             `json:"billingSnapshot"`
	Types           []TypeSummary      `json:"types"`
	Findings        []estimate.Finding `json:"findings"`
	// MonthlyActions and ActionsSavedPerMonth sum the analyzed types.
	MonthlyActions       float64  `json:"monthlyActions"`
	ActionsSavedPerMonth float64  `json:"actionsSavedPerMonth"`
	Caveats              []string `json:"caveats"`
}

// Run performs the analysis.
func Run(ctx context.Context, r *history.Reader, opts Options, progress func(Progress)) (*Report, error) {
	if progress == nil {
		progress = func(Progress) {}
	}
	if opts.Window <= 0 {
		opts.Window = 7 * 24 * time.Hour
	}
	if opts.SampleSize <= 0 {
		opts.SampleSize = 200
	}
	if opts.TopTypes <= 0 {
		opts.TopTypes = 5
	}
	rules, err := billing.LoadRules()
	if err != nil {
		return nil, fmt.Errorf("billing rules: %w", err)
	}
	titles := map[string]string{}
	if all, err := kb.AllGuidance(); err == nil {
		for id, g := range all {
			titles[id] = g.Title
		}
	}
	detectors := detect.ByID(opts.Detectors)
	if len(detectors) == 0 {
		return nil, fmt.Errorf("no detectors match %v", opts.Detectors)
	}
	if opts.MaxChildren <= 0 {
		opts.MaxChildren = 200
	}
	dctx := &detect.Context{Rules: rules, Now: time.Now()}
	if h, err := r.RetentionHours(ctx); err == nil {
		dctx.RetentionHours = h
	}

	rep := &Report{
		Tool:            "tcopt",
		GeneratedAt:     time.Now().UTC(),
		Profile:         opts.Profile,
		Namespace:       r.Namespace(),
		WindowDays:      opts.Window.Hours() / 24,
		BillingSnapshot: rules.Snapshot,
		Types:           []TypeSummary{},
		Findings:        []estimate.Finding{},
		Caveats: []string{
			"Estimates come from Event History. Queries, Activity Heartbeats, rejected Updates, Schedules, Export, and replication aren't in history and aren't counted.",
			"Version and Side Effect markers aren't counted as Actions; Local Activity markers count once per Workflow Task.",
			"Monthly figures scale the sample to the window's execution count, then to 30 days.",
		},
	}

	types := opts.Types
	counts := map[string]int64{}
	if len(types) == 0 {
		progress(Progress{Stage: "rank"})
		ranked, err := r.RankTypes(ctx, opts.Window, 5000)
		if err != nil {
			return nil, err
		}
		for i, t := range ranked {
			if i >= opts.TopTypes {
				break
			}
			types = append(types, t.WorkflowType)
			counts[t.WorkflowType] = t.Executions
		}
	}

	for _, wt := range types {
		sum := TypeSummary{WorkflowType: wt, ExecutionsInWindow: -1, MonthlyExecutions: -1}
		n, ok := counts[wt]
		if !ok || n < 0 {
			if c, err := r.Count(ctx, wt, opts.Window); err == nil {
				n = c
			} else {
				n = -1
			}
		}
		sum.ExecutionsInWindow = n
		if n >= 0 {
			sum.MonthlyExecutions = int64(float64(n) * (30 * 24 * time.Hour).Hours() / opts.Window.Hours())
		}

		progress(Progress{Stage: "sample", WorkflowType: wt})
		refs, err := r.Sample(ctx, wt, opts.Window, opts.SampleSize, max(opts.SampleSize*5, 1000))
		if err != nil {
			return nil, err
		}
		// A failed fetch drops that execution from the sample; it's counted,
		// not fatal.
		hs, _ := r.FetchAll(ctx, refs, opts.Parallel, func(done, total int) {
			progress(Progress{Stage: "fetch", WorkflowType: wt, Done: done, Total: total})
		})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		sum.FetchErrors = len(refs) - len(hs)

		progress(Progress{Stage: "detect", WorkflowType: wt})
		exs := make([]*model.Execution, 0, len(hs))
		for _, ref := range refs {
			if h, ok := hs[ref]; ok {
				exs = append(exs, model.Normalize(ref.WorkflowID, ref.RunID, h))
			}
		}
		sum.Sampled = len(exs)
		if detect.NeedsChildren(detectors) {
			attachChildren(ctx, r, exs, opts, func() { progress(Progress{Stage: "children", WorkflowType: wt}) })
		}
		sum.MonthlyActions = -1
		if len(exs) > 0 {
			per := make([]float64, len(exs))
			var total float64
			for i, ex := range exs {
				per[i] = rules.ExecutionActions(ex)
				total += per[i]
			}
			sum.ActionsPerExecution = estimate.Summarize(per)
			if sum.MonthlyExecutions >= 0 {
				sum.MonthlyActions = math.Round(total / float64(len(exs)) * float64(sum.MonthlyExecutions))
			}
		}

		findings := estimate.Run(estimate.Input{
			WorkflowType:      wt,
			Executions:        exs,
			MonthlyExecutions: sum.MonthlyExecutions,
			Titles:            titles,
		}, detectors, dctx)
		for _, f := range findings {
			if f.ProjectedMonthly != nil {
				sum.ActionsSavedPerMonth += f.ProjectedMonthly.Actions
			}
		}
		rep.ActionsSavedPerMonth += sum.ActionsSavedPerMonth
		if sum.MonthlyActions > 0 {
			rep.MonthlyActions += sum.MonthlyActions
		}
		rep.Findings = append(rep.Findings, findings...)
		rep.Types = append(rep.Types, sum)
	}
	rep.Caveats = append(rep.Caveats, opts.Caveats...)
	if dctx.RetentionHours == 0 {
		rep.Caveats = append(rep.Caveats, "Couldn't read the Namespace's retention, so storage estimates for closed Workflows are missing.")
	}

	env := &Env{Reader: r, Rules: rules, Window: opts.Window, Types: rep.Types, Findings: rep.Findings, Titles: titles}
	for _, c := range opts.Collectors {
		progress(Progress{Stage: "collect", WorkflowType: c.Name()})
		fs, err := c.Collect(ctx, env)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// A collector can return findings and an error together, when some
		// of its checks ran and others couldn't.
		if err != nil {
			rep.Caveats = append(rep.Caveats, fmt.Sprintf("Some %s checks didn't run: %v", c.Name(), err))
		}
		if cv, ok := c.(Caveater); ok {
			rep.Caveats = append(rep.Caveats, cv.Caveats()...)
		}
		for _, f := range fs {
			if f.Title == "" {
				f.Title = titles[f.ID]
			}
			if f.Guidance == "" {
				f.Guidance = f.ID
			}
			if p := f.ProjectedMonthly; p != nil {
				rep.ActionsSavedPerMonth += p.Actions
			}
			rep.Findings = append(rep.Findings, f)
		}
	}
	rep.Findings, rep.Caveats = applyFeatures(rep.Findings, rep.Caveats, opts)
	estimate.SortFindings(rep.Findings)
	return rep, nil
}

// applyFeatures says, for each finding whose fix needs a newer feature,
// whether the user's SDKs have it, and hides findings whose only fix is
// pre-release unless the user asked for them.
func applyFeatures(fs []estimate.Finding, caveats []string, opts Options) ([]estimate.Finding, []string) {
	cat, err := features.Load()
	if err != nil {
		return fs, append(caveats, "Couldn't load the feature catalog: "+err.Error())
	}
	var sdks []features.SDK
	for _, root := range opts.RepoPaths {
		found, err := features.Detect(root)
		if err != nil {
			caveats = append(caveats, fmt.Sprintf("Couldn't read SDK versions in %s: %v", root, err))
		}
		sdks = append(sdks, found...)
	}
	out := fs[:0]
	hidden := 0
	for _, f := range fs {
		preOnly := len(f.Features) > 0
		for _, id := range f.Features {
			if ft, ok := cat.Get(id); !ok || !ft.PreRelease() {
				preOnly = false
			}
			lines, err := cat.Status(id, sdks)
			if err != nil {
				continue
			}
			for _, l := range lines {
				f.FeatureStatus = append(f.FeatureStatus, l.Message)
			}
		}
		if preOnly && !opts.IncludePreview {
			hidden++
			continue
		}
		out = append(out, f)
	}
	if hidden > 0 {
		caveats = append(caveats, fmt.Sprintf("%d findings whose only fix is a pre-release feature are hidden; show them with --include-preview.", hidden))
	}
	return out, caveats
}

// attachChildren fetches Child Workflow histories for executions that
// started children, up to opts.MaxChildren per Workflow Type.
func attachChildren(ctx context.Context, r *history.Reader, exs []*model.Execution, opts Options, started func()) {
	type want struct {
		ex  *model.Execution
		id  int64
		ref history.Ref
	}
	var wants []want
	for _, ex := range exs {
		for _, c := range ex.Children {
			if c.RunID == "" || len(wants) >= opts.MaxChildren {
				continue
			}
			status := enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
			if c.ClosedEventID != 0 {
				status = enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED // closed, so safe to cache
			}
			wants = append(wants, want{ex, c.InitiatedEventID, history.Ref{WorkflowID: c.WorkflowID, RunID: c.RunID, Status: status}})
		}
	}
	if len(wants) == 0 {
		return
	}
	started()
	refs := make([]history.Ref, len(wants))
	for i, w := range wants {
		refs[i] = w.ref
	}
	hs, _ := r.FetchAll(ctx, refs, opts.Parallel, nil)
	for _, w := range wants {
		h, ok := hs[w.ref]
		if !ok {
			continue
		}
		if w.ex.ChildExecutions == nil {
			w.ex.ChildExecutions = map[int64]*model.Execution{}
		}
		w.ex.ChildExecutions[w.id] = model.Normalize(w.ref.WorkflowID, w.ref.RunID, h)
	}
}
