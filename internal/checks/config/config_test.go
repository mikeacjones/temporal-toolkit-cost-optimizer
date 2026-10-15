package config

import (
	"context"
	"strings"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	schedulepb "go.temporal.io/api/schedule/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/analyze"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/billing"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/estimate"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/history"
)

var now = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// fakeCloud is a CloudSource with fixed answers.
type fakeCloud struct {
	ns    map[string]*NamespaceInfo
	sinks []ExportSink
	usage map[string][]DayUsage
	noUse bool
}

func (f *fakeCloud) Namespace(_ context.Context, name string) (*NamespaceInfo, error) {
	return f.ns[name], nil
}

func (f *fakeCloud) AllNamespaces(context.Context) ([]*NamespaceInfo, error) {
	var out []*NamespaceInfo
	for _, n := range f.ns {
		out = append(out, n)
	}
	return out, nil
}

func (f *fakeCloud) ExportSinks(context.Context, string) ([]ExportSink, error) { return f.sinks, nil }

func (f *fakeCloud) Usage(context.Context, time.Time, time.Time) (map[string][]DayUsage, error) {
	if f.noUse {
		return nil, errPermission
	}
	return f.usage, nil
}

type permErr struct{}

func (permErr) Error() string { return "PermissionDenied: request unauthorized" }

var errPermission = permErr{}

type notFound struct{}

func (notFound) Error() string { return "NotFound" }

var errNotFound = notFound{}

// days returns n days of identical usage.
func days(n int, actions, activeGBHours, retainedGBHours float64) []DayUsage {
	out := make([]DayUsage, n)
	for i := range out {
		out[i] = DayUsage{
			Day: now.Truncate(24*time.Hour).AddDate(0, 0, -i-1), Actions: actions,
			ActiveByteSeconds: activeGBHours * 1e9 * 3600, RetainedByteSeconds: retainedGBHours * 1e9 * 3600,
		}
	}
	return out
}

// fakeService answers the data-plane calls the checks make.
type fakeService struct {
	workflowservice.WorkflowServiceClient
	schedules  []*schedulepb.ScheduleListEntry
	describe   map[string]*workflowservice.DescribeScheduleResponse
	schedulers []*workflowpb.WorkflowExecutionInfo
	executions []*workflowpb.WorkflowExecutionInfo
	histories  map[string]*historypb.History
	counts     map[string]int64
}

func (f *fakeService) ListSchedules(context.Context, *workflowservice.ListSchedulesRequest, ...grpc.CallOption) (*workflowservice.ListSchedulesResponse, error) {
	return &workflowservice.ListSchedulesResponse{Schedules: f.schedules}, nil
}

func (f *fakeService) DescribeSchedule(_ context.Context, in *workflowservice.DescribeScheduleRequest, _ ...grpc.CallOption) (*workflowservice.DescribeScheduleResponse, error) {
	return f.describe[in.GetScheduleId()], nil
}

func (f *fakeService) ListWorkflowExecutions(_ context.Context, in *workflowservice.ListWorkflowExecutionsRequest, _ ...grpc.CallOption) (*workflowservice.ListWorkflowExecutionsResponse, error) {
	return &workflowservice.ListWorkflowExecutionsResponse{Executions: f.executions}, nil
}

func (f *fakeService) DescribeWorkflowExecution(_ context.Context, in *workflowservice.DescribeWorkflowExecutionRequest, _ ...grpc.CallOption) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	for _, s := range f.schedulers {
		if s.GetExecution().GetWorkflowId() == in.GetExecution().GetWorkflowId() {
			return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: s}, nil
		}
	}
	return nil, errNotFound
}

func (f *fakeService) GetWorkflowExecutionHistory(_ context.Context, in *workflowservice.GetWorkflowExecutionHistoryRequest, _ ...grpc.CallOption) (*workflowservice.GetWorkflowExecutionHistoryResponse, error) {
	return &workflowservice.GetWorkflowExecutionHistoryResponse{History: f.histories[in.GetExecution().GetWorkflowId()]}, nil
}

func (f *fakeService) CountWorkflowExecutions(_ context.Context, in *workflowservice.CountWorkflowExecutionsRequest, _ ...grpc.CallOption) (*workflowservice.CountWorkflowExecutionsResponse, error) {
	key := "all"
	if in.GetQuery() != "" {
		key = "recent"
	}
	return &workflowservice.CountWorkflowExecutionsResponse{Count: f.counts[key]}, nil
}

func env(t *testing.T, svc *fakeService, ns string, findings ...estimate.Finding) *analyze.Env {
	t.Helper()
	rules, err := billing.LoadRules()
	if err != nil {
		t.Fatal(err)
	}
	return &analyze.Env{
		Reader:   history.NewReader(svc, ns, 1000, ""),
		Rules:    rules,
		Window:   7 * 24 * time.Hour,
		Findings: findings,
		Types:    []analyze.TypeSummary{{WorkflowType: "OrderWorkflow", MonthlyActions: 1000}},
	}
}

