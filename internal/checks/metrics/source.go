package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Metric names the checks read.
const (
	metricBillable      = "temporal_cloud_v1_billable_action_count"
	metricOpenWorkflows = "temporal_cloud_v1_namespace_open_workflows"
)

// Label names on Temporal Cloud metrics.
const (
	labelNamespace    = "temporal_namespace"
	labelWorkflowType = "temporal_workflow_type"
	labelActionType   = "action_type"
)

// Point is one aggregation window. Rate is the per-second rate averaged
// over the Seconds that end at Time; for a value metric such as open
// Workflows it's the value itself.
type Point struct {
	Time    time.Time
	Seconds float64
	Rate    float64
}

// Series is one metric with one set of labels.
type Series struct {
	Metric string
	Labels map[string]string
	Points []Point
}

// Dataset is what a Source returned for one Namespace.
type Dataset struct {
	Series []Series
	// Start and End bound the windows the data covers, and Covered is the
	// total length of the distinct windows, which can be less than End
	// minus Start when scrapes were sparse.
	Start, End time.Time
	Covered    time.Duration
	// Description says where the data came from, for evidence, such as
	// "3 one-minute windows from the OpenMetrics endpoint".
	Description string
}

// Source reads metric series for a Namespace.
type Source interface {
	// Fetch returns the series for metrics in namespace between start and
	// end. A source may cover less than asked; the OpenMetrics endpoint
	// only has the latest minute. The Dataset says what it covers.
	Fetch(ctx context.Context, namespace string, metrics []string, start, end time.Time) (*Dataset, error)
	// History reports whether the source can return days of data, which
	// MET-005 and MET-006 need.
	History() bool
}

// DefaultEndpoint is the Temporal Cloud OpenMetrics endpoint.
const DefaultEndpoint = "https://metrics.temporal.io/v1/metrics"

