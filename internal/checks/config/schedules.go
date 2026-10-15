package config

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	schedulepb "go.temporal.io/api/schedule/v1"
	"go.temporal.io/api/workflowservice/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/estimate"
)

// schedulerWorkflowPrefix starts the ID of the internal Workflow that runs
// each Schedule.
const schedulerWorkflowPrefix = "temporal-sys-scheduler:"

// schedule is what the checks read about one Schedule.
type schedule struct {
	ID           string
	WorkflowType string
	Spec         *schedulepb.ScheduleSpec
	Paused       bool
	Limited      bool
	Remaining    int64
	Created      time.Time
	Future       []time.Time
	LastAction   time.Time
	StateBytes   int64
	// HistoryBytes is the size of the Schedule's internal Workflow
	// history, or -1 when Visibility didn't say.
	HistoryBytes int64
}

// specEntries counts the calendar, cron, and interval entries in a spec.
func specEntries(s *schedulepb.ScheduleSpec) int {
	return len(s.GetCalendar()) + len(s.GetStructuredCalendar()) + len(s.GetCronString()) + len(s.GetInterval())
}

// period returns how often a Schedule runs: its shortest interval when the
// spec only has intervals, or else the median gap between upcoming runs.
// It returns 0 when that can't be told.
func (s schedule) period() time.Duration {
	sp := s.Spec
	if len(sp.GetInterval()) > 0 && len(sp.GetCalendar())+len(sp.GetStructuredCalendar())+len(sp.GetCronString()) == 0 {
		var shortest time.Duration
		for _, iv := range sp.GetInterval() {
			if d := iv.GetInterval().AsDuration(); d > 0 && (shortest == 0 || d < shortest) {
				shortest = d
			}
		}
		return shortest
	}
	if len(s.Future) < 2 {
		return 0
	}
	gaps := make([]time.Duration, 0, len(s.Future)-1)
	for i := 1; i < len(s.Future); i++ {
		gaps = append(gaps, s.Future[i].Sub(s.Future[i-1]))
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
	return gaps[len(gaps)/2]
}

// runsPerMonth estimates a Schedule's runs in a 30-day month, or 0 when it
// can't be told or the Schedule won't run.
func (s schedule) runsPerMonth() float64 {
	if s.Paused || (s.Limited && s.Remaining <= 0) {
		return 0
	}
	p := s.period()
	if p <= 0 {
		return 0
	}
	runs := float64(30*24*time.Hour) / float64(p)
	if s.Limited {
		runs = min(runs, float64(s.Remaining))
	}
	return runs
}

// oneShot reports whether a Schedule runs once, as a delayed start.
func (s schedule) oneShot() bool { return s.Limited && s.Remaining == 1 }

// loadSchedules lists and describes the Namespace's Schedules, up to
// MaxSchedules, and reads their internal Workflows' history sizes.
func (r *run) loadSchedules(ctx context.Context) ([]schedule, error) {
	svc, ns := r.env.Reader.Service(), r.env.Reader.Namespace()
	var out []schedule
	var token []byte
	for len(out) < r.opts.MaxSchedules {
		if err := r.env.Reader.Wait(ctx); err != nil {
			return nil, err
		}
		resp, err := svc.ListSchedules(ctx, &workflowservice.ListSchedulesRequest{
			Namespace: ns, MaximumPageSize: 100, NextPageToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list Schedules: %w", err)
		}
		for _, e := range resp.GetSchedules() {
			if len(out) >= r.opts.MaxSchedules {
				break
			}
			s := schedule{
				ID: e.GetScheduleId(), WorkflowType: e.GetInfo().GetWorkflowType().GetName(),
				Spec: e.GetInfo().GetSpec(), Paused: e.GetInfo().GetPaused(),
				StateBytes: e.GetInfo().GetStateSizeBytes(), HistoryBytes: -1,
			}
			for _, t := range e.GetInfo().GetFutureActionTimes() {
				s.Future = append(s.Future, t.AsTime())
			}
			for _, a := range e.GetInfo().GetRecentActions() {
				if t := a.GetActualTime().AsTime(); t.After(s.LastAction) {
					s.LastAction = t
				}
			}
			if err := r.describe(ctx, &s); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		if token = resp.GetNextPageToken(); len(token) == 0 {
			break
		}
	}
	if len(out) > 0 {
		r.schedulerHistorySizes(ctx, out)
	}
	return out, nil
}

// describe fills the settings ListSchedules leaves out.
func (r *run) describe(ctx context.Context, s *schedule) error {
	if err := r.env.Reader.Wait(ctx); err != nil {
		return err
	}
	resp, err := r.env.Reader.Service().DescribeSchedule(ctx, &workflowservice.DescribeScheduleRequest{
		Namespace: r.env.Reader.Namespace(), ScheduleId: s.ID,
	})
	if err != nil {
		return fmt.Errorf("describe Schedule %s: %w", s.ID, err)
	}
	sc := resp.GetSchedule()
	if sc.GetSpec() != nil {
		s.Spec = sc.GetSpec()
	}
	if wt := sc.GetAction().GetStartWorkflow().GetWorkflowType().GetName(); wt != "" {
		s.WorkflowType = wt
	}
	s.Paused = sc.GetState().GetPaused()
	s.Limited = sc.GetState().GetLimitedActions()
	s.Remaining = sc.GetState().GetRemainingActions()
	s.Created = resp.GetInfo().GetCreateTime().AsTime()
	if b := resp.GetInfo().GetStateSizeBytes(); b > 0 {
		s.StateBytes = b
	}
	return nil
}

// schedulerHistorySizes reads each Schedule's internal Workflow with
// DescribeWorkflowExecution, which reports the live history size (not a
// billable Action). Visibility leaves the size empty while the Workflow is
// open. Schedules whose internal Workflow can't be described keep
// HistoryBytes at -1.
func (r *run) schedulerHistorySizes(ctx context.Context, ss []schedule) {
	for i := range ss {
		if r.env.Reader.Wait(ctx) != nil {
			return
		}
		resp, err := r.env.Reader.Service().DescribeWorkflowExecution(ctx, &workflowservice.DescribeWorkflowExecutionRequest{
			Namespace: r.env.Reader.Namespace(),
			Execution: &commonpb.WorkflowExecution{WorkflowId: schedulerWorkflowPrefix + ss[i].ID},
		})
		if err != nil {
			continue
		}
		ss[i].HistoryBytes = resp.GetWorkflowExecutionInfo().GetHistorySizeBytes()
	}
}

// checkSchedules runs CFG-007, CFG-008, and CFG-010.
func (r *run) checkSchedules(ctx context.Context) error {
	ss, err := r.loadSchedules(ctx)
	if err != nil {
		return err
	}
	scheduleRun := r.notInHistory("schedule_run")
	start := r.eventActions(enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED)
	timer := r.eventActions(enumspb.EVENT_TYPE_TIMER_STARTED)

	wf001 := map[string]estimate.Finding{}
	for _, f := range r.env.Findings {
		if f.ID == "WF-001" {
			wf001[f.WorkflowType] = f
		}
	}

	type group struct {
		evidence []estimate.Evidence
		actions  float64
		active   float64
	}
	frequent, oneShot, storage, standalone := map[string]*group{}, map[string]*group{}, map[string]*group{}, map[string]*group{}
	addTo := func(m map[string]*group, wt string, e estimate.Evidence, actions, active float64) {
		g := m[wt]
		if g == nil {
			g = &group{}
			m[wt] = g
		}
		if len(g.evidence) < estimate.MaxEvidence {
			g.evidence = append(g.evidence, e)
		}
		g.actions += actions
		g.active += active
	}
	windowStart := r.now.Add(-r.env.Window)
	for _, s := range ss {
		setting := "schedule/" + s.ID
		runs := s.runsPerMonth()

		// CFG-007: a Workflow that sleeps between runs pays one Timer per
		// run instead of a Schedule run.
		if p := s.period(); runs > 0 && !s.Limited && p > 0 && p <= r.opts.FrequentInterval {
			addTo(frequent, s.WorkflowType, estimate.Evidence{
				Setting: setting,
				Note: fmt.Sprintf("Runs every %s, about %s times a month at %s Actions each.",
					p, estimate.FormatActions(runs), estimate.FormatActions(scheduleRun)),
			}, runs*(scheduleRun-timer), 0)
		}
		// CFG-007: a Schedule used once as a delayed start. Count the ones
		// created in the window, scaled to a month.
		if s.oneShot() {
			var perMonth float64
			if !s.Created.IsZero() && s.Created.After(windowStart) {
				perMonth = float64(30*24*time.Hour) / float64(r.env.Window)
			}
			addTo(oneShot, s.WorkflowType, estimate.Evidence{
				Setting: setting,
				Note:    fmt.Sprintf("Runs once (1 remaining action), as a delayed start. A Schedule run costs %s Actions; a start with a delay costs %s.", estimate.FormatActions(scheduleRun), estimate.FormatActions(start)),
			}, perMonth*(scheduleRun-start), 0)
		}
		// CFG-008: Schedules that hold storage: unused ones, large internal
		// histories, and specs that keep growing.
		unused := s.Paused || (len(s.Future) == 0 && !s.oneShot())
		var reasons []string
		switch {
		case s.Paused:
			reasons = append(reasons, "it's paused")
		case unused:
			reasons = append(reasons, "it has no upcoming runs")
		}
		if s.HistoryBytes >= r.opts.LargeSchedulerHistory {
			reasons = append(reasons, fmt.Sprintf("its internal Workflow history is %s", bytesText(s.HistoryBytes)))
		}
		if n := specEntries(s.Spec); n >= r.opts.LargeSpecEntries {
			reasons = append(reasons, fmt.Sprintf("its spec has %d entries, which suggests updates add to it instead of replacing it", n))
		}
		if len(reasons) > 0 {
			var active float64
			if unused && s.HistoryBytes > 0 {
				// Deleting it frees the open internal Workflow's history.
				active = float64(s.HistoryBytes) / 1e9 * hoursPerMonth
			}
			addTo(storage, s.WorkflowType, estimate.Evidence{Setting: setting, Note: "Flagged because " + strings.Join(reasons, ", and ") + "."}, 0, active)
		}
		// CFG-010: the target Workflow only wraps one Activity.
		if f, ok := wf001[s.WorkflowType]; ok && runs > 0 {
			per := f.ActionsSavedPerExecution.P50
			addTo(standalone, s.WorkflowType, estimate.Evidence{
				Setting: setting,
				Note:    fmt.Sprintf("Starts %s, which WF-001 found only runs one Activity, about %s times a month.", s.WorkflowType, estimate.FormatActions(runs)),
			}, runs*per, 0)
		}
	}

	emit := func(id string, conf detect.Confidence, m map[string]*group, features []string) {
		types := make([]string, 0, len(m))
		for wt := range m {
			types = append(types, wt)
		}
		sort.Strings(types)
		for _, wt := range types {
			g := m[wt]
			r.add(estimate.Finding{
				ID: id, Confidence: conf, WorkflowType: wt, Evidence: g.evidence, Features: features,
				ProjectedMonthly: &estimate.Projection{Actions: g.actions, ActiveGBHours: g.active},
			})
		}
	}
	emit("CFG-007", detect.Medium, frequent, []string{"workflow-start-delay"})
	emit("CFG-007", detect.Medium, oneShot, []string{"workflow-start-delay"})
	emit("CFG-008", detect.Medium, storage, nil)
	emit("CFG-010", detect.Advisory, standalone, []string{"standalone-activity"})
	return nil
}

func bytesText(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", b)
}
