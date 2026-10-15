package config

import (
	"context"
	"fmt"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/estimate"
)

// checkReplication is CFG-001: a replica on a Namespace whose name or tags
// mark it as non-production. A replica doubles Actions and storage, so
// removing it saves the extra copy. When usage isn't readable, the analyzed
// Namespace falls back to its sampled histories, a lower bound.
func (r *run) checkReplication(ns *NamespaceInfo, days []DayUsage, analyzed bool) {
	if ns.Replicas == 0 {
		return
	}
	why, ok := nonProduction(ns)
	if !ok {
		return
	}
	// Usage reports billed amounts, which include the replica's copy.
	extra := 1 - 1/r.replication
	proj := &estimate.Projection{}
	basis := "usage"
	if m := r.monthlyUsage(days); m.known {
		proj.Actions = m.Actions * extra
		proj.ActiveGBHours = m.ActiveGBHours * extra
		proj.RetainedGBHours = m.RetainedGBHours * extra
	} else if analyzed {
		// Histories show each Action once; the replica bills it again.
		proj.Actions = r.historyMonthlyActions() * (r.replication - 1)
		basis = "the analyzed Workflow Types' histories (a lower bound)"
	}
	r.add(estimate.Finding{
		ID:         "CFG-001",
		Confidence: detect.Medium,
		Evidence: []estimate.Evidence{{
			Setting: fmt.Sprintf("namespace/%s/replicas", ns.Name),
			Note: fmt.Sprintf("%d replica(s) in %s on a Namespace that looks non-production (%s). Estimate from %s.",
				ns.Replicas, strings.Join(ns.ReplicaRegions, ", "), why, basis),
		}},
		ProjectedMonthly: proj,
		Keys:             []string{ns.Name},
	})
}

// checkFairness is CFG-003: Fairness is on, but none of the sampled
// histories sets a fairness key, so every Action carries the surcharge
// without anything using it.
func (r *run) checkFairness(ctx context.Context) error {
	if !r.ns.FairnessEnabled {
		return nil
	}
	checked, types := 0, 0
	for _, t := range r.env.Types {
		refs, err := r.env.Reader.Sample(ctx, t.WorkflowType, r.env.Window, r.opts.FairnessSamples, r.opts.FairnessSamples*5)
		if err != nil {
			return err
		}
		hs, _ := r.env.Reader.FetchAll(ctx, refs, 4, nil)
		if len(hs) > 0 {
			types++
		}
		for _, h := range hs {
			checked++
			if fairnessKey(h) != "" {
				return nil // Fairness is in use
			}
		}
	}
	if checked == 0 {
		return nil // nothing to judge by
	}
	surcharge := r.fairness - 1
	proj := &estimate.Projection{}
	basis := "usage"
	if m := r.monthlyUsage(r.usage[r.ns.Name]); m.known {
		// Billed Actions include the surcharge.
		proj.Actions = m.Actions * surcharge / r.fairness
	} else {
		proj.Actions = r.historyMonthlyActions() * surcharge
		basis = "the analyzed Workflow Types' histories (a lower bound)"
	}
	r.add(estimate.Finding{
		ID:         "CFG-003",
		Confidence: detect.Medium,
		Evidence: []estimate.Evidence{{
			Setting: fmt.Sprintf("namespace/%s/fairness", r.ns.Name),
			Note: fmt.Sprintf("Fairness is on, and none of %d histories across %d Workflow Types sets a fairness key. Estimate from %s.",
				checked, types, basis),
		}},
		ProjectedMonthly: proj,
	})
	return nil
}

// fairnessKey returns the first fairness key a history sets, or "".
func fairnessKey(h *historypb.History) string {
	for _, ev := range h.GetEvents() {
		var key string
		switch ev.GetEventType() {
		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED:
			key = ev.GetWorkflowExecutionStartedEventAttributes().GetPriority().GetFairnessKey()
		case enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED:
			key = ev.GetActivityTaskScheduledEventAttributes().GetPriority().GetFairnessKey()
		case enumspb.EVENT_TYPE_START_CHILD_WORKFLOW_EXECUTION_INITIATED:
			key = ev.GetStartChildWorkflowExecutionInitiatedEventAttributes().GetPriority().GetFairnessKey()
		case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_OPTIONS_UPDATED:
			key = ev.GetWorkflowExecutionOptionsUpdatedEventAttributes().GetPriority().GetFairnessKey()
		}
		if key != "" {
			return key
		}
	}
	return ""
}

// checkCapacity is CFG-004: Provisioned Capacity whose hourly floor is
// above what the Namespace uses. Each TRU after the first commits a floor
// of Actions per hour; when usage stays at or near that floor most days,
// the TRUs, not the Workflows, set the bill. Usage includes the floor's own
// Actions, so the saving can't be separated and isn't projected.
func (r *run) checkCapacity() {
	// The floor each TRU after the first adds, from the billing rules
	// (cost/cloud-pricing, "How does pricing for Capacity Modes work?").
	floorPerHour := r.env.Rules.TRUFloorPerHour()
	if r.ns.TRUs <= 1 || floorPerHour == 0 {
		return
	}
	floorDay := (r.ns.TRUs - 1) * floorPerHour * 24
	days := r.usage[r.ns.Name]
	note := fmt.Sprintf("%g TRUs commit at least %s Actions a day (%s a month).",
		r.ns.TRUs, estimate.FormatActions(floorDay), estimate.FormatActions(floorDay*30))
	if r.usage == nil {
		note += " Usage isn't readable, so tcopt can't compare it with the floor."
	} else {
		atFloor := 0
		var total float64
		for _, d := range days {
			total += d.Actions
			if d.Actions <= floorDay*1.02 {
				atFloor++
			}
		}
		missing := r.usageDays - len(days) // days with no record are below the floor
		atFloor += max(missing, 0)
		if r.usageDays == 0 || atFloor*2 < r.usageDays {
			return
		}
		note += fmt.Sprintf(" On %d of %d days, usage was at or below that floor (%s Actions a day on average).",
			atFloor, r.usageDays, estimate.FormatActions(total/float64(r.usageDays)))
	}
	r.add(estimate.Finding{
		ID:         "CFG-004",
		Confidence: detect.Advisory,
		Evidence:   []estimate.Evidence{{Setting: fmt.Sprintf("namespace/%s/capacity", r.ns.Name), Note: note}},
	})
}

