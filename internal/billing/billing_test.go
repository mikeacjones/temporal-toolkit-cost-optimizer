package billing

import (
	"os"
	"path/filepath"
	"testing"

	"go.temporal.io/sdk/client"

	commonpb "go.temporal.io/api/common/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/model"
)

func TestUpsertActions(t *testing.T) {
	r, err := LoadRules()
	if err != nil {
		t.Fatal(err)
	}
	p := &commonpb.Payload{Data: []byte(`"x"`)}
	cases := []struct {
		name   string
		fields map[string]*commonpb.Payload
		want   float64
	}{
		{"one attribute", map[string]*commonpb.Payload{"OrderStatus": p}, 1},
		{"several attributes are one Action", map[string]*commonpb.Payload{"OrderStatus": p, "Region": p, "Tier": p}, 1},
		{"TemporalChangeVersion only is free", map[string]*commonpb.Payload{model.ChangeVersionKey: p}, 0},
		{"TemporalChangeVersion with another attribute is billed", map[string]*commonpb.Payload{model.ChangeVersionKey: p, "Tier": p}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.UpsertActions(&model.Upsert{Fields: tc.fields}); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRulesParse(t *testing.T) {
	r, err := LoadRules()
	if err != nil {
		t.Fatal(err)
	}
	if r.Snapshot == "" {
		t.Error("billing rules have no snapshot date")
	}
	if _, err := ParseRules([]byte("rules:\n  - event: EVENT_TYPE_NOT_REAL\n    actions: 1\n")); err == nil {
		t.Error("expected an error for an unknown event type")
	}
}

func TestExecutionActionsOnGoldens(t *testing.T) {
	r, err := LoadRules()
	if err != nil {
		t.Fatal(err)
	}
	// start + Activity + upserts; the TemporalChangeVersion upsert and the
	// version marker are free.
	cases := map[string]float64{
		"sa002_positive":   1 + 1 + 3,
		"sa002_negative":   1 + 1 + 1,
		"sa_changeversion": 1 + 1 + 1,
		"sa004_positive":   1 + 2 + 2,
	}
	for name, want := range cases {
		f, err := os.Open(filepath.Join("..", "..", "testdata", "histories", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		h, err := client.HistoryFromJSON(f, client.HistoryJSONOptions{})
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got := r.ExecutionActions(model.Normalize("wf", "run", h)); got != want {
			t.Errorf("%s: %v Actions, want %v", name, got, want)
		}
	}
}