func collect(t *testing.T, opts Options, e *analyze.Env) ([]estimate.Finding, error) {
	t.Helper()
	opts.Now = func() time.Time { return now }
	return New(opts).Collect(context.Background(), e)
}

func byID(fs []estimate.Finding, id string) []estimate.Finding {
	var out []estimate.Finding
	for _, f := range fs {
		if f.ID == id {
			out = append(out, f)
		}
	}
	return out
}

func near(a, b float64) bool { d := a - b; return d < 0.01 && d > -0.01 }

func TestReplicationOnNonProduction(t *testing.T) {
	cloud := &fakeCloud{
		ns: map[string]*NamespaceInfo{
			"orders-dev.acct":  {Name: "orders-dev.acct", RetentionDays: 7, Replicas: 1, ReplicaRegions: []string{"aws-us-west-2"}},
			"orders-prod.acct": {Name: "orders-prod.acct", RetentionDays: 7, Replicas: 1},
		},
		// 1,000 Actions and 1 GB-hour of each storage a day, billed at 2x.
		usage: map[string][]DayUsage{"orders-dev.acct": days(7, 1000, 1, 1)},
	}
	fs, _ := collect(t, Options{Cloud: cloud}, env(t, &fakeService{}, "orders-dev.acct"))
	got := byID(fs, "CFG-001")
	if len(got) != 1 {
		t.Fatalf("got %d CFG-001 findings, want 1", len(got))
	}
	p := got[0].ProjectedMonthly
	if p.Actions != 15000 || !near(p.ActiveGBHours, 15) || !near(p.RetainedGBHours, 15) {
		t.Errorf("projection %+v, want half of 30,000 Actions and 30 GB-hours", p)
	}

	fs, _ = collect(t, Options{Cloud: cloud}, env(t, &fakeService{}, "orders-prod.acct"))
	if len(byID(fs, "CFG-001")) != 0 {
		t.Error("a production Namespace shouldn't be flagged")
	}
}

