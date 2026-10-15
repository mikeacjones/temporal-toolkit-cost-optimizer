// Package metrics holds the Cloud metrics checks (MET-001 to MET-006). They
// read temporal_cloud_v1_billable_action_count, which breaks billable
// Actions down by Action type and Workflow Type, and find costs that Event
// History can't show: Heartbeats, Queries, and rejected Updates. They also
// compare the metric with what the sampled histories explain, and watch for
// design changes and spikes over time.
//
// The Temporal Cloud OpenMetrics endpoint only returns the latest
// one-minute window, so MET-001 to MET-004 work from a few minutes of
// scrapes. MET-005 and MET-006 need days of data, which come from a
// Prometheus-compatible server that stores the scrapes.
//
// The metric is an estimate: it leaves out some Actions and reports
// replicated Namespaces at 1x. Findings say so.
package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/analyze"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/estimate"
)

// Collector runs the MET checks as an analyze.Collector.
type Collector struct {
	scrape  *OpenMetricsSource
	history Source // optional; nil means MET-005 and MET-006 can't run
	now     func() time.Time
	caveats []string
}

// Option configures a Collector.
type Option func(*Collector)

// WithPrometheus reads metric history from a Prometheus-compatible server
// that stores the OpenMetrics scrapes, so MET-005 and MET-006 can run, and
// MET-001 to MET-004 use a week of data instead of a few minutes. token may
// be empty.
func WithPrometheus(url, token string) Option {
	return func(c *Collector) {
		c.history = &PrometheusSource{URL: url, Token: token, Client: &http.Client{Timeout: 30 * time.Second}, Step: time.Hour}
	}
}

// WithSource replaces the history source, for tests.
func WithSource(s Source) Option { return func(c *Collector) { c.history = s } }

// WithScrapes sets how many one-minute windows to scrape from the
// OpenMetrics endpoint, and how long to wait between scrapes. More windows
// smooth out a busy minute but take longer; the account limit is 180
// requests an hour.
func WithScrapes(n int, poll time.Duration) Option {
	return func(c *Collector) { c.scrape.Scrapes, c.scrape.Poll = n, poll }
}

// WithHTTPClient replaces the HTTP client for the OpenMetrics endpoint.
func WithHTTPClient(h *http.Client) Option { return func(c *Collector) { c.scrape.Client = h } }

// WithClock replaces time.Now and the scrape wait, for tests.
func WithClock(now func() time.Time, sleep func(context.Context, time.Duration) error) Option {
	return func(c *Collector) { c.now, c.scrape.Sleep = now, sleep }
}

// New returns the MET checks. endpoint is the OpenMetrics URL, or "" for
// the default; apiKey belongs to a Service Account with the Metrics
// Read-Only role.
func New(endpoint, apiKey string, opts ...Option) *Collector {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	c := &Collector{
		scrape: &OpenMetricsSource{
			Endpoint: endpoint, APIKey: apiKey,
			Client:  &http.Client{Timeout: 15 * time.Second},
			Scrapes: 3, Poll: 30 * time.Second, MaxWait: 4 * time.Minute,
			Sleep: sleepCtx,
		},
		now: time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

var _ analyze.Collector = (*Collector)(nil)

// Name is shown in progress and caveats.
func (c *Collector) Name() string { return "Cloud metrics" }

// Caveats explains what the last Collect couldn't check. The caller adds
// them to the report.
func (c *Collector) Caveats() []string { return c.caveats }

// historyRange is how far back the history source reads: a week to
// average over, plus the latest day.
const historyRange = 8 * 24 * time.Hour

// Collect reads the metrics and runs the checks.
func (c *Collector) Collect(ctx context.Context, env *analyze.Env) ([]estimate.Finding, error) {
	c.caveats = nil
	ns := env.Reader.Namespace()
	metrics := []string{metricBillable, metricOpenWorkflows}

	var ds *Dataset
	var err error
	if c.history != nil {
		end := c.now().UTC().Truncate(time.Hour)
		ds, err = c.history.Fetch(ctx, ns, metrics, end.Add(-historyRange), end)
	} else {
		ds, err = c.scrape.Fetch(ctx, ns, metrics, time.Time{}, time.Time{})
	}
	if err != nil {
		return nil, err
	}

	in := newInput(ds, env)
	if in.openAvg < 0 {
		for _, wt := range in.types() {
			if in.sum(wt, actQuery) > 0 {
				c.caveats = append(c.caveats, "MET-002 didn't run: the metrics had Queries but no open-Workflow count to compare them with.")
				break
			}
		}
	}
	var out []estimate.Finding
	out = append(out, heartbeatHeavy(in)...)
	out = append(out, queryPolling(in)...)
	out = append(out, rejectedUpdates(in)...)
	out = append(out, unexplainedMix(in)...)
	if c.history != nil && c.history.History() {
		out = append(out, designChange(in)...)
		out = append(out, usageSpike(in)...)
	} else {
		c.caveats = append(c.caveats, "MET-005 and MET-006 need days of metric history. The OpenMetrics endpoint only returns the latest minute, so point tcopt at a Prometheus that stores the scrapes to run them.")
	}
	if ds.Covered < time.Hour {
		c.caveats = append(c.caveats, "Metric checks used "+ds.Description+"; monthly figures scale that up, so a busy or quiet minute moves them.")
	}
	c.caveats = append(c.caveats, "billable_action_count leaves out some Actions, such as Export and Fairness, and counts replicated Namespaces once.")
	for i := range out {
		out[i].Source = estimate.SourceMetrics
		out[i].Guidance = out[i].ID
		if out[i].Title == "" {
			out[i].Title = env.Titles[out[i].ID]
		}
	}
	estimate.SortFindings(out)
	return out, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
