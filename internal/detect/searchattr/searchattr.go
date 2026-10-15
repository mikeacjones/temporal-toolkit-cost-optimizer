// Package searchattr holds the Search Attribute detectors (SA-*).
package searchattr

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

func init() {
	detect.Register(sa002{})
	detect.Register(sa004{})
}

// sa002 finds two or more billable upserts from the same Workflow Task.
type sa002 struct{}

func (sa002) ID() string                    { return "SA-002" }
func (sa002) Confidence() detect.Confidence { return detect.High }

func (sa002) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	rules := c.Rules
	byTask := map[int64][]*model.Upsert{}
	var order []int64
	for _, u := range ex.Upserts {
		if u.WorkflowTask == nil || rules.UpsertActions(u) == 0 {
			continue
		}
		id := u.WorkflowTask.CompletedEventID
		if _, seen := byTask[id]; !seen {
			order = append(order, id)
		}
		byTask[id] = append(byTask[id], u)
	}

	var out []detect.Match
	for _, id := range order {
		ups := byTask[id]
		if len(ups) < 2 {
			continue
		}
		var billed float64
		ids := make([]int64, 0, len(ups))
		keySet := map[string]bool{}
		for _, u := range ups {
			billed += rules.UpsertActions(u)
			ids = append(ids, u.EventID)
			for _, k := range u.BillableKeys() {
				keySet[k] = true
			}
		}
		keys := sortedKeys(keySet)
		out = append(out, detect.Match{
			EventIDs: ids,
			Note: fmt.Sprintf("%d upserts in the Workflow Task completed at event %d (%s)",
				len(ups), id, strings.Join(keys, ", ")),
			// One combined upsert still costs one Action.
			ActionsSaved: billed - rules.UpsertActions(ups[0]),
			Keys:         keys,
		})
	}
	return out
}

// sa004 finds upserts that set every attribute to the value it already had.
type sa004 struct{}

func (sa004) ID() string                    { return "SA-004" }
func (sa004) Confidence() detect.Confidence { return detect.High }

func (sa004) Detect(ex *model.Execution, c *detect.Context) []detect.Match {
	rules := c.Rules
	var out []detect.Match
	for _, u := range ex.Upserts {
		actions := rules.UpsertActions(u)
		if actions == 0 || !u.Unchanged() {
			continue
		}
		keys := u.BillableKeys()
		out = append(out, detect.Match{
			EventIDs:     []int64{u.EventID},
			Note:         fmt.Sprintf("upsert at event %d sets %s to the value it already had", u.EventID, strings.Join(keys, ", ")),
			ActionsSaved: actions,
			Keys:         keys,
		})
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
