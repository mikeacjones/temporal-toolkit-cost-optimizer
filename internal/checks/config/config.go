// Package config holds the Namespace and account configuration checks
// (CFG-*, STO-002). They read the Cloud Ops API and the Namespace's
// Schedules instead of Workflow histories, and run as an analyze.Collector.
//
// Which source each check needs:
//
//	CFG-001 replication on a non-production Namespace   Cloud Ops (usage for the estimate)
//	CFG-003 Fairness on but unused                     Cloud Ops, plus sampled histories
//	CFG-004 Provisioned Capacity above use             Cloud Ops and usage
//	CFG-005 Export with full retention                 Cloud Ops (usage for the estimate)
//	CFG-006 idle Namespace                             usage, or Visibility counts
//	CFG-007 frequent or one-shot Schedules             the Namespace's Schedules
//	CFG-008 Schedule storage                           the Namespace's Schedules and their internal Workflows
//	CFG-010 a Schedule that starts a one-Activity Workflow  Schedules, plus WF-001 findings
//	STO-002 retention longer than the default          Cloud Ops (usage for the estimate)
//
// Without a Cloud Ops source, only the Schedule checks and the
// Visibility-based CFG-006 run, and the collector says so in a caveat.
//
// This package's name matches internal/config; import it as, for example,
// cfgchecks.
package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"gopkg.in/yaml.v3"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/analyze"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/estimate"
	kbdata "github.com/mikeacjones/temporal-toolkit-cost-optimizer/kb"
)

// Options tune the checks. Zero values take the defaults.
type Options struct {
	// Cloud reads Namespace settings and usage. Nil skips the checks that
	// need it.
	Cloud CloudSource
	// AccountWide also runs CFG-001 and CFG-006 over every Namespace the
	// caller can see, not only the one being analyzed.
	AccountWide bool
	// UsageDays is how many whole days of usage to read; default 7.
	UsageDays int
	// IdleDays is how long a Namespace must go without Actions to be idle
	// (CFG-006); default 14.
	IdleDays int
	// FrequentInterval flags Schedules that run at least this often
	// (CFG-007); default 5 minutes.
	FrequentInterval time.Duration
	// MaxSchedules caps how many Schedules are described; default 200.
	MaxSchedules int
	// LargeSchedulerHistory flags a Schedule whose internal Workflow
	// history is at least this many bytes (CFG-008); default 1 MiB.
	LargeSchedulerHistory int64
	// LargeSpecEntries flags a Schedule spec with at least this many
	// calendar, cron, or interval entries (CFG-008); default 20.
	LargeSpecEntries int
	// FairnessSamples is how many histories per Workflow Type CFG-003
	// reads; default 20.
	FairnessSamples int
	// DefaultRetentionDays is the retention STO-002 compares with; default
	// 30, the Temporal Cloud default.
	DefaultRetentionDays int
	// ExportRetentionDays is the shorter retention CFG-005 suggests;
	// default 7.
	ExportRetentionDays int
	// Now is when the checks run; default time.Now.
	Now func() time.Time
}

func (o *Options) defaults() {
	if o.UsageDays <= 0 {
		o.UsageDays = 7
	}
	if o.IdleDays <= 0 {
		o.IdleDays = 14
	}
	if o.FrequentInterval <= 0 {
		o.FrequentInterval = 5 * time.Minute
	}
	if o.MaxSchedules <= 0 {
		o.MaxSchedules = 200
	}
	if o.LargeSchedulerHistory <= 0 {
		o.LargeSchedulerHistory = 1 << 20
	}
	if o.LargeSpecEntries <= 0 {
		o.LargeSpecEntries = 20
	}
	if o.FairnessSamples <= 0 {
		o.FairnessSamples = 20
	}
	if o.DefaultRetentionDays <= 0 {
		o.DefaultRetentionDays = 30
	}
	if o.ExportRetentionDays <= 0 {
		o.ExportRetentionDays = 7
	}
	if o.Now == nil {
		o.Now = time.Now
	}
}

