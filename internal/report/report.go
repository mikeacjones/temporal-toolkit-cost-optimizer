// Package report writes an analysis as JSON or Markdown. Reports include
// Event IDs and types, never payloads.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/analyze"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/estimate"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/kb"
)

// Formats lists the supported output formats.
var Formats = []string{"md", "json"}

// Write renders the report in the given format.
func Write(w io.Writer, rep *analyze.Report, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	case "md", "markdown":
		return writeMarkdown(w, rep)
	}
	return fmt.Errorf("unknown format %q; use one of %s", format, strings.Join(Formats, ", "))
}

func writeMarkdown(w io.Writer, rep *analyze.Report) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Temporal cost review: %s\n\n", rep.Namespace)
	fmt.Fprintf(&b, "Profile `%s`, executions started in the last %s days, generated %s. ",
		rep.Profile, trimFloat(rep.WindowDays), rep.GeneratedAt.Format("2006-01-02 15:04 MST"))
	fmt.Fprintf(&b, "Billing rules snapshot %s.\n\n", rep.BillingSnapshot)

	if len(rep.Findings) == 0 {
		b.WriteString("No findings in the sampled executions.\n\n")
	} else {
		fmt.Fprintf(&b, "Estimated savings: %s Actions a month across %d findings, of about %s Actions a month the analyzed types use today.\n\n",
			actions(rep.ActionsSavedPerMonth), len(rep.Findings), actions(rep.MonthlyActions))
	}

	b.WriteString("## Workflow Types analyzed\n\n")
	b.WriteString("| Workflow Type | Executions in window | Executions per month (est.) | Sampled | Actions per run (p50, mean) | Actions per month (est.) | Savings per month (est.) |\n|---|---:|---:|---:|---:|---:|---:|\n")
	for _, t := range rep.Types {
		savings := "no findings"
		if t.ActionsSavedPerMonth > 0 {
			savings = actions(t.ActionsSavedPerMonth)
		}
		monthly := "unknown"
		if t.MonthlyActions >= 0 {
			monthly = actions(t.MonthlyActions)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %s, %s | %s | %s |\n", t.WorkflowType, count(t.ExecutionsInWindow),
			count(t.MonthlyExecutions), t.Sampled, actions(t.ActionsPerExecution.P50), actions(t.ActionsPerExecution.Mean), monthly, savings)
	}
	b.WriteString("\n")

	if len(rep.Findings) > 0 {
		b.WriteString("## Findings\n\n")
	}
	for _, f := range rep.Findings {
		writeFinding(&b, f)
	}

	b.WriteString("## What these estimates leave out\n\n")
	for _, c := range rep.Caveats {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeFinding(b *strings.Builder, f estimate.Finding) {
	title := f.Title
	if title == "" {
		title = f.ID
	}
	if f.WorkflowType != "" {
		fmt.Fprintf(b, "### %s: %s in %s\n\n", f.ID, title, f.WorkflowType)
	} else {
		fmt.Fprintf(b, "### %s: %s\n\n", f.ID, title)
	}
	fmt.Fprintf(b, "Confidence %s, from %s.", strings.ToUpper(string(f.Confidence)), sourceName(f.Source))
	if p := f.Prevalence; p != nil {
		fmt.Fprintf(b, " Matched %d of %d sampled executions (95%% interval %.0f%% to %.0f%%).",
			p.Matched, p.Sampled, p.Low*100, p.High*100)
		fmt.Fprintf(b, " Actions saved per matching execution: p50 %s, p95 %s.",
			trimFloat(f.ActionsSavedPerExecution.P50), trimFloat(f.ActionsSavedPerExecution.P95))
	}
	if p := f.ProjectedMonthly; p != nil {
		if p.Actions > 0 {
			fmt.Fprintf(b, " Estimated %s Actions a month", actions(p.Actions))
			if p.Executions > 0 {
				fmt.Fprintf(b, " over %s executions", count(p.Executions))
			}
			b.WriteString(".")
		}
		if p.ActiveGBHours > 0 || p.RetainedGBHours > 0 {
			fmt.Fprintf(b, " Estimated storage saved a month: %s GB-hours active, %s GB-hours retained.",
				trimFloat(p.ActiveGBHours), trimFloat(p.RetainedGBHours))
		}
	}
	b.WriteString("\n\n")

	if g, err := kb.GuidanceFor(f.Guidance); err == nil {
		fmt.Fprintf(b, "Why it costs: %s\n\nFix: %s\n\nTradeoffs: %s\n\n", g.WhyItCosts, g.Fix, g.Tradeoffs)
	}

	b.WriteString("Evidence:\n\n")
	for _, e := range f.Evidence {
		fmt.Fprintf(b, "- %s\n", evidenceLine(e))
	}
	b.WriteString("\n")
}

func sourceName(s string) string {
	switch s {
	case estimate.SourceConfig:
		return "Namespace configuration"
	case estimate.SourceMetrics:
		return "Cloud metrics"
	case estimate.SourceCode:
		return "your code"
	}
	return "Event History"
}

// evidenceLine describes one piece of evidence in Markdown.
func evidenceLine(e estimate.Evidence) string {
	switch {
	case e.WorkflowID != "":
		return fmt.Sprintf("`%s` run `%s`, events %s: %s", e.WorkflowID, e.RunID, ids(e.EventIDs), e.Note)
	case e.File != "":
		return fmt.Sprintf("`%s:%d`: %s", e.File, e.Line, e.Note)
	case e.Metric != "":
		return fmt.Sprintf("`%s` over %s: %s", e.Metric, e.Window, e.Note)
	case e.Setting != "":
		return fmt.Sprintf("`%s`: %s", e.Setting, e.Note)
	}
	return e.Note
}

func ids(xs []int64) string {
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = fmt.Sprint(x)
	}
	return strings.Join(s, ", ")
}

func actions(x float64) string { return estimate.FormatActions(x) }

// count formats an integer with thousands separators, or "unknown" for -1.
func count(n int64) string {
	if n < 0 {
		return "unknown"
	}
	s := fmt.Sprint(n)
	var out []byte
	for i := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out)
}

func trimFloat(x float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", x), "0"), ".")
}