func TestNonProductionNames(t *testing.T) {
	cases := map[string]bool{
		"payments-staging.a1b2c": true, "dev.a1b2c": true, "qa_orders.a1b2c": true,
		"payments.a1b2c": false, "devices-prod.a1b2c": false, "tmprl-dem-cld.a1b2c": false,
	}
	for name, want := range cases {
		if _, got := nonProduction(&NamespaceInfo{Name: name}); got != want {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
	if _, got := nonProduction(&NamespaceInfo{Name: "orders.a1b2c", Tags: map[string]string{"env": "test"}}); !got {
		t.Error("an env=test tag should mark the Namespace non-production")
	}
}

func startedWithFairness(key string) *historypb.History {
	return &historypb.History{Events: []*historypb.HistoryEvent{{
		EventId: 1, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{
			WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
				Priority: &commonpb.Priority{FairnessKey: key},
			},
		},
	}}}
}

func TestFairnessUnused(t *testing.T) {
	cloud := &fakeCloud{
		ns:    map[string]*NamespaceInfo{"ns": {Name: "ns", RetentionDays: 7, FairnessEnabled: true}},
		usage: map[string][]DayUsage{"ns": days(7, 1100, 0, 0)},
	}
	svc := &fakeService{
		executions: []*workflowpb.WorkflowExecutionInfo{{
			Execution: &commonpb.WorkflowExecution{WorkflowId: "a", RunId: "r"},
			Type:      &commonpb.WorkflowType{Name: "OrderWorkflow"},
			Status:    enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED,
		}},
		histories: map[string]*historypb.History{"a": startedWithFairness("")},
	}
	fs, err := collect(t, Options{Cloud: cloud}, env(t, svc, "ns"))
	if err != nil {
		t.Fatal(err)
	}
	got := byID(fs, "CFG-003")
	if len(got) != 1 {
		t.Fatalf("got %d CFG-003 findings, want 1", len(got))
	}
	// 33,000 billed Actions a month include a 0.1 surcharge: 3,000 of them.
	if got[0].ProjectedMonthly.Actions != 3000 {
		t.Errorf("saved %v Actions, want 3,000", got[0].ProjectedMonthly.Actions)
	}

	svc.histories["a"] = startedWithFairness("tenant-7")
	fs, _ = collect(t, Options{Cloud: cloud}, env(t, svc, "ns"))
	if len(byID(fs, "CFG-003")) != 0 {
		t.Error("a history with a fairness key means Fairness is in use")
	}
}

func TestProvisionedCapacityAtFloor(t *testing.T) {
	floor := 2 * 360_000.0 * 24 // 3 TRUs, at the billing rules' floor
	cloud := &fakeCloud{
		ns:    map[string]*NamespaceInfo{"ns": {Name: "ns", RetentionDays: 7, TRUs: 3}},
		usage: map[string][]DayUsage{"ns": days(7, floor, 0, 0)},
	}
	fs, _ := collect(t, Options{Cloud: cloud}, env(t, &fakeService{}, "ns"))
	if len(byID(fs, "CFG-004")) != 1 {
		t.Error("usage at the TRU floor every day should be flagged")
	}
	cloud.usage["ns"] = days(7, floor*3, 0, 0)
	fs, _ = collect(t, Options{Cloud: cloud}, env(t, &fakeService{}, "ns"))
	if len(byID(fs, "CFG-004")) != 0 {
		t.Error("usage well above the floor shouldn't be flagged")
	}
}

func TestRetention(t *testing.T) {
	cloud := &fakeCloud{
		ns:    map[string]*NamespaceInfo{"ns": {Name: "ns", RetentionDays: 30}},
		sinks: []ExportSink{{Name: "s3-archive", Enabled: true}},
		usage: map[string][]DayUsage{"ns": days(7, 1, 0, 10)}, // 300 retained GB-hours a month
	}
	fs, _ := collect(t, Options{Cloud: cloud}, env(t, &fakeService{}, "ns"))
	got := byID(fs, "CFG-005")
	if len(got) != 1 || !near(got[0].ProjectedMonthly.RetainedGBHours, 300*(1-7.0/30)) {
		t.Fatalf("CFG-005: %+v", got)
	}
	if len(byID(fs, "STO-002")) != 0 {
		t.Error("STO-002 shouldn't repeat CFG-005")
	}

	cloud.sinks = nil
	cloud.ns["ns"].RetentionDays = 90
	fs, _ = collect(t, Options{Cloud: cloud}, env(t, &fakeService{}, "ns"))
	got = byID(fs, "STO-002")
	if len(got) != 1 || !near(got[0].ProjectedMonthly.RetainedGBHours, 300*(1-30.0/90)) {
		t.Fatalf("STO-002: %+v", got)
	}

	cloud.ns["ns"].RetentionDays = 30
	fs, _ = collect(t, Options{Cloud: cloud}, env(t, &fakeService{}, "ns"))
	if len(byID(fs, "STO-002")) != 0 {
		t.Error("default retention shouldn't be flagged")
	}
}

func TestIdleNamespace(t *testing.T) {
	cloud := &fakeCloud{
		ns: map[string]*NamespaceInfo{"ns": {Name: "ns", RetentionDays: 7}, "old.acct": {Name: "old.acct"}},
		usage: map[string][]DayUsage{
			"ns":       days(14, 50, 1, 1),
			"old.acct": days(14, 0, 0.5, 2),
		},
	}
	fs, _ := collect(t, Options{Cloud: cloud, UsageDays: 14, AccountWide: true}, env(t, &fakeService{}, "ns"))
	got := byID(fs, "CFG-006")
	if len(got) != 1 || got[0].Evidence[0].Setting != "namespace/old.acct" {
		t.Fatalf("CFG-006: %+v", got)
	}
	if !near(got[0].ProjectedMonthly.ActiveGBHours, 15) || !near(got[0].ProjectedMonthly.RetainedGBHours, 60) {
		t.Errorf("projection %+v", got[0].ProjectedMonthly)
	}

	// Without usage, Visibility counts decide.
	svc := &fakeService{counts: map[string]int64{"recent": 0, "all": 12}}
	fs, err := collect(t, Options{}, env(t, svc, "ns"))
	if len(byID(fs, "CFG-006")) != 1 {
		t.Errorf("no starts and stored executions should be idle; err %v", err)
	}
}

func scheduleEntry(id, wt string, spec *schedulepb.ScheduleSpec, future ...time.Time) *schedulepb.ScheduleListEntry {
	info := &schedulepb.ScheduleListInfo{Spec: spec, WorkflowType: &commonpb.WorkflowType{Name: wt}}
	for _, f := range future {
		info.FutureActionTimes = append(info.FutureActionTimes, timestamppb.New(f))
	}
	return &schedulepb.ScheduleListEntry{ScheduleId: id, Info: info}
}

func describeResp(state *schedulepb.ScheduleState, created time.Time) *workflowservice.DescribeScheduleResponse {
	return &workflowservice.DescribeScheduleResponse{
		Schedule: &schedulepb.Schedule{State: state},
		Info:     &schedulepb.ScheduleInfo{CreateTime: timestamppb.New(created)},
	}
}

func TestSchedules(t *testing.T) {
	every := func(d time.Duration) *schedulepb.ScheduleSpec {
		return &schedulepb.ScheduleSpec{Interval: []*schedulepb.IntervalSpec{{Interval: durationpb.New(d)}}}
	}
	svc := &fakeService{
		schedules: []*schedulepb.ScheduleListEntry{
			scheduleEntry("poll", "PollWorkflow", every(time.Minute), now.Add(time.Minute), now.Add(2*time.Minute)),
			scheduleEntry("daily", "ReportWorkflow", every(24*time.Hour), now.Add(time.Hour)),
			scheduleEntry("remind-123", "ReminderWorkflow", &schedulepb.ScheduleSpec{}, now.Add(time.Hour)),
			scheduleEntry("old", "ReportWorkflow", every(time.Hour)),
			scheduleEntry("ping", "PingWorkflow", every(time.Hour), now.Add(time.Hour), now.Add(2*time.Hour)),
		},
		describe: map[string]*workflowservice.DescribeScheduleResponse{
			"poll":       describeResp(&schedulepb.ScheduleState{}, now.AddDate(0, -1, 0)),
			"daily":      describeResp(&schedulepb.ScheduleState{}, now.AddDate(0, -1, 0)),
			"remind-123": describeResp(&schedulepb.ScheduleState{LimitedActions: true, RemainingActions: 1}, now.Add(-time.Hour)),
			"old":        describeResp(&schedulepb.ScheduleState{Paused: true}, now.AddDate(-1, 0, 0)),
			"ping":       describeResp(&schedulepb.ScheduleState{}, now.AddDate(0, -1, 0)),
		},
		schedulers: []*workflowpb.WorkflowExecutionInfo{{
			Execution:        &commonpb.WorkflowExecution{WorkflowId: schedulerWorkflowPrefix + "old"},
			HistorySizeBytes: 2 << 20,
		}},
	}
	wf001 := estimate.Finding{ID: "WF-001", WorkflowType: "PingWorkflow", ActionsSavedPerExecution: estimate.Percentiles{P50: 1}}
	fs, err := collect(t, Options{}, env(t, svc, "ns", wf001))
	if err == nil || !strings.Contains(err.Error(), "no Temporal Cloud login") {
		t.Errorf("without a Cloud source the error should say what didn't run, got %v", err)
	}

	freq := byID(fs, "CFG-007")
	var poll, remind *estimate.Finding
	for i := range freq {
		switch freq[i].WorkflowType {
		case "PollWorkflow":
			poll = &freq[i]
		case "ReminderWorkflow":
			remind = &freq[i]
		}
	}
	// 43,200 runs a month, each 3 Actions as a Schedule or 1 as a Timer.
	if poll == nil || poll.ProjectedMonthly.Actions != 86400 {
		t.Errorf("frequent Schedule: %+v", poll)
	}
	// One one-shot Schedule created in a 7-day window: 30/7 a month at 2
	// Actions saved each.
	if remind == nil || !near(remind.ProjectedMonthly.Actions, 9) {
		t.Errorf("one-shot Schedule: %+v", remind)
	}
	if len(freq) != 2 {
		t.Errorf("got %d CFG-007 findings, want 2 (the daily Schedule isn't frequent)", len(freq))
	}

	st := byID(fs, "CFG-008")
	if len(st) != 1 || st[0].ProjectedMonthly == nil || !near(st[0].ProjectedMonthly.ActiveGBHours, float64(2<<20)/1e9*720) {
		t.Errorf("CFG-008: %+v", st)
	}

	sa := byID(fs, "CFG-010")
	if len(sa) != 1 || sa[0].WorkflowType != "PingWorkflow" || sa[0].ProjectedMonthly.Actions != 720 {
		t.Errorf("CFG-010: %+v", sa)
	}
	for _, f := range fs {
		if f.Source != estimate.SourceConfig || len(f.Evidence) == 0 || f.Evidence[0].Setting == "" {
			t.Errorf("%s: every finding needs the config source and a setting as evidence", f.ID)
		}
	}
}

func TestUsageUnreadable(t *testing.T) {
	cloud := &fakeCloud{
		ns:    map[string]*NamespaceInfo{"orders-dev.acct": {Name: "orders-dev.acct", RetentionDays: 7, Replicas: 1}},
		noUse: true,
	}
	fs, err := collect(t, Options{Cloud: cloud}, env(t, &fakeService{}, "orders-dev.acct"))
	if err == nil || !strings.Contains(err.Error(), "usage isn't readable") {
		t.Errorf("expected a caveat about usage, got %v", err)
	}
	got := byID(fs, "CFG-001")
	// Falls back to the histories: 1,000 Actions a month, billed again.
	if len(got) != 1 || got[0].ProjectedMonthly.Actions != 1000 {
		t.Errorf("CFG-001 without usage: %+v", got)
	}
}