// Collector runs the configuration checks.
type Collector struct{ opts Options }

// New returns the configuration checks as an analyze.Collector.
func New(opts Options) *Collector {
	opts.defaults()
	return &Collector{opts: opts}
}

// Name implements analyze.Collector.
func (*Collector) Name() string { return "Namespace config" }

// hoursPerMonth turns daily rates into a 30-day month.
const hoursPerMonth = 30 * 24

// Collect implements analyze.Collector. A check whose source fails is
// skipped; the errors come back joined so the analysis can list them as
// caveats, alongside whatever the other checks found.
func (c *Collector) Collect(ctx context.Context, env *analyze.Env) ([]estimate.Finding, error) {
	r := &run{opts: c.opts, env: env, now: c.opts.Now()}
	if err := r.loadMultipliers(); err != nil {
		return nil, err
	}
	var errs []error
	note := func(what string, err error) {
		if err != nil && ctx.Err() == nil {
			errs = append(errs, fmt.Errorf("%s: %w", what, err))
		}
	}

	if c.opts.Cloud != nil {
		note("Namespace settings", r.loadCloud(ctx))
	} else {
		errs = append(errs, errors.New("no Temporal Cloud login or API key, so CFG-001, CFG-003, CFG-004, CFG-005, and STO-002 didn't run"))
	}
	if r.ns != nil {
		r.checkReplication(r.ns, r.usage[r.ns.Name], true)
		note("CFG-003", r.checkFairness(ctx))
		r.checkCapacity()
		r.checkRetention()
	}
	if c.opts.AccountWide && r.all != nil {
		for _, ns := range r.all {
			if ns.Name != env.Reader.Namespace() {
				r.checkReplication(ns, r.usage[ns.Name], false)
			}
		}
	}
	note("CFG-006", r.checkIdle(ctx))
	note("Schedules", r.checkSchedules(ctx))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return r.findings, errors.Join(errs...)
}

// run holds one Collect call's state.
type run struct {
	opts Options
	env  *analyze.Env
	now  time.Time

	ns        *NamespaceInfo
	all       []*NamespaceInfo
	sinks     []ExportSink
	usage     map[string][]DayUsage
	usageDays int

	replication float64 // Actions and storage multiplier for a replica
	fairness    float64 // Actions multiplier while Fairness is on

	findings []estimate.Finding
}

func (r *run) loadCloud(ctx context.Context) error {
	name := r.env.Reader.Namespace()
	ns, err := r.opts.Cloud.Namespace(ctx, name)
	if err != nil {
		return err
	}
	r.ns = ns
	var errs []error
	if sinks, err := r.opts.Cloud.ExportSinks(ctx, name); err != nil {
		errs = append(errs, err)
	} else {
		r.sinks = sinks
	}
	end := r.now.UTC().Truncate(24 * time.Hour)
	start := end.Add(-time.Duration(r.opts.UsageDays) * 24 * time.Hour)
	if u, err := r.opts.Cloud.Usage(ctx, start, end); err != nil {
		errs = append(errs, fmt.Errorf("usage isn't readable, so estimates that need it are missing: %w", err))
	} else {
		r.usage = u
		days := map[time.Time]bool{}
		for _, du := range u {
			for _, d := range du {
				days[d.Day] = true
			}
		}
		r.usageDays = len(days)
	}
	if r.opts.AccountWide {
		if all, err := r.opts.Cloud.AllNamespaces(ctx); err != nil {
			errs = append(errs, err)
		} else {
			r.all = all
		}
	}
	return errors.Join(errs...)
}

