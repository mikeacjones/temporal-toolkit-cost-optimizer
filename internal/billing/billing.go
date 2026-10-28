// Package billing reads the billing rules in kb/billing/actions.yaml: which
// history events and operations are billable Actions, and how many.
package billing

import (
	"fmt"
	"io/fs"
	"slices"

	enumspb "go.temporal.io/api/enums/v1"
	"gopkg.in/yaml.v3"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
	kbdata "github.com/mikeacjones/temporal-toolkit-cost-optimizer/kb"
)

// Rules are the billing rules for one knowledge-base snapshot.
type Rules struct {
	Snapshot     string `yaml:"snapshot"`
	Rules        []Rule `yaml:"rules"`
	NotInHistory []struct {
		Operation string  `yaml:"operation"`
		Actions   float64 `yaml:"actions"`
		Note      string  `yaml:"note"`
		Source    string  `yaml:"source"`
	} `yaml:"not_in_history"`

	byEvent map[enumspb.EventType]Rule
}

// Rule maps one history event, or one operation, to Actions.
type Rule struct {
	Event       string   `yaml:"event"`
	Operation   string   `yaml:"operation"`
	Actions     float64  `yaml:"actions"`
	ActionsFrom string   `yaml:"actions_from"`
	Per         string   `yaml:"per"`
	MarkerNames []string `yaml:"marker_names"`
	Source      string   `yaml:"source"`
	ExcludeIf   struct {
		SearchAttributeOnly string `yaml:"search_attribute_only"`
	} `yaml:"exclude_if"`
}

// LoadRules reads the embedded billing rules.
func LoadRules() (*Rules, error) {
	b, err := fs.ReadFile(kbdata.FS, "billing/actions.yaml")
	if err != nil {
		return nil, err
	}
	return ParseRules(b)
}

// ParseRules parses billing rules from YAML.
func ParseRules(b []byte) (*Rules, error) {
	r := &Rules{byEvent: map[enumspb.EventType]Rule{}}
	if err := yaml.Unmarshal(b, r); err != nil {
		return nil, err
	}
	for _, rule := range r.Rules {
		if rule.Event == "" {
			continue
		}
		et, ok := enumspb.EventType_value[rule.Event]
		if !ok {
			return nil, fmt.Errorf("billing rule for unknown event %q", rule.Event)
		}
		r.byEvent[enumspb.EventType(et)] = rule
	}
	return r, nil
}

// Rule returns the rule for an event type.
func (r *Rules) Rule(et enumspb.EventType) (Rule, bool) {
	rule, ok := r.byEvent[et]
	return rule, ok
}

// UpsertActions returns the Actions billed for one upsert.
func (r *Rules) UpsertActions(u *model.Upsert) float64 {
	rule, ok := r.byEvent[enumspb.EVENT_TYPE_UPSERT_WORKFLOW_SEARCH_ATTRIBUTES]
	if !ok {
		return 0
	}
	if only := rule.ExcludeIf.SearchAttributeOnly; only != "" {
		excluded := len(u.Fields) > 0
		for k := range u.Fields {
			if k != only {
				excluded = false
				break
			}
		}
		if excluded {
			return 0
		}
	}
	return rule.Actions
}

// ExecutionActions estimates the billable Actions recorded in one
// execution's history. Operations that never reach history, such as Queries
// and Heartbeats, aren't included.
func (r *Rules) ExecutionActions(ex *model.Execution) float64 {
	var total float64
	perTask := map[string]map[int64]bool{} // rule event -> Workflow Tasks already counted
	for _, ev := range ex.Events {
		rule, ok := r.byEvent[ev.GetEventType()]
		if !ok {
			continue
		}
		switch ev.GetEventType() {
		case enumspb.EVENT_TYPE_UPSERT_WORKFLOW_SEARCH_ATTRIBUTES:
			total += r.UpsertActions(&model.Upsert{Fields: ev.GetUpsertWorkflowSearchAttributesEventAttributes().GetSearchAttributes().GetIndexedFields()})
			continue
		case enumspb.EVENT_TYPE_MARKER_RECORDED:
			attrs := ev.GetMarkerRecordedEventAttributes()
			if len(rule.MarkerNames) > 0 && !slices.Contains(rule.MarkerNames, attrs.GetMarkerName()) {
				continue
			}
			if rule.Per == "workflow_task" {
				seen := perTask[rule.Event]
				if seen == nil {
					seen = map[int64]bool{}
					perTask[rule.Event] = seen
				}
				if seen[attrs.GetWorkflowTaskCompletedEventId()] {
					continue
				}
				seen[attrs.GetWorkflowTaskCompletedEventId()] = true
			}
		}
		switch rule.ActionsFrom {
		case "":
			total += rule.Actions
		case "attempt_minus_one":
			total += float64(max(ev.GetActivityTaskStartedEventAttributes().GetAttempt()-1, 0))
		case "nonfirst_local_activity_execution_attempts":
			total += float64(ev.GetWorkflowTaskCompletedEventAttributes().GetMeteringMetadata().GetNonfirstLocalActivityExecutionAttempts())
		}
	}
	return total
}

// OperationActions returns the Actions for an operation listed under
// not_in_history, such as "schedule_run" or "workflow_task_heartbeat".
func (r *Rules) OperationActions(op string) (float64, bool) {
	for _, o := range r.NotInHistory {
		if o.Operation == op {
			return o.Actions, true
		}
	}
	return 0, false
}
