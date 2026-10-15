package metrics

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/analyze"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/estimate"
)

// Thresholds. Each check needs enough data in the window to say anything,
// so each has a minimum count as well as a ratio.
const (
	// MET-001: more than one Heartbeat Action per Activity attempt.
	maxHeartbeatsPerAttempt = 1.0
	minActivityAttempts     = 20.0

	// MET-002: more than 4 Queries an hour per open Workflow, about one
	// every 15 minutes, for every open Workflow.
	maxQueriesPerOpenPerHour = 4.0
	minQueries               = 20.0
	// The saving keeps one Query an hour per open Workflow for the times
	// someone does need live state.
	keptQueriesPerOpenPerHour = 1.0

	// MET-003: at least 10% of Updates rejected.
	minRejectedShare = 0.10
	minRejected      = 10.0

	// MET-004: the metric shows at least 1.5 times the Actions the sampled
	// histories explain.
	minMixRatio   = 1.5
	minMixActions = 300.0
	// An Action type that makes up this much of the gap is named as the
	// likely cause.
	causeShare = 0.30

	// MET-005: Activities and Timers per start both move by 25% or more,
	// in the same direction, and by at least half an Action per start.
	minDesignChange      = 0.25
	minDesignChangeAbs   = 0.5
	minStartsPerWindow   = 100.0
	designRecentDuration = 24 * time.Hour

	// MET-006: the latest day is at least twice the average of the 7 days
	// before it.
	spikeFactor      = 2.0
	minDailyActions  = 10000.0
	minBaselineDays  = 4
	spikeTypesToList = 3
)

// Action types, from the Metric Name column in
// https://docs.temporal.io/evaluate/cloud/actions.
const (
	actScheduleActivity        = "schedule_activity"
	actRetryActivity           = "retry_activity"
	actHeartbeat               = "record_activity_heartbeat"
	actHeartbeatByID           = "record_activity_heartbeat_by_id"
	actStartStandalone         = "start_standalone_activity"
	actRetryStandalone         = "retry_standalone_activity"
	actStandaloneHeartbeat     = "record_standalone_activity_heartbeat"
	actStandaloneHeartbeatByID = "record_standalone_activity_heartbeat_by_id"
	actQuery                   = "query_workflow"
	actAcceptUpdate            = "accept_workflow_update"
	actRejectUpdate            = "reject_workflow_update"
	actStartWorkflow           = "start_workflow"
	actStartMultiOp            = "start_workflow_multi_operation"
	actContinueAsNew           = "continue_as_new_workflow"
	actStartTimer              = "start_timer"
)

// inHistory lists the Action types that Event History records, so the
// sampled histories can explain them. Queries, Heartbeats, rejected
// Updates, Schedules, and Standalone Activities never reach a Workflow's
// history.
var inHistory = map[string]bool{
	actStartWorkflow: true, "start_child_workflow": true, actContinueAsNew: true, actStartMultiOp: true,
	"update_workflow_options": true, "update_workflow_options_via_start": true,
	"upsert_workflow_search_attributes": true, "reset_workflow": true,
	actScheduleActivity: true, actRetryActivity: true, "retry_activity_local": true, "record_activity_markers": true,
	actStartTimer: true, "signal_workflow": true, "signal_external_workflow": true, "signal_workflow_with_start": true,
	actAcceptUpdate: true,
}

func visibleInHistory(action string) bool {
	return inHistory[action] || strings.Contains(action, "nexus")
}

const month = 30 * 24 * time.Hour

// input is the dataset summed by Workflow Type and Action type.
type input struct {
	ds         *Dataset
	env        *analyze.Env
	monthScale float64                       // multiplies dataset totals into a month
	byType     map[string]map[string]float64 // Workflow Type -> Action type -> Actions
	openAvg    float64                       // average open Workflows, or -1 when absent
}

func newInput(ds *Dataset, env *analyze.Env) *input {
	in := &input{ds: ds, env: env, byType: map[string]map[string]float64{}, openAvg: -1}
	if ds.Covered > 0 {
		in.monthScale = float64(month) / float64(ds.Covered)
	}
	var openSum, openN float64
	for _, s := range ds.Series {
		switch s.Metric {
		case metricBillable:
			wt, act := s.Labels[labelWorkflowType], s.Labels[labelActionType]
			m := in.byType[wt]
			if m == nil {
				m = map[string]float64{}
				in.byType[wt] = m
			}
			m[act] += total(s.Points)
		case metricOpenWorkflows:
			for _, p := range s.Points {
				openSum += p.Rate
				openN++
			}
		}
	}
	if openN > 0 {
		// Series are per region; sum across them, averaged over windows.
		windows := map[time.Time]bool{}
		for _, s := range ds.Series {
			if s.Metric == metricOpenWorkflows {
				for _, p := range s.Points {
					windows[p.Time] = true
				}
			}
		}
		in.openAvg = openSum / float64(len(windows))
	}
	return in
}