// loadMultipliers reads the Namespace multipliers from the embedded billing
// rules, which billing.Rules doesn't expose.
func (r *run) loadMultipliers() error {
	b, err := fs.ReadFile(kbdata.FS, "billing/actions.yaml")
	if err != nil {
		return err
	}
	var doc struct {
		Multipliers []struct {
			Setting string  `yaml:"setting"`
			Actions float64 `yaml:"actions"`
		} `yaml:"namespace_multipliers"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return err
	}
	for _, m := range doc.Multipliers {
		switch m.Setting {
		case "replication":
			r.replication = m.Actions
		case "fairness":
			r.fairness = m.Actions
		}
	}
	if r.replication <= 1 || r.fairness <= 1 {
		return errors.New("billing rules are missing the replication or fairness multiplier")
	}
	return nil
}

// notInHistory returns the Actions an operation costs from the billing
// rules' not_in_history list, or 0 when it isn't listed.
func (r *run) notInHistory(op string) float64 {
	for _, n := range r.env.Rules.NotInHistory {
		if n.Operation == op {
			return n.Actions
		}
	}
	return 0
}

// eventActions returns the Actions one history event costs.
func (r *run) eventActions(et enumspb.EventType) float64 {
	rule, _ := r.env.Rules.Rule(et)
	return rule.Actions
}

// monthly is a Namespace's usage scaled to a 30-day month.
type monthly struct {
	Actions, ActiveGBHours, RetainedGBHours float64
	known                                   bool
}

// monthlyUsage scales a Namespace's usage to a month. Days with no record
// count as zero.
func (r *run) monthlyUsage(days []DayUsage) monthly {
	if r.usage == nil || r.usageDays == 0 {
		return monthly{}
	}
	var m monthly
	for _, d := range days {
		m.Actions += d.Actions
		m.ActiveGBHours += byteSecondsToGBHours(d.ActiveByteSeconds)
		m.RetainedGBHours += byteSecondsToGBHours(d.RetainedByteSeconds)
	}
	scale := 30 / float64(r.usageDays)
	m.Actions *= scale
	m.ActiveGBHours *= scale
	m.RetainedGBHours *= scale
	m.known = true
	return m
}

// historyMonthlyActions sums what the analyzed Workflow Types use a month,
// from their histories: a lower bound for the whole Namespace.
func (r *run) historyMonthlyActions() float64 {
	var t float64
	for _, ty := range r.env.Types {
		if ty.MonthlyActions > 0 {
			t += ty.MonthlyActions
		}
	}
	return t
}

func byteSecondsToGBHours(bs float64) float64 { return bs / 1e9 / 3600 }

func (r *run) add(f estimate.Finding) {
	f.Source = estimate.SourceConfig
	if f.Guidance == "" {
		f.Guidance = f.ID
	}
	if f.Title == "" {
		f.Title = r.env.Titles[f.ID]
	}
	if p := f.ProjectedMonthly; p != nil {
		p.Actions = math.Round(p.Actions)
		p.ActiveGBHours = round3(p.ActiveGBHours)
		p.RetainedGBHours = round3(p.RetainedGBHours)
		if p.Actions == 0 && p.ActiveGBHours == 0 && p.RetainedGBHours == 0 {
			f.ProjectedMonthly = nil
		}
	}
	r.findings = append(r.findings, f)
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }

// nonProduction reports whether a Namespace's name or tags mark it as
// development, test, or staging, and returns the word that did.
func nonProduction(ns *NamespaceInfo) (string, bool) {
	words := []string{"dev", "develop", "development", "test", "testing", "qa", "staging", "stage", "stg", "sandbox", "demo", "poc"}
	isWord := func(s string) bool {
		for _, w := range words {
			if s == w {
				return true
			}
		}
		return false
	}
	// The Namespace name, without the account suffix.
	name := strings.ToLower(strings.SplitN(ns.Name, ".", 2)[0])
	for _, tok := range strings.FieldsFunc(name, func(r rune) bool { return !(r >= 'a' && r <= 'z') }) {
		if isWord(tok) {
			return "name " + ns.Name, true
		}
	}
	for k, v := range ns.Tags {
		k, v = strings.ToLower(k), strings.ToLower(v)
		if (k == "env" || k == "environment" || k == "stage" || k == "tier") && isWord(v) {
			return fmt.Sprintf("tag %s=%s", k, v), true
		}
		if isWord(k) {
			return "tag " + k, true
		}
	}
	return "", false
}
