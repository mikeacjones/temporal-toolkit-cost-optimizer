package searchattr

import (
	"fmt"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

// QuickGap is how soon one Workflow Task's upsert must follow the
// previous one for SA-003: a value that only stays visible this long
// isn't worth an Action.
const QuickGap = 2 * time.Second

func init() {
	detect.Register(sa001{})
	detect.Register(sa003{})
}

// sa001 finds Search Attributes upserted in the first Workflow Task, before
// any Activity, Timer, Signal, or Update could change them. They could go in
// the start options, which aren't billed.
type sa001 struct{}

func (sa001) ID() string                    { return "SA-001" }
func (sa001) Confidence() detect.Confidence { return detect.Medium }

func (sa001) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	if len(ex.WorkflowTasks) == 0 {
		return nil
	}
	first := ex.WorkflowTasks[0]
	for _, et := range first.After {
		if et == enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED || et == enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_UPDATE_ACCEPTED {
			return nil // the values may come from the message
		}
	}
	var ids []int64
	keys := map[string]bool{}
	var actions float64
	for _, u := range ex.Upserts {
		if u.WorkflowTask != first {
			continue
		}
		a := c.Rules.UpsertActions(u)
		if a == 0 {
			continue
		}
		if actions == 0 {
			actions = a
		}
		ids = append(ids, u.EventID)
		for _, k := range u.BillableKeys() {
			keys[k] = true
		}
	}
	if len(ids) == 0 {
		return nil
	}
	ks := sortedKeys(keys)
	return []detect.Match{{
		EventIDs: ids,
		Note:     fmt.Sprintf("upserted %s in the first Workflow Task, before anything could change them", strings.Join(ks, ", ")),
		// Moving them to the start saves one upsert; SA-002 counts any extra
		// upserts in the same task.
		ActionsSaved: actions,
		Keys:         ks,
	}}
}

// sa003 finds upserts in consecutive Workflow Tasks, each following the
// last within QuickGap with no Signal or Update in between. Setting them
// together at the later point keeps the final values. The first Workflow
// Task is left to SA-001.
type sa003 struct{}

func (sa003) ID() string                    { return "SA-003" }
func (sa003) Confidence() detect.Confidence { return detect.Medium }

func (sa003) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	// The first billable upsert in each Workflow Task, in task order.
	type taskUpsert struct {
		wt *model.WorkflowTask
		u  *model.Upsert
	}
	var seq []taskUpsert
	seen := map[*model.WorkflowTask]bool{}
	for _, u := range ex.Upserts {
		if u.WorkflowTask == nil || u.WorkflowTask.Index == 0 || seen[u.WorkflowTask] || c.Rules.UpsertActions(u) == 0 {
			continue
		}
		seen[u.WorkflowTask] = true
		seq = append(seq, taskUpsert{u.WorkflowTask, u})
	}

	continues := func(prev, next taskUpsert) bool {
		if next.wt.Index != prev.wt.Index+1 || next.wt.CompletedTime.Sub(prev.wt.CompletedTime) > QuickGap {
			return false
		}
		for _, et := range next.wt.After {
			if et == enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED || et == enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_UPDATE_ACCEPTED {
				return false
			}
		}
		return true
	}

	var out []detect.Match
	for i := 0; i < len(seq); {
		j := i
		for j+1 < len(seq) && continues(seq[j], seq[j+1]) {
			j++
		}
		if j > i {
			ids := make([]int64, 0, j-i+1)
			keys := map[string]bool{}
			var saved float64
			for k, tu := range seq[i : j+1] {
				ids = append(ids, tu.u.EventID)
				for _, key := range tu.u.BillableKeys() {
					keys[key] = true
				}
				if k > 0 {
					saved += c.Rules.UpsertActions(tu.u)
				}
			}
			ks := sortedKeys(keys)
			out = append(out, detect.Match{
				EventIDs: ids,
				Note: fmt.Sprintf("%d upserts of %s in consecutive Workflow Tasks within %s, with no Signal or Update between",
					j-i+1, strings.Join(ks, ", "), seq[j].wt.CompletedTime.Sub(seq[i].wt.CompletedTime).Round(time.Millisecond)),
				ActionsSaved: saved,
				Keys:         ks,
			})
		}
		i = j + 1
	}
	return out
}