// total is the number of Actions in a series: each point's per-second rate
// times its window length.
func total(ps []Point) float64 {
	var t float64
	for _, p := range ps {
		t += p.Rate * p.Seconds
	}
	return t
}

func (in *input) types() []string {
	out := make([]string, 0, len(in.byType))
	for t := range in.byType {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func (in *input) sum(wt string, actions ...string) float64 {
	var t float64
	for _, a := range actions {
		t += in.byType[wt][a]
	}
	return t
}

func (in *input) window() string { return in.ds.Description }

func series(wt string, actions ...string) string {
	sel := fmt.Sprintf(`%s="%s"`, labelWorkflowType, wt)
	if len(actions) == 1 {
		sel += fmt.Sprintf(`,%s="%s"`, labelActionType, actions[0])
	} else if len(actions) > 1 {
		sel += fmt.Sprintf(`,%s=~"%s"`, labelActionType, strings.Join(actions, "|"))
	}
	return metricBillable + "{" + sel + "}"
}

func perMonth(x, scale float64) float64 { return math.Round(x * scale) }

// heartbeatHeavy is MET-001: Workflow Types whose Activities record more
// than one Heartbeat Action per attempt. Every SDK sends the first
// Heartbeat at once, so short Activities with a Heartbeat timeout pay one
// Action per run; later Heartbeats are throttled to 80% of the timeout.
func heartbeatHeavy(in *input) []estimate.Finding {
	var out []estimate.Finding
	for _, wt := range in.types() {
		hbActions := []string{actHeartbeat, actHeartbeatByID, actStandaloneHeartbeat, actStandaloneHeartbeatByID}
		hb := in.sum(wt, hbActions...)
		attempts := in.sum(wt, actScheduleActivity, actRetryActivity, actStartStandalone, actRetryStandalone)
		if attempts < minActivityAttempts || hb <= maxHeartbeatsPerAttempt*attempts {
			continue
		}
		ratio := hb / attempts
		saved := perMonth(hb-maxHeartbeatsPerAttempt*attempts, in.monthScale)
		out = append(out, estimate.Finding{
			ID: "MET-001", WorkflowType: wt, Confidence: detect.Medium,
			ProjectedMonthly: &estimate.Projection{Actions: saved},
			Evidence: []estimate.Evidence{{
				Metric: series(wt, hbActions[0], hbActions[1]), Window: in.window(),
				Note: fmt.Sprintf("%s Heartbeat Actions for %s Activity attempts: %.1f per attempt", fmtN(hb), fmtN(attempts), ratio),
			}},
		})
	}
	return out
}

// queryPolling is MET-002: Queries far more often than anyone could be
// reading the answers, which points at a dashboard or a client loop
// polling with Query. Each Query a Worker receives is one Action.
func queryPolling(in *input) []estimate.Finding {
	queries := 0.0
	type tq struct {
		wt string
		q  float64
	}
	var per []tq
	for _, wt := range in.types() {
		if q := in.sum(wt, actQuery); q > 0 {
			queries += q
			per = append(per, tq{wt, q})
		}
	}
	if queries < minQueries || in.openAvg <= 0 {
		return nil
	}
	hours := in.ds.Covered.Hours()
	rate := queries / in.openAvg / hours
	if rate <= maxQueriesPerOpenPerHour {
		return nil
	}
	kept := keptQueriesPerOpenPerHour * in.openAvg * hours
	sort.Slice(per, func(i, j int) bool { return per[i].q > per[j].q })
	ev := []estimate.Evidence{{
		Metric: metricBillable + `{action_type="query_workflow"}`, Window: in.window(),
		Note: fmt.Sprintf("%s Queries for about %s open Workflows: %.1f per open Workflow per hour", fmtN(queries), fmtN(in.openAvg), rate),
	}}
	for i, p := range per {
		if i == 3 {
			break
		}
		ev = append(ev, estimate.Evidence{
			Metric: series(p.wt, actQuery), Window: in.window(),
			Note: fmt.Sprintf("%s: %s Queries, %.0f%% of the total", p.wt, fmtN(p.q), 100*p.q/queries),
		})
	}
	wt := ""
	if len(per) == 1 {
		wt = per[0].wt
	}
	return []estimate.Finding{{
		ID: "MET-002", WorkflowType: wt, Confidence: detect.Medium,
		ProjectedMonthly: &estimate.Projection{Actions: perMonth(queries-kept, in.monthScale)},
		Evidence:         ev,
	}}
}

// rejectedUpdates is MET-003: Updates that Workers reject. A rejected
// Update is still one Action, and it never reaches history.
func rejectedUpdates(in *input) []estimate.Finding {
	var out []estimate.Finding
	for _, wt := range in.types() {
		rej, acc := in.sum(wt, actRejectUpdate), in.sum(wt, actAcceptUpdate)
		if rej < minRejected || rej/(rej+acc) < minRejectedShare {
			continue
		}
		out = append(out, estimate.Finding{
			ID: "MET-003", WorkflowType: wt, Confidence: detect.Medium,
			ProjectedMonthly: &estimate.Projection{Actions: perMonth(rej, in.monthScale)},
			Evidence: []estimate.Evidence{{
				Metric: series(wt, actRejectUpdate, actAcceptUpdate), Window: in.window(),
				Note: fmt.Sprintf("%s Updates rejected and %s accepted: %.0f%% rejected", fmtN(rej), fmtN(acc), 100*rej/(rej+acc)),
			}},
		})
	}
	return out
}

// unexplainedMix is MET-004: a Workflow Type whose metric shows far more
// history-visible Actions than its sampled histories explain. Retries
// that the sample missed, or tasks rejected by the wrong Worker, are the
// usual cause. It's a pointer to other findings, so it carries no saving
// of its own.
func unexplainedMix(in *input) []estimate.Finding {
	var out []estimate.Finding
	for _, t := range in.env.Types {
		if t.MonthlyActions <= 0 {
			continue
		}
		acts := in.byType[t.WorkflowType]
		var visible float64
		for a, n := range acts {
			if visibleInHistory(a) {
				visible += n
			}
		}
		if visible < minMixActions {
			continue
		}
		metricMonthly := visible * in.monthScale
		if metricMonthly < minMixRatio*t.MonthlyActions {
			continue
		}
		// Rank Action types by how much of the metric they make up.
		type share struct {
			action string
			n      float64
		}
		var shares []share
		for a, n := range acts {
			if visibleInHistory(a) && n > 0 {
				shares = append(shares, share{a, n})
			}
		}
		sort.Slice(shares, func(i, j int) bool { return shares[i].n > shares[j].n })
		var parts []string
		for i, s := range shares {
			if i == 3 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s %.0f%%", s.action, 100*s.n/visible))
		}
		note := fmt.Sprintf("The metric shows about %s history-visible Actions a month; the sampled histories explain %s (%.1f times). Largest Action types: %s.",
			estimate.FormatActions(metricMonthly), estimate.FormatActions(t.MonthlyActions), metricMonthly/t.MonthlyActions, strings.Join(parts, ", "))
		if acts[actRetryActivity]/visible >= causeShare {
			note += " Activity retries dominate, which points at a retry storm (ACT-001) or Activities rejected by the wrong Worker (ACT-006)."
		}
		out = append(out, estimate.Finding{
			ID: "MET-004", WorkflowType: t.WorkflowType, Confidence: detect.Medium,
			Evidence: []estimate.Evidence{{Metric: series(t.WorkflowType), Window: in.window(), Note: note}},
		})
	}
	return out
}

// designChange is MET-005: Activities and Timers per start change
// together between the week before and the latest day, which is how a
// polling loop being added or removed shows up after a deploy.
func designChange(in *input) []estimate.Finding {
	split := in.ds.End.Add(-designRecentDuration)
	type counts struct{ starts, acts, timers float64 }
	var out []estimate.Finding
	byType := map[string]*[2]counts{} // [0] before, [1] recent
	for _, s := range in.ds.Series {
		if s.Metric != metricBillable {
			continue
		}
		wt, act := s.Labels[labelWorkflowType], s.Labels[labelActionType]
		c := byType[wt]
		if c == nil {
			c = &[2]counts{}
			byType[wt] = c
		}
		for _, p := range s.Points {
			i := 0
			if !p.Time.Before(split) {
				i = 1
			}
			n := p.Rate * p.Seconds
			switch act {
			case actStartWorkflow, actStartMultiOp, actContinueAsNew:
				c[i].starts += n
			case actScheduleActivity:
				c[i].acts += n
			case actStartTimer:
				c[i].timers += n
			}
		}
	}
	types := make([]string, 0, len(byType))
	for wt := range byType {
		types = append(types, wt)
	}
	sort.Strings(types)
	for _, wt := range types {
		c := byType[wt]
		before, recent := c[0], c[1]
		if before.starts < minStartsPerWindow || recent.starts < minStartsPerWindow {
			continue
		}
		a0, a1 := before.acts/before.starts, recent.acts/recent.starts
		t0, t1 := before.timers/before.starts, recent.timers/recent.starts
		da, dt := a1-a0, t1-t0
		if da == 0 || dt == 0 || (da > 0) != (dt > 0) {
			continue
		}
		if !changed(a0, a1) || !changed(t0, t1) || math.Abs(da)+math.Abs(dt) < minDesignChangeAbs {
			continue
		}
		dir := "rose"
		if da < 0 {
			dir = "fell"
		}
		f := estimate.Finding{
			ID: "MET-005", WorkflowType: wt, Confidence: detect.Advisory,
			Evidence: []estimate.Evidence{{
				Metric: series(wt, actScheduleActivity, actStartTimer, actStartWorkflow), Window: in.ds.Description,
				Note: fmt.Sprintf("In the latest day, Activities per start %s from %.2f to %.2f and Timers per start from %.2f to %.2f, compared with the week before.",
					dir, a0, a1, t0, t1),
			}},
		}
		if da > 0 {
			// What the change adds each month at the latest day's volume.
			startsPerMonth := recent.starts * float64(month) / float64(designRecentDuration)
			f.ProjectedMonthly = &estimate.Projection{Actions: math.Round((da + dt) * startsPerMonth)}
		}
		out = append(out, f)
	}
	return out
}

func changed(before, after float64) bool {
	if before == 0 {
		return after > 0
	}
	return math.Abs(after-before)/before >= minDesignChange
}

// usageSpike is MET-006: the latest day's Actions are at least twice the
// average of the 7 days before it, for the Namespace or a Workflow Type.
// Temporal Cloud has no spend cap, so a spike runs until someone notices.
func usageSpike(in *input) []estimate.Finding {
	end := in.ds.End
	latestStart := end.Add(-24 * time.Hour)
	baseStart := latestStart.Add(-7 * 24 * time.Hour)
	type daily struct {
		latest float64
		days   map[int]float64 // day index before latestStart -> Actions
	}
	ns := &daily{days: map[int]float64{}}
	byType := map[string]*daily{}
	for _, s := range in.ds.Series {
		if s.Metric != metricBillable {
			continue
		}
		wt := s.Labels[labelWorkflowType]
		d := byType[wt]
		if d == nil {
			d = &daily{days: map[int]float64{}}
			byType[wt] = d
		}
		for _, p := range s.Points {
			n := p.Rate * p.Seconds
			switch {
			case !p.Time.Before(latestStart) && !p.Time.After(end):
				d.latest += n
				ns.latest += n
			case !p.Time.Before(baseStart) && p.Time.Before(latestStart):
				day := int(latestStart.Sub(p.Time) / (24 * time.Hour))
				d.days[day] += n
				ns.days[day] += n
			}
		}
	}
	baseline := func(d *daily) (float64, bool) {
		if len(d.days) < minBaselineDays {
			return 0, false
		}
		var t float64
		for _, n := range d.days {
			t += n
		}
		return t / float64(len(d.days)), true
	}
	spiking := func(d *daily) (float64, bool) {
		b, ok := baseline(d)
		return b, ok && d.latest >= minDailyActions && d.latest >= spikeFactor*b
	}
	window := "the latest day against the 7 days before it"

	type spike struct {
		wt       string
		latest   float64
		base     float64
		increase float64
	}
	var types []spike
	for wt, d := range byType {
		if b, ok := spiking(d); ok {
			types = append(types, spike{wt, d.latest, b, d.latest - b})
		}
	}
	sort.Slice(types, func(i, j int) bool { return types[i].increase > types[j].increase })

	if b, ok := spiking(ns); ok {
		f := estimate.Finding{
			ID: "MET-006", Confidence: detect.Medium,
			ProjectedMonthly: &estimate.Projection{Actions: math.Round((ns.latest - b) * 30)},
			Evidence: []estimate.Evidence{{
				Metric: metricBillable, Window: window,
				Note: fmt.Sprintf("%s Actions in the latest day against a daily average of %s: %.1f times", fmtN(ns.latest), fmtN(b), ns.latest/b),
			}},
		}
		for i, t := range types {
			if i == spikeTypesToList {
				break
			}
			f.Evidence = append(f.Evidence, estimate.Evidence{
				Metric: series(t.wt), Window: window,
				Note: fmt.Sprintf("%s: %s Actions against a daily average of %s", t.wt, fmtN(t.latest), fmtN(t.base)),
			})
		}
		return []estimate.Finding{f}
	}
	var out []estimate.Finding
	for _, t := range types {
		out = append(out, estimate.Finding{
			ID: "MET-006", WorkflowType: t.wt, Confidence: detect.Medium,
			ProjectedMonthly: &estimate.Projection{Actions: math.Round(t.increase * 30)},
			Evidence: []estimate.Evidence{{
				Metric: series(t.wt), Window: window,
				Note: fmt.Sprintf("%s Actions in the latest day against a daily average of %s: %.1f times", fmtN(t.latest), fmtN(t.base), t.latest/t.base),
			}},
		})
	}
	return out
}

func fmtN(x float64) string { return estimate.FormatActions(x) }