// OpenMetricsSource scrapes the Temporal Cloud OpenMetrics endpoint. Each
// scrape returns only the most recently completed one-minute window, so it
// scrapes Scrapes times, waiting for a new window between them. The
// account's limit is 180 requests an hour.
//
// https://docs.temporal.io/cloud/metrics/openmetrics/api-reference
type OpenMetricsSource struct {
	Endpoint string
	APIKey   string
	Client   *http.Client
	// Scrapes is the number of distinct one-minute windows to collect.
	Scrapes int
	// Poll is how long to wait before scraping again for a new window.
	Poll time.Duration
	// MaxWait bounds the whole collection.
	MaxWait time.Duration
	// Sleep waits; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// History is false: the endpoint has no historical data.
func (s *OpenMetricsSource) History() bool { return false }

// Fetch scrapes the endpoint until it has s.Scrapes distinct windows or
// s.MaxWait passes. start and end are ignored.
func (s *OpenMetricsSource) Fetch(ctx context.Context, namespace string, metrics []string, _, _ time.Time) (*Dataset, error) {
	q := url.Values{}
	q.Add("namespaces", namespace)
	for _, m := range metrics {
		q.Add("metrics", m)
	}
	u := s.Endpoint + "?" + q.Encode()

	type key struct {
		metric, labels string
	}
	series := map[key]*Series{}
	windows := map[time.Time]bool{}
	deadline := time.Now().Add(s.MaxWait)
	for attempt := 0; len(windows) < s.Scrapes; attempt++ {
		if attempt > 0 {
			if time.Now().After(deadline) {
				break
			}
			if err := s.Sleep(ctx, s.Poll); err != nil {
				return nil, err
			}
		}
		samples, err := s.scrape(ctx, u)
		if err != nil {
			if len(windows) > 0 {
				break // keep what we have
			}
			return nil, err
		}
		for _, sm := range samples {
			t := sm.Time
			if t.IsZero() {
				t = time.Now().UTC().Truncate(time.Minute)
			}
			// Any sample dates the window, even one for another Namespace.
			windows[t] = true
			if !namespaceMatches(sm.Labels[labelNamespace], namespace) {
				continue
			}
			k := key{sm.Name, labelKey(sm.Labels)}
			se := series[k]
			if se == nil {
				se = &Series{Metric: sm.Name, Labels: sm.Labels}
				series[k] = se
			}
			if !hasPoint(se.Points, t) {
				se.Points = append(se.Points, Point{Time: t, Seconds: 60, Rate: sm.Value})
			}
		}
		if len(samples) == 0 {
			// Nothing was reported this minute; count the window anyway so an
			// idle Namespace doesn't scrape until MaxWait.
			windows[time.Now().UTC().Truncate(time.Minute)] = true
		}
	}

	ds := &Dataset{Covered: time.Duration(len(windows)) * time.Minute}
	for t := range windows {
		if ds.Start.IsZero() || t.Add(-time.Minute).Before(ds.Start) {
			ds.Start = t.Add(-time.Minute)
		}
		if t.After(ds.End) {
			ds.End = t
		}
	}
	for _, se := range series {
		ds.Series = append(ds.Series, *se)
	}
	sortSeries(ds.Series)
	ds.Description = fmt.Sprintf("%d one-minute %s from the OpenMetrics endpoint", len(windows), plural(len(windows), "window"))
	return ds, nil
}

func (s *OpenMetricsSource) scrape(ctx context.Context, u string) ([]sample, error) {
	for try := 0; ; try++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+s.APIKey)
		req.Header.Set("Accept", "application/openmetrics-text, text/plain")
		resp, err := s.Client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("scrape %s: %w", s.Endpoint, err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode == http.StatusTooManyRequests && try == 0:
			wait := 30 * time.Second
			if sec, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && sec > 0 && sec <= 120 {
				wait = time.Duration(sec) * time.Second
			}
			if err := s.Sleep(ctx, wait); err != nil {
				return nil, err
			}
			continue
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return nil, fmt.Errorf("the OpenMetrics endpoint refused the API key (HTTP %d); it needs a Service Account key with the Metrics Read-Only role", resp.StatusCode)
		case resp.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("scrape %s: HTTP %d: %s", s.Endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return parseText(strings.NewReader(string(body)))
	}
}

// PrometheusSource reads metric history from a Prometheus-compatible server
// that stores the OpenMetrics scrapes, such as the user's own Prometheus,
// Grafana Mimir, or Thanos, through the query_range HTTP API.
//
// https://prometheus.io/docs/prometheus/latest/querying/api/#range-queries
type PrometheusSource struct {
	// URL is the server's base URL, such as http://prometheus:9090.
	URL string
	// Token, when set, is sent as a Bearer token.
	Token  string
	Client *http.Client
	// Step is the resolution of the returned points.
	Step time.Duration
}

// History is true.
func (p *PrometheusSource) History() bool { return true }

// groupBy lists the labels each metric keeps when summed across regions.
var groupBy = map[string][]string{
	metricBillable:      {labelWorkflowType, labelActionType},
	metricOpenWorkflows: nil,
}

// Fetch runs one query_range per metric, averaging each series over Step
// and summing across regions.
func (p *PrometheusSource) Fetch(ctx context.Context, namespace string, metrics []string, start, end time.Time) (*Dataset, error) {
	step := p.Step
	if step <= 0 {
		step = time.Hour
	}
	ds := &Dataset{Start: start, End: end, Covered: end.Sub(start)}
	matcher := fmt.Sprintf(`%s=~"%s"`, labelNamespace, namespaceRegex(namespace))
	for _, m := range metrics {
		by := ""
		if labels := groupBy[m]; len(labels) > 0 {
			by = " by (" + strings.Join(labels, ", ") + ")"
		}
		query := fmt.Sprintf("sum%s (avg_over_time(%s{%s}[%s]))", by, m, matcher, promDuration(step))
		q := url.Values{}
		q.Set("query", query)
		q.Set("start", strconv.FormatInt(start.Unix(), 10))
		q.Set("end", strconv.FormatInt(end.Unix(), 10))
		q.Set("step", strconv.Itoa(int(step.Seconds())))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.URL, "/")+"/api/v1/query_range?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if p.Token != "" {
			req.Header.Set("Authorization", "Bearer "+p.Token)
		}
		resp, err := p.Client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", p.URL, err)
		}
		var body struct {
			Status string `json:"status"`
			Error  string `json:"error"`
			Data   struct {
				Result []struct {
					Metric map[string]string `json:"metric"`
					Values [][2]any          `json:"values"`
				} `json:"result"`
			} `json:"data"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", p.URL, err)
		}
		if body.Status != "success" {
			return nil, fmt.Errorf("query %s: %s", p.URL, body.Error)
		}
		for _, r := range body.Data.Result {
			se := Series{Metric: m, Labels: r.Metric}
			for _, v := range r.Values {
				ts, _ := v[0].(float64)
				str, _ := v[1].(string)
				val, err := strconv.ParseFloat(str, 64)
				if err != nil {
					continue
				}
				se.Points = append(se.Points, Point{Time: time.Unix(int64(ts), 0).UTC(), Seconds: step.Seconds(), Rate: val})
			}
			ds.Series = append(ds.Series, se)
		}
	}
	sortSeries(ds.Series)
	ds.Description = fmt.Sprintf("%s of stored metrics at %s resolution", humanDuration(end.Sub(start)), humanDuration(step))
	return ds, nil
}

// namespaceMatches accepts the Namespace's full name (name.account) or
// the name alone, since metric labels may carry either.
func namespaceMatches(label, namespace string) bool {
	if label == "" || label == namespace {
		return true
	}
	short, _, _ := strings.Cut(namespace, ".")
	return label == short
}

func namespaceRegex(namespace string) string {
	short, _, _ := strings.Cut(namespace, ".")
	esc := func(s string) string { return strings.ReplaceAll(regexpQuote(s), `\`, `\\`) }
	if short == namespace {
		return esc(namespace)
	}
	return esc(namespace) + "|" + esc(short)
}

func regexpQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func promDuration(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func labelKey(l map[string]string) string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + l[k] + ",")
	}
	return b.String()
}

func hasPoint(ps []Point, t time.Time) bool {
	for _, p := range ps {
		if p.Time.Equal(t) {
			return true
		}
	}
	return false
}

func sortSeries(ss []Series) {
	sort.Slice(ss, func(i, j int) bool {
		if ss[i].Metric != ss[j].Metric {
			return ss[i].Metric < ss[j].Metric
		}
		return labelKey(ss[i].Labels) < labelKey(ss[j].Labels)
	})
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func humanDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d == time.Hour:
		return "1 hour"
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	return d.String()
}