// checkRetention covers CFG-005 (Export on, retention still long) and
// STO-002 (retention above the default with no Export). Retained Storage
// scales with retention days, so shortening retention saves that share.
func (r *run) checkRetention() {
	days := r.ns.RetentionDays
	exporting := false
	var sinkNames []string
	for _, s := range r.sinks {
		if s.Enabled {
			exporting = true
			sinkNames = append(sinkNames, s.Name)
		}
	}
	m := r.monthlyUsage(r.usage[r.ns.Name])

	if exporting && days > r.opts.ExportRetentionDays {
		proj := &estimate.Projection{}
		if m.known {
			proj.RetainedGBHours = m.RetainedGBHours * (1 - float64(r.opts.ExportRetentionDays)/float64(days))
		}
		r.add(estimate.Finding{
			ID:         "CFG-005",
			Confidence: detect.Advisory,
			Evidence: []estimate.Evidence{{
				Setting: fmt.Sprintf("namespace/%s/retention_days", r.ns.Name),
				Note: fmt.Sprintf("Export is on (%s) and retention is %d days. Export costs %s Action per closed Workflow and doesn't lower storage; the estimate assumes %d days of retention.",
					strings.Join(sinkNames, ", "), days, estimate.FormatActions(r.notInHistory("export")), r.opts.ExportRetentionDays),
			}},
			ProjectedMonthly: proj,
		})
		return
	}
	if !exporting && days > r.opts.DefaultRetentionDays {
		proj := &estimate.Projection{}
		if m.known {
			proj.RetainedGBHours = m.RetainedGBHours * (1 - float64(r.opts.DefaultRetentionDays)/float64(days))
		}
		r.add(estimate.Finding{
			ID:         "STO-002",
			Confidence: detect.Advisory,
			Evidence: []estimate.Evidence{{
				Setting: fmt.Sprintf("namespace/%s/retention_days", r.ns.Name),
				Note: fmt.Sprintf("Retention is %d days, above the %d-day default, and nothing exports closed histories. The estimate assumes %d days.",
					days, r.opts.DefaultRetentionDays, r.opts.DefaultRetentionDays),
			}},
			ProjectedMonthly: proj,
		})
	}
}

// checkIdle is CFG-006: a Namespace that ran no Actions for IdleDays but
// still holds storage. With usage, it checks every Namespace in the
// account when AccountWide is set; without it, it counts starts in the
// analyzed Namespace through Visibility.
func (r *run) checkIdle(ctx context.Context) error {
	if r.usage != nil && r.usageDays >= min(r.opts.IdleDays, r.opts.UsageDays) {
		for name, days := range r.usage {
			if !r.opts.AccountWide && name != r.env.Reader.Namespace() {
				continue
			}
			m := r.monthlyUsage(days)
			if m.Actions > 0 || m.ActiveGBHours+m.RetainedGBHours == 0 {
				continue
			}
			r.add(estimate.Finding{
				ID:         "CFG-006",
				Confidence: detect.High,
				Evidence: []estimate.Evidence{{
					Setting: "namespace/" + name,
					Note:    fmt.Sprintf("No Actions in the last %d days of usage, but storage is still billed.", r.usageDays),
				}},
				ProjectedMonthly: &estimate.Projection{ActiveGBHours: m.ActiveGBHours, RetainedGBHours: m.RetainedGBHours},
				Keys:             []string{name},
			})
		}
		return nil
	}

	// No usage: count starts in the analyzed Namespace.
	since := r.now.Add(-time.Duration(r.opts.IdleDays) * 24 * time.Hour).UTC().Format(time.RFC3339)
	starts, err := r.count(ctx, fmt.Sprintf("StartTime > '%s'", since))
	if err != nil || starts > 0 {
		return err
	}
	stored, err := r.count(ctx, "")
	if err != nil || stored == 0 {
		return err
	}
	r.add(estimate.Finding{
		ID:         "CFG-006",
		Confidence: detect.Medium,
		Evidence: []estimate.Evidence{{
			Setting: "namespace/" + r.env.Reader.Namespace(),
			Note:    fmt.Sprintf("No Workflow started in the last %d days, and %d executions are still stored.", r.opts.IdleDays, stored),
		}},
	})
	return nil
}

func (r *run) count(ctx context.Context, query string) (int64, error) {
	if err := r.env.Reader.Wait(ctx); err != nil {
		return 0, err
	}
	resp, err := r.env.Reader.Service().CountWorkflowExecutions(ctx, &workflowservice.CountWorkflowExecutionsRequest{
		Namespace: r.env.Reader.Namespace(), Query: query,
	})
	if err != nil {
		return 0, err
	}
	return resp.GetCount(), nil
}
