// Package history reads the Namespace being analyzed: it ranks Workflow
// Types, samples executions, and fetches their histories through a rate
// limiter and an on-disk cache. Nothing here is a billable Action, but every
// call counts against the Namespace's requests per second.
package history

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/proto"
)

// DefaultRPS is the default request rate against the Namespace.
const DefaultRPS = 10

// Reader reads one Namespace.
type Reader struct {
	svc       workflowservice.WorkflowServiceClient
	namespace string
	limiter   *rate.Limiter
	cacheDir  string // empty disables the cache
}

// NewReader returns a Reader that makes at most rps requests per second and
// caches closed histories under cacheDir.
func NewReader(svc workflowservice.WorkflowServiceClient, namespace string, rps float64, cacheDir string) *Reader {
	if rps <= 0 {
		rps = DefaultRPS
	}
	return &Reader{
		svc:       svc,
		namespace: namespace,
		limiter:   rate.NewLimiter(rate.Limit(rps), 1),
		cacheDir:  cacheDir,
	}
}

// Namespace returns the Namespace this Reader reads.
func (r *Reader) Namespace() string { return r.namespace }

// DefaultCacheDir returns ~/.cache/tcopt/histories, or the platform's cache
// directory.
func DefaultCacheDir() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "tcopt", "histories")
}

// TypeCount is a Workflow Type and how many executions of it started in the
// window.
type TypeCount struct {
	WorkflowType string
	// Seen is the number of executions in the listing sample.
	Seen int
	// Executions is the exact count from CountWorkflowExecutions, or -1 if it
	// wasn't counted.
	Executions int64
}

// RankTypes lists up to limit executions that started in the window and
// counts them by Workflow Type, most first. Visibility has no GROUP BY
// WorkflowType, so this is a sample when the window holds more than limit.
// It then counts each type exactly.
func (r *Reader) RankTypes(ctx context.Context, window time.Duration, limit int) ([]TypeCount, error) {
	query := fmt.Sprintf("StartTime > '%s'", time.Now().Add(-window).UTC().Format(time.RFC3339))
	seen := map[string]int{}
	var token []byte
	total := 0
	for total < limit {
		page := min(limit-total, 1000)
		if err := r.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		resp, err := r.svc.ListWorkflowExecutions(ctx, &workflowservice.ListWorkflowExecutionsRequest{
			Namespace: r.namespace, PageSize: int32(page), NextPageToken: token, Query: query,
		})
		if err != nil {
			return nil, fmt.Errorf("list executions: %w", err)
		}
		for _, ex := range resp.GetExecutions() {
			seen[ex.GetType().GetName()]++
		}
		total += len(resp.GetExecutions())
		token = resp.GetNextPageToken()
		if len(token) == 0 {
			break
		}
	}

	out := make([]TypeCount, 0, len(seen))
	for t, n := range seen {
		out = append(out, TypeCount{WorkflowType: t, Seen: n, Executions: -1})
	}
	for i := range out {
		n, err := r.Count(ctx, out[i].WorkflowType, window)
		if err == nil {
			out[i].Executions = n
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Executions != b.Executions {
			return a.Executions > b.Executions
		}
		if a.Seen != b.Seen {
			return a.Seen > b.Seen
		}
		return a.WorkflowType < b.WorkflowType
	})
	return out, nil
}

// Count returns how many executions of a Workflow Type started in the window.
func (r *Reader) Count(ctx context.Context, workflowType string, window time.Duration) (int64, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return 0, err
	}
	resp, err := r.svc.CountWorkflowExecutions(ctx, &workflowservice.CountWorkflowExecutionsRequest{
		Namespace: r.namespace, Query: typeQuery(workflowType, window),
	})
	if err != nil {
		return 0, fmt.Errorf("count %s: %w", workflowType, err)
	}
	return resp.GetCount(), nil
}

// Ref identifies one execution in a sample.
type Ref struct {
	WorkflowID string
	RunID      string
	Status     enumspb.WorkflowExecutionStatus
	StartTime  time.Time
	HistoryLen int64
}

