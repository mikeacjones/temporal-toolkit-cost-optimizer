package config

import (
	"context"
	"time"

	namespacev1 "go.temporal.io/cloud-sdk/api/namespace/v1"
	usagev1 "go.temporal.io/cloud-sdk/api/usage/v1"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/cloudauth"
)

// NamespaceInfo is what the checks read about a Namespace from the Cloud
// Ops API.
type NamespaceInfo struct {
	Name          string
	RetentionDays int
	// Replicas counts replicas beyond the primary.
	Replicas        int
	ReplicaRegions  []string
	Tags            map[string]string
	FairnessEnabled bool
	// TRUs is the provisioned capacity, or 0 in on-demand mode.
	TRUs    float64
	Active  bool
	Created time.Time
}

// ExportSink is one Workflow History Export destination.
type ExportSink struct {
	Name    string
	Enabled bool
}

// DayUsage is one Namespace's usage on one day, from the Usage API.
type DayUsage struct {
	Day     time.Time
	Actions float64
	// ActiveByteSeconds and RetainedByteSeconds are storage, as the API
	// reports it.
	ActiveByteSeconds   float64
	RetainedByteSeconds float64
}

// CloudSource reads account and Namespace settings. Implementations only
// read.
type CloudSource interface {
	Namespace(ctx context.Context, name string) (*NamespaceInfo, error)
	// AllNamespaces lists every Namespace the caller can see, for checks
	// that run across the account.
	AllNamespaces(ctx context.Context) ([]*NamespaceInfo, error)
	ExportSinks(ctx context.Context, namespace string) ([]ExportSink, error)
	// Usage returns daily usage per Namespace, keyed by Namespace name, for
	// whole days in [start, end).
	Usage(ctx context.Context, start, end time.Time) (map[string][]DayUsage, error)
}

// OpsSource adapts a Cloud Ops API reader, from a browser login or an API
// key, to CloudSource.
type OpsSource struct{ Ops *cloudauth.Ops }

// Namespace implements CloudSource.
func (s OpsSource) Namespace(ctx context.Context, name string) (*NamespaceInfo, error) {
	ns, err := s.Ops.Namespace(ctx, name)
	if err != nil {
		return nil, err
	}
	return namespaceInfo(ns), nil
}

// AllNamespaces implements CloudSource.
func (s OpsSource) AllNamespaces(ctx context.Context) ([]*NamespaceInfo, error) {
	all, err := s.Ops.AllNamespaces(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*NamespaceInfo, 0, len(all))
	for _, ns := range all {
		out = append(out, namespaceInfo(ns))
	}
	return out, nil
}

// ExportSinks implements CloudSource.
func (s OpsSource) ExportSinks(ctx context.Context, namespace string) ([]ExportSink, error) {
	sinks, err := s.Ops.ExportSinks(ctx, namespace)
	if err != nil {
		return nil, err
	}
	out := make([]ExportSink, 0, len(sinks))
	for _, sk := range sinks {
		out = append(out, ExportSink{Name: sk.GetName(), Enabled: sk.GetSpec().GetEnabled()})
	}
	return out, nil
}

// Usage implements CloudSource.
func (s OpsSource) Usage(ctx context.Context, start, end time.Time) (map[string][]DayUsage, error) {
	sums, err := s.Ops.Usage(ctx, start, end)
	if err != nil {
		return nil, err
	}
	return usageByNamespace(sums), nil
}

func namespaceInfo(ns *namespacev1.Namespace) *NamespaceInfo {
	spec := ns.GetSpec()
	info := &NamespaceInfo{
		Name:            ns.GetNamespace(),
		RetentionDays:   int(spec.GetRetentionDays()),
		Tags:            ns.GetTags(),
		FairnessEnabled: spec.GetFairness().GetTaskQueueFairnessEnabled(),
		TRUs:            ns.GetCapacity().GetProvisioned().GetCurrentValue(),
		Active:          ns.GetState().String() == "RESOURCE_STATE_ACTIVE",
		Created:         ns.GetCreatedTime().AsTime(),
	}
	// Replicas lists the primary too. Older Namespaces describe a
	// multi-region setup only through spec regions.
	if rs := ns.GetReplicas(); len(rs) > 0 {
		for _, r := range rs {
			if !r.GetIsPrimary() {
				info.Replicas++
				info.ReplicaRegions = append(info.ReplicaRegions, r.GetRegion())
			}
		}
	} else if n := max(len(spec.GetReplicas()), len(spec.GetRegions())) - 1; n > 0 {
		info.Replicas = n
		for _, r := range spec.GetReplicas()[min(1, len(spec.GetReplicas())):] {
			info.ReplicaRegions = append(info.ReplicaRegions, r.GetRegion())
		}
		if len(info.ReplicaRegions) == 0 && len(spec.GetRegions()) > 1 {
			info.ReplicaRegions = spec.GetRegions()[1:]
		}
	}
	return info
}

// usageByNamespace sums each day's records per Namespace. A day can carry
// several record groups for one Namespace.
func usageByNamespace(sums []*usagev1.Summary) map[string][]DayUsage {
	out := map[string][]DayUsage{}
	for _, sm := range sums {
		if sm.GetIncomplete() {
			continue
		}
		day := sm.GetStartTime().AsTime()
		perNS := map[string]*DayUsage{}
		for _, g := range sm.GetRecordGroups() {
			ns := ""
			for _, gb := range g.GetGroupBys() {
				if gb.GetKey() == usagev1.GroupByKey_GROUP_BY_KEY_NAMESPACE {
					ns = gb.GetValue()
				}
			}
			if ns == "" {
				continue
			}
			d := perNS[ns]
			if d == nil {
				d = &DayUsage{Day: day}
				perNS[ns] = d
			}
			for _, rec := range g.GetRecords() {
				switch rec.GetType() {
				case usagev1.RecordType_RECORD_TYPE_ACTIONS:
					d.Actions += rec.GetValue()
				case usagev1.RecordType_RECORD_TYPE_ACTIVE_STORAGE:
					d.ActiveByteSeconds += rec.GetValue()
				case usagev1.RecordType_RECORD_TYPE_RETAINED_STORAGE:
					d.RetainedByteSeconds += rec.GetValue()
				}
			}
		}
		for ns, d := range perNS {
			out[ns] = append(out[ns], *d)
		}
	}
	return out
}
