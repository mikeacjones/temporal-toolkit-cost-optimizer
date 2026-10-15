package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/analyze"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/estimate"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/history"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "metrics", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func env(ns string, types ...analyze.TypeSummary) *analyze.Env {
	return &analyze.Env{Reader: history.NewReader(nil, ns, 1, ""), Types: types, Titles: map[string]string{}}
}

func noSleep(context.Context, time.Duration) error { return nil }

func byID(fs []estimate.Finding) map[string][]estimate.Finding {
	out := map[string][]estimate.Finding{}
	for _, f := range fs {
		out[f.ID] = append(out[f.ID], f)
	}
	return out
}

func TestParseText(t *testing.T) {
	in := `# TYPE m gauge
m{a="x",b="say \"hi\"\\"} 1.5 1792083600000
m{a="y"} 2 1792083600.5
m 3
# EOF`
	got, err := parseText(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d samples", len(got))
	}
	if got[0].Labels["b"] != `say "hi"\` || got[0].Value != 1.5 || got[0].Time.UnixMilli() != 1792083600000 {
		t.Errorf("first sample %+v", got[0])
	}
	if got[1].Time.UnixMilli() != 1792083600500 {
		t.Errorf("seconds timestamp read as %v", got[1].Time)
	}
	if !got[2].Time.IsZero() || len(got[2].Labels) != 0 {
		t.Errorf("third sample %+v", got[2])
	}
}

// scrapeServer serves the window fixtures in turn, then repeats the last.
func scrapeServer(t *testing.T, windows ...string) (*httptest.Server, *atomic.Int32) {
	var calls atomic.Int32
	bodies := make([][]byte, len(windows))
	for i, w := range windows {
		bodies[i] = fixture(t, w)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("Access Denied"))
			return
		}
		if r.URL.Query().Get("namespaces") == "" || len(r.URL.Query()["metrics"]) == 0 {
			t.Errorf("scrape without filters: %s", r.URL.RawQuery)
		}
		n := int(calls.Add(1)) - 1
		w.Header().Set("Content-Type", "application/openmetrics-text")
		_, _ = w.Write(bodies[min(n, len(bodies)-1)])
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestCollectFromScrapes(t *testing.T) {
	srv, calls := scrapeServer(t, "scrape-window-1.txt", "scrape-window-1.txt", "scrape-window-2.txt")
	c := New(srv.URL, "test-key", WithScrapes(2, time.Second), WithClock(time.Now, noSleep))
	fs, err := c.Collect(context.Background(), env("orders.a1b2c",
		analyze.TypeSummary{WorkflowType: "OrderWorkflow", MonthlyActions: 10_000_000},
		analyze.TypeSummary{WorkflowType: "ReminderWorkflow", MonthlyActions: 500_000},
	))
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Errorf("made %d scrapes, want 3 (one repeated window)", calls.Load())
	}
	got := byID(fs)
	// Two one-minute windows scale to a month by 30 days / 120 s.
	const scale = 30 * 24 * 3600 / 120.0

	met1 := got["MET-001"]
	if len(met1) != 1 || met1[0].WorkflowType != "OrderWorkflow" {
		t.Fatalf("MET-001: %+v", met1)
	}
	// 26 Heartbeats/s against 6.5 attempts/s, over 120 s; the other
	// Namespace's series is ignored.
	if want := (26.0 - 6.5) * 120 * scale; met1[0].ProjectedMonthly.Actions != want {
		t.Errorf("MET-001 saves %v, want %v", met1[0].ProjectedMonthly.Actions, want)
	}

	met2 := got["MET-002"]
	if len(met2) != 1 || met2[0].WorkflowType != "ReminderWorkflow" {
		t.Fatalf("MET-002: %+v", met2)
	}
	if !strings.Contains(met2[0].Evidence[0].Note, "180.0 per open Workflow per hour") {
		t.Errorf("MET-002 evidence: %s", met2[0].Evidence[0].Note)
	}

	met3 := got["MET-003"]
	if len(met3) != 1 || met3[0].WorkflowType != "SlotWorkflow" || met3[0].ProjectedMonthly.Actions != 3*120*scale {
		t.Fatalf("MET-003: %+v", met3)
	}

	met4 := got["MET-004"]
	if len(met4) != 1 || met4[0].WorkflowType != "OrderWorkflow" || met4[0].ProjectedMonthly != nil {
		t.Fatalf("MET-004: %+v", met4)
	}
	if len(got["MET-005"])+len(got["MET-006"]) != 0 {
		t.Error("MET-005 and MET-006 need history and shouldn't run on scrapes")
	}
	for _, f := range fs {
		if f.Source != estimate.SourceMetrics || f.Guidance != f.ID || f.Evidence[0].Metric == "" || f.Evidence[0].Window == "" {
			t.Errorf("%s: missing source, guidance, or evidence: %+v", f.ID, f)
		}
	}
	if !strings.Contains(strings.Join(c.Caveats(), " "), "MET-005 and MET-006") {
		t.Errorf("caveats should say why MET-005 and MET-006 didn't run: %v", c.Caveats())
	}
}

func TestQuietNamespaceHasNoFindings(t *testing.T) {
	srv, _ := scrapeServer(t, "scrape-window-1.txt")
	c := New(srv.URL, "test-key", WithScrapes(1, time.Second), WithClock(time.Now, noSleep))
	fs, err := c.Collect(context.Background(), env("quiet.a1b2c"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 0 {
		t.Errorf("got findings for a Namespace with no series: %+v", fs)
	}
}

func TestForbiddenKey(t *testing.T) {
	srv, _ := scrapeServer(t, "scrape-window-1.txt")
	c := New(srv.URL, "wrong", WithScrapes(1, time.Second), WithClock(time.Now, noSleep))
	_, err := c.Collect(context.Background(), env("orders.a1b2c"))
	if err == nil || !strings.Contains(err.Error(), "Metrics Read-Only") {
		t.Errorf("want an error naming the role, got %v", err)
	}
}

func TestRetryAfter429(t *testing.T) {
	var calls atomic.Int32
	body := fixture(t, "scrape-window-1.txt")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	var slept time.Duration
	c := New(srv.URL, "k", WithScrapes(1, time.Second),
		WithClock(time.Now, func(_ context.Context, d time.Duration) error { slept += d; return nil }))
	if _, err := c.Collect(context.Background(), env("orders.a1b2c")); err != nil {
		t.Fatal(err)
	}
	if slept != 7*time.Second {
		t.Errorf("waited %v, want the Retry-After of 7s", slept)
	}
}

func TestNamespaceMatches(t *testing.T) {
	for _, tc := range []struct {
		label string
		want  bool
	}{{"orders.a1b2c", true}, {"orders", true}, {"", true}, {"other.a1b2c", false}, {"orders.zzz", false}} {
		if got := namespaceMatches(tc.label, "orders.a1b2c"); got != tc.want {
			t.Errorf("namespaceMatches(%q) = %v", tc.label, got)
		}
	}
	if got := namespaceRegex("orders.a1b2c"); got != `orders\\.a1b2c|orders` {
		t.Errorf("namespaceRegex = %s", got)
	}
}

// fakeHistory returns a fixed dataset, standing in for Prometheus.
type fakeHistory struct{ ds *Dataset }

func (f fakeHistory) Fetch(context.Context, string, []string, time.Time, time.Time) (*Dataset, error) {
	return f.ds, nil
}
func (fakeHistory) History() bool { return true }

// hourly builds a billable-action series with one point an hour across the
// range; rate(t) gives the per-second rate for the hour ending at t.
func hourly(wt, action string, start, end time.Time, rate func(t time.Time) float64) Series {
	s := Series{Metric: metricBillable, Labels: map[string]string{labelWorkflowType: wt, labelActionType: action}}
	for t := start.Add(time.Hour); !t.After(end); t = t.Add(time.Hour) {
		s.Points = append(s.Points, Point{Time: t, Seconds: 3600, Rate: rate(t)})
	}
	return s
}

func TestDesignChangeAndSpike(t *testing.T) {
	end := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	start := end.Add(-historyRange)
	recent := func(t time.Time) bool { return t.After(end.Add(-24 * time.Hour)) }
	step := func(before, after float64) func(time.Time) float64 {
		return func(t time.Time) float64 {
			if recent(t) {
				return after
			}
			return before
		}
	}
	ds := &Dataset{Start: start, End: end, Covered: historyRange, Description: "8 days of stored metrics at 1 hour resolution", Series: []Series{
		// PollWorkflow: a polling loop added in the latest day; Activities
		// and Timers per start go from 1 to 3 together.
		hourly("PollWorkflow", "start_workflow", start, end, step(1, 1)),
		hourly("PollWorkflow", "schedule_activity", start, end, step(1, 3)),
		hourly("PollWorkflow", "start_timer", start, end, step(1, 3)),
		// SteadyWorkflow: no change.
		hourly("SteadyWorkflow", "start_workflow", start, end, step(1, 1)),
		hourly("SteadyWorkflow", "schedule_activity", start, end, step(2, 2)),
		hourly("SteadyWorkflow", "start_timer", start, end, step(1, 1)),
	}}
	c := New("", "", WithSource(fakeHistory{ds}), WithClock(func() time.Time { return end }, noSleep))
	fs, err := c.Collect(context.Background(), env("orders.a1b2c"))
	if err != nil {
		t.Fatal(err)
	}
	got := byID(fs)
	met5 := got["MET-005"]
	if len(met5) != 1 || met5[0].WorkflowType != "PollWorkflow" {
		t.Fatalf("MET-005: %+v", met5)
	}
	// 4 more Actions per start at 1 start/s for a 30-day month.
	if want := 4.0 * 86400 * 30; met5[0].ProjectedMonthly.Actions != want {
		t.Errorf("MET-005 projects %v, want %v", met5[0].ProjectedMonthly.Actions, want)
	}
	// PollWorkflow's Actions went from 3/s to 7/s, and the Namespace from
	// 7/s to 11/s: a Workflow Type spike, but not a Namespace one.
	met6 := got["MET-006"]
	if len(met6) != 1 || met6[0].WorkflowType != "PollWorkflow" {
		t.Fatalf("MET-006: %+v", met6)
	}
	for _, cav := range c.Caveats() {
		if strings.Contains(cav, "MET-005 and MET-006") {
			t.Error("history was available; MET-005 and MET-006 should have run")
		}
	}
}

func TestPrometheusSource(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("unexpected request %s %v", r.URL.Path, r.Header)
		}
		queries = append(queries, r.URL.Query().Get("query"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"temporal_workflow_type":"OrderWorkflow","action_type":"start_workflow"},"values":[[1792080000,"2"],[1792083600,"3"]]}]}}`))
	}))
	defer srv.Close()
	p := &PrometheusSource{URL: srv.URL, Token: "tok", Client: srv.Client(), Step: time.Hour}
	end := time.Unix(1792083600, 0)
	ds, err := p.Fetch(context.Background(), "orders.a1b2c", []string{metricBillable}, end.Add(-2*time.Hour), end)
	if err != nil {
		t.Fatal(err)
	}
	want := `sum by (temporal_workflow_type, action_type) (avg_over_time(temporal_cloud_v1_billable_action_count{temporal_namespace=~"orders\\.a1b2c|orders"}[1h]))`
	if len(queries) != 1 || queries[0] != want {
		t.Errorf("query = %q\nwant    %q", queries, want)
	}
	if len(ds.Series) != 1 || total(ds.Series[0].Points) != 5*3600 {
		t.Errorf("series %+v", ds.Series)
	}
}