// Closed reports whether the execution has closed, so its history won't
// change.
func (r Ref) Closed() bool {
	return r.Status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING &&
		r.Status != enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED
}

// Sample lists up to poolSize executions of a Workflow Type that started in
// the window, then picks n of them spread across close status, history
// length, and start time.
func (r *Reader) Sample(ctx context.Context, workflowType string, window time.Duration, n, poolSize int) ([]Ref, error) {
	var pool []Ref
	var token []byte
	for len(pool) < poolSize {
		if err := r.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		resp, err := r.svc.ListWorkflowExecutions(ctx, &workflowservice.ListWorkflowExecutionsRequest{
			Namespace: r.namespace, PageSize: int32(min(poolSize-len(pool), 1000)),
			NextPageToken: token, Query: typeQuery(workflowType, window),
		})
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", workflowType, err)
		}
		for _, ex := range resp.GetExecutions() {
			pool = append(pool, Ref{
				WorkflowID: ex.GetExecution().GetWorkflowId(),
				RunID:      ex.GetExecution().GetRunId(),
				Status:     ex.GetStatus(),
				StartTime:  ex.GetStartTime().AsTime(),
				HistoryLen: ex.GetHistoryLength(),
			})
		}
		token = resp.GetNextPageToken()
		if len(token) == 0 {
			break
		}
	}
	return Stratify(pool, n), nil
}

// Stratify picks n refs from pool. It groups the pool by close status and
// history-length quartile, gives each group a share of n proportional to its
// size (at least one), and within a group picks evenly across start time.
func Stratify(pool []Ref, n int) []Ref {
	if len(pool) <= n {
		return pool
	}
	lens := make([]int64, len(pool))
	for i, r := range pool {
		lens[i] = r.HistoryLen
	}
	sort.Slice(lens, func(i, j int) bool { return lens[i] < lens[j] })
	q1, q2, q3 := lens[len(lens)/4], lens[len(lens)/2], lens[3*len(lens)/4]
	quartile := func(l int64) int {
		switch {
		case l <= q1:
			return 0
		case l <= q2:
			return 1
		case l <= q3:
			return 2
		}
		return 3
	}

	type key struct {
		status enumspb.WorkflowExecutionStatus
		q      int
	}
	groups := map[key][]Ref{}
	var keys []key
	for _, r := range pool {
		k := key{r.Status, quartile(r.HistoryLen)}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], r)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].status != keys[j].status {
			return keys[i].status < keys[j].status
		}
		return keys[i].q < keys[j].q
	})

	// Largest-remainder allocation, with a floor of one per group.
	alloc := map[key]int{}
	left := n
	for _, k := range keys {
		alloc[k] = 1
		left--
	}
	if left > 0 {
		type rem struct {
			k key
			r float64
		}
		var rems []rem
		used := 0
		for _, k := range keys {
			exact := float64(left) * float64(len(groups[k])) / float64(len(pool))
			whole := int(exact)
			alloc[k] += whole
			used += whole
			rems = append(rems, rem{k, exact - float64(whole)})
		}
		sort.SliceStable(rems, func(i, j int) bool { return rems[i].r > rems[j].r })
		for i := 0; i < left-used; i++ {
			alloc[rems[i%len(rems)].k]++
		}
	}

	var out []Ref
	for _, k := range keys {
		g := groups[k]
		sort.Slice(g, func(i, j int) bool { return g[i].StartTime.Before(g[j].StartTime) })
		take := min(alloc[k], len(g))
		for i := range take {
			out = append(out, g[i*len(g)/take])
		}
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// Fetch returns an execution's full history, from the cache when the
// execution is closed and was fetched before.
func (r *Reader) Fetch(ctx context.Context, ref Ref) (*historypb.History, error) {
	path := r.cachePath(ref)
	if path != "" && ref.Closed() {
		if b, err := os.ReadFile(path); err == nil {
			h := &historypb.History{}
			if err := proto.Unmarshal(b, h); err == nil {
				return h, nil
			}
		}
	}

	h := &historypb.History{}
	var token []byte
	for {
		if err := r.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		resp, err := r.svc.GetWorkflowExecutionHistory(ctx, &workflowservice.GetWorkflowExecutionHistoryRequest{
			Namespace:     r.namespace,
			Execution:     &commonpb.WorkflowExecution{WorkflowId: ref.WorkflowID, RunId: ref.RunID},
			NextPageToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("history %s/%s: %w", ref.WorkflowID, ref.RunID, err)
		}
		h.Events = append(h.Events, resp.GetHistory().GetEvents()...)
		token = resp.GetNextPageToken()
		if len(token) == 0 {
			break
		}
	}

	if path != "" && ref.Closed() {
		if b, err := proto.Marshal(h); err == nil {
			_ = os.MkdirAll(filepath.Dir(path), 0o700)
			_ = os.WriteFile(path, b, 0o600)
		}
	}
	return h, nil
}

// FetchAll fetches every ref with up to parallel requests in flight. It calls
// progress after each one. Failed fetches are skipped and returned as errors
// alongside the histories that succeeded.
func (r *Reader) FetchAll(ctx context.Context, refs []Ref, parallel int, progress func(done, total int)) (map[Ref]*historypb.History, error) {
	if parallel <= 0 {
		parallel = 4
	}
	out := make(map[Ref]*historypb.History, len(refs))
	var (
		mu   sync.Mutex
		errs []error
		done int
		wg   sync.WaitGroup
	)
	ch := make(chan Ref)
	for range parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ref := range ch {
				h, err := r.Fetch(ctx, ref)
				mu.Lock()
				if err != nil {
					errs = append(errs, err)
				} else {
					out[ref] = h
				}
				done++
				if progress != nil {
					progress(done, len(refs))
				}
				mu.Unlock()
			}
		}()
	}
	for _, ref := range refs {
		select {
		case ch <- ref:
		case <-ctx.Done():
		}
	}
	close(ch)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, errors.Join(errs...)
}

