package tui

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/analyze"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/detect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/estimate"
)

func sampleReport() *analyze.Report {
	return &analyze.Report{
		Namespace:            "orders.a1b2c",
		ActionsSavedPerMonth: 8_200_000, MonthlyActions: 41_000_000,
		Types: []analyze.TypeSummary{
			{WorkflowType: "OrderWorkflow", ActionsSavedPerMonth: 8_200_000, MonthlyActions: 41_000_000, ActionsPerExecution: estimate.Percentiles{Mean: 10}, Sampled: 200},
			{WorkflowType: "NotifyWorkflow", MonthlyActions: 900_000, ActionsPerExecution: estimate.Percentiles{Mean: 3}, Sampled: 200},
		},
		Findings: []estimate.Finding{{
			ID: "SA-002", Title: "Several Search Attribute upserts in one Workflow Task",
			WorkflowType: "OrderWorkflow", Confidence: detect.High,
			Prevalence:               &estimate.Prevalence{Matched: 187, Sampled: 200},
			ActionsSavedPerExecution: estimate.Percentiles{P50: 2, P95: 4},
			ProjectedMonthly:         &estimate.Projection{Executions: 4_100_000, Actions: 8_200_000},
			Evidence:                 []estimate.Evidence{{WorkflowID: "order-123", EventIDs: []int64{14, 15, 16}, Note: "3 upserts"}},
			Guidance:                 "SA-002",
		}},
	}
}

func render(t *testing.T, width, height int, setup func(*model)) string {
	return renderWith(t, termenv.Ascii, width, height, setup)
}

func renderWith(t *testing.T, profile termenv.Profile, width, height int, setup func(*model)) string {
	t.Helper()
	lipgloss.SetColorProfile(profile)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	m := newModel(context.Background(), connection{who: "PROFILE prod", namespace: "orders.a1b2c"}, analyze.Options{}, false)
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	setup(m)
	return m.View()
}

func TestOverviewFitsTheTerminal(t *testing.T) {
	for _, tc := range []struct {
		w       int
		profile termenv.Profile
	}{{80, termenv.Ascii}, {120, termenv.Ascii}, {80, termenv.TrueColor}, {100, termenv.ANSI256}} {
		w := tc.w
		out := renderWith(t, tc.profile, w, 24, func(m *model) {
			m.report = sampleReport()
			m.buildRows()
		})
		lines := strings.Split(out, "\n")
		if len(lines) > 24 {
			t.Errorf("width %d: %d lines, want at most 24", w, len(lines))
		}
		for i, l := range lines {
			if lw := lipgloss.Width(l); lw > w {
				t.Errorf("width %d: line %d is %d wide: %q", w, i, lw, l)
			}
		}
		for _, want := range []string{"PROFILE prod", "NS orders.a1b2c", "8.2M Actions", "OrderWorkflow", "41M", "no findings", "[HIGH]", "SA-002", "187 of 200 sampled"} {
			if !strings.Contains(ansi.Strip(out), want) {
				t.Errorf("width %d: missing %q", w, want)
			}
		}
		if os.Getenv("TCOPT_SHOW_VIEW") != "" && tc.profile == termenv.Ascii {
			t.Logf("\n%s", out)
		}
	}
}

func TestDetailsFitASmallTerminal(t *testing.T) {
	out := render(t, 100, 20, func(m *model) {
		m.report = sampleReport()
		m.buildRows()
		m.detail = true
	})
	if n := len(strings.Split(out, "\n")); n > 20 {
		t.Errorf("%d lines, want at most 20", n)
	}
}

func TestStatesLookDifferent(t *testing.T) {
	loading := render(t, 100, 24, func(m *model) { m.running = true })
	empty := render(t, 100, 24, func(m *model) { m.report = &analyze.Report{}; m.buildRows() })
	if !strings.Contains(loading, "ANALYZING") || !strings.Contains(loading, "loading") {
		t.Error("loading state should show the ANALYZING pill and a loading figure")
	}
	if !strings.Contains(empty, "No Workflow Types started in the window.") {
		t.Error("empty state should say there was nothing to analyze")
	}
}