func (r *Reader) cachePath(ref Ref) string {
	if r.cacheDir == "" {
		return ""
	}
	return filepath.Join(r.cacheDir, safe(r.namespace), safe(ref.WorkflowID), safe(ref.RunID)+".pb")
}

// safe makes a string usable as one path element.
func safe(s string) string {
	r := strings.NewReplacer("/", "%2F", "\\", "%5C", ":", "%3A", "..", "%2E%2E")
	return r.Replace(s)
}

func typeQuery(workflowType string, window time.Duration) string {
	return fmt.Sprintf("WorkflowType = '%s' AND StartTime > '%s'",
		strings.ReplaceAll(workflowType, "'", "\\'"),
		time.Now().Add(-window).UTC().Format(time.RFC3339))
}

// Service returns the Namespace's WorkflowService client, for collectors
// that read more than histories, such as Schedules. Calls through it bypass
// the rate limiter, so callers should call Wait first.
func (r *Reader) Service() workflowservice.WorkflowServiceClient { return r.svc }

// Wait blocks until the rate limiter allows one more request.
func (r *Reader) Wait(ctx context.Context) error { return r.limiter.Wait(ctx) }

// RetentionHours reads the Namespace's retention period with
// DescribeNamespace, which isn't a billable Action.
func (r *Reader) RetentionHours(ctx context.Context) (float64, error) {
	if err := r.limiter.Wait(ctx); err != nil {
		return 0, err
	}
	resp, err := r.svc.DescribeNamespace(ctx, &workflowservice.DescribeNamespaceRequest{Namespace: r.namespace})
	if err != nil {
		return 0, fmt.Errorf("describe Namespace: %w", err)
	}
	return resp.GetConfig().GetWorkflowExecutionRetentionTtl().AsDuration().Hours(), nil
}
