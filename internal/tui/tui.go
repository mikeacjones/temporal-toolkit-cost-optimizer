// Package tui is tcopt's Bubble Tea interface.
//
// In this first cut the TUI runs the analysis in-process. Phase 2 moves it
// into the Session and Analysis Workflows, and the state hub feeds this
// model instead.
package tui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/analyze"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/config"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/estimate"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/history"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/kb"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/theme"
)

// Options configure the TUI.
type Options struct {
	// Target is the Namespace chosen with --profile or TEMPORAL_*
	// variables. When it's nil, the TUI connects to the saved target, or
	// walks the user through setup.
	Target  *config.Target
	Analyze analyze.Options
	// Repos override the saved repos for code checks and SDK versions.
	Repos []string
	// RPS and CacheDir configure history reads.
	RPS      float64
	CacheDir string
	// Motion is false with --reduced-motion, TCOPT_REDUCED_MOTION=1, over
	// SSH, or when output isn't a terminal.
	Motion bool
}

// connection is a Namespace the overview analyzes.
type connection struct {
	who       string // "PROFILE prod" or "LOGIN you@example.com", for the header
	namespace string
	reader    *history.Reader
	target    *config.Target
}

// MotionAllowed applies the reduced-motion rules from the environment.
func MotionAllowed(reducedFlag, isTTY bool) bool {
	if reducedFlag || !isTTY {
		return false
	}
	if os.Getenv("TCOPT_REDUCED_MOTION") == "1" {
		return false
	}
	if os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "" {
		return false
	}
	return true
}

// Run starts the TUI and blocks until the user quits.
func Run(ctx context.Context, opts Options) error {
	a := newApp(ctx, opts)
	p := tea.NewProgram(a, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	a.close()
	kitty.clear()
	return err
}

type (
	progressMsg analyze.Progress
	doneMsg     struct {
		rep *analyze.Report
		err error
	}
)

// otherRow labels findings that aren't about one analyzed Workflow Type.
const otherRow = "Namespace, Schedules, and code"

type typeRow struct {
	name     string
	perRun   float64 // mean Actions per execution today
	monthly  float64 // Actions per month today, or -1 when unknown
	saved    float64 // Actions per month the findings would save
	sampled  int
	findings []estimate.Finding
}

// model is the overview screen for one connected Namespace.
type model struct {
	ctx    context.Context
	cancel context.CancelFunc
	conn   connection
	opts   analyze.Options
	events chan tea.Msg

	width, height int
	frame         int
	motion        bool
	stars         *starfield

	running  bool
	started  time.Time
	progress analyze.Progress
	report   *analyze.Report
	err      error
	status   string // the latest acknowledgment, such as "Analysis started"

	rows     []typeRow
	selected int
	finding  int
	detail   bool
}

func newModel(ctx context.Context, conn connection, opts analyze.Options, motion bool) *model {
	ctx, cancel := context.WithCancel(ctx)
	opts.Profile = conn.who
	return &model{
		ctx:    ctx,
		cancel: cancel,
		conn:   conn,
		opts:   opts,
		events: make(chan tea.Msg, 64),
		motion: motion,
		stars:  newStarfield(2),
	}
}

// Init starts the analysis. The app owns the animation tick.
func (m *model) Init() tea.Cmd {
	return tea.Batch(m.startAnalysis(), m.waitForEvent())
}

// close stops the analysis when the user switches Namespace or quits.
func (m *model) close() {
	m.cancel()
	if m.conn.target != nil {
		m.conn.target.Close()
	}
}

func tick() tea.Cmd {
	return tea.Tick(125*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *model) waitForEvent() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-m.events:
			return msg
		case <-m.ctx.Done():
			return nil
		}
	}
}

func (m *model) startAnalysis() tea.Cmd {
	if m.running {
		return nil
	}
	m.running = true
	m.started = time.Now()
	m.err = nil
	m.status = "Analysis started"
	return func() tea.Msg {
		go func() {
			rep, err := analyze.Run(m.ctx, m.conn.reader, m.opts, func(p analyze.Progress) {
				select {
				case m.events <- progressMsg(p):
				default: // drop progress rather than block the fetchers
				}
			})
			select {
			case m.events <- doneMsg{rep, err}:
			case <-m.ctx.Done():
			}
		}()
		return nil
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.stars.resize(msg.Width)
	case tickMsg:
		m.frame++
		if m.motion {
			m.stars.step(m.progress.Stage == "fetch" && m.running)
		}
		return m, nil
	case progressMsg:
		m.progress = analyze.Progress(msg)
		return m, m.waitForEvent()
	case doneMsg:
		m.running = false
		m.err = msg.err
		if msg.err == nil {
			m.report = msg.rep
			m.buildRows()
			m.status = fmt.Sprintf("Analysis done in %s", time.Since(m.started).Round(time.Second))
		}
		return m, m.waitForEvent()
	case tea.KeyMsg:
		return m, m.key(msg)
	}
	return m, nil
}

func (m *model) key(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "q", "ctrl+c":
		return tea.Quit
	case "up", "k":
		if m.selected > 0 {
			m.selected--
			m.finding = 0
		}
	case "down", "j":
		if m.selected < len(m.rows)-1 {
			m.selected++
			m.finding = 0
		}
	case "right", "l", "tab":
		if r := m.currentRow(); r != nil && m.finding < len(r.findings)-1 {
			m.finding++
		}
	case "left", "h", "shift+tab":
		if m.finding > 0 {
			m.finding--
		}
	case "enter":
		m.detail = !m.detail
	case "n":
		return func() tea.Msg { return changeTargetMsg{} }
	case "m":
		m.motion = !m.motion
		if m.motion {
			m.status = "Motion on"
		} else {
			m.status = "Motion paused"
		}
	case "r":
		if m.running {
			m.status = "An analysis is already running"
			return nil
		}
		return m.startAnalysis()
	}
	return nil
}

func (m *model) buildRows() {
	byType := map[string]*typeRow{}
	m.rows = m.rows[:0]
	for _, t := range m.report.Types {
		m.rows = append(m.rows, typeRow{
			name: t.WorkflowType, perRun: t.ActionsPerExecution.Mean, monthly: t.MonthlyActions,
			saved: t.ActionsSavedPerMonth, sampled: t.Sampled,
		})
	}
	for i := range m.rows {
		byType[m.rows[i].name] = &m.rows[i]
	}
	// Findings about the Namespace, Schedules, metrics, or code may not
	// belong to an analyzed Workflow Type; they get a row of their own.
	other := typeRow{name: otherRow, monthly: -1}
	for _, f := range m.report.Findings {
		if r := byType[f.WorkflowType]; r != nil {
			r.findings = append(r.findings, f)
			continue
		}
		other.findings = append(other.findings, f)
		if p := f.ProjectedMonthly; p != nil {
			other.saved += p.Actions
		}
	}
	if len(other.findings) > 0 {
		m.rows = append(m.rows, other)
	}
	// Most savings first; then the types that use the most Actions today.
	sort.SliceStable(m.rows, func(i, j int) bool {
		if m.rows[i].saved != m.rows[j].saved {
			return m.rows[i].saved > m.rows[j].saved
		}
		return m.rows[i].monthly > m.rows[j].monthly
	})
	m.selected, m.finding = 0, 0
}

func (m *model) currentRow() *typeRow {
	if m.selected < 0 || m.selected >= len(m.rows) {
		return nil
	}
	return &m.rows[m.selected]
}

func (m *model) currentFinding() *estimate.Finding {
	r := m.currentRow()
	if r == nil || m.finding >= len(r.findings) {
		return nil
	}
	return &r.findings[m.finding]
}

func (m *model) mood() mood {
	switch {
	case m.err != nil:
		return moodError
	case m.running && m.progress.Stage == "fetch":
		return moodFetching
	case m.report != nil && len(m.report.Findings) > 0:
		return moodFound
	}
	return moodIdle
}

// ziggyLine is what Ziggy says. It always matches what's on screen.
func (m *model) ziggyLine() string {
	switch m.mood() {
	case moodError:
		return "Couldn't finish the analysis. Press r to try again."
	case moodFetching:
		return fmt.Sprintf("Scanning %s histories: %s of %s.", m.progress.WorkflowType,
			commas(int64(m.progress.Done)), commas(int64(m.progress.Total)))
	case moodFound:
		if f := m.currentFinding(); f != nil && f.ProjectedMonthly != nil {
			extra := "extra Actions"
			if f.ActionsSavedPerExecution.P50 == 1 {
				extra = "extra Action"
			}
			return fmt.Sprintf("%s: %s %s per run, about %s Actions a month.", f.WorkflowType,
				short(f.ActionsSavedPerExecution.P50), extra, estimate.FormatActions(f.ProjectedMonthly.Actions))
		}
		return "Pick a Workflow Type to inspect."
	}
	if m.running {
		switch m.progress.Stage {
		case "rank":
			return "Ranking Workflow Types by executions."
		case "sample":
			return "Picking executions to sample from " + m.progress.WorkflowType + "."
		case "children":
			return "Reading Child Workflow histories for " + m.progress.WorkflowType + "."
		case "detect":
			return "Running detectors on " + m.progress.WorkflowType + "."
		case "collect":
			return "Running " + m.progress.WorkflowType + " checks."
		}
		return "Connecting to the Namespace."
	}
	if m.report != nil {
		return "No findings in the sampled executions."
	}
	return "Pick a Workflow Type to inspect."
}

func (m *model) View() string {
	if m.width == 0 {
		return ""
	}
	header := m.viewHeader()
	footer := m.viewFooter()
	// The finding panel may take what's left after a body of at least 6 rows.
	strip := m.viewFindingStrip(m.height - lipgloss.Height(header) - lipgloss.Height(footer) - 6)
	used := lipgloss.Height(header) + lipgloss.Height(footer) + lipgloss.Height(strip)
	body := m.viewBody(max(m.height-used, 6))
	return lipgloss.JoinVertical(lipgloss.Left, header, body, strip, footer)
}

func (m *model) viewHeader() string {
	eyebrow := []seg{
		{text: "✦ ", fg: theme.Green},
		{text: fmt.Sprintf("TEMPORAL COST OPTIMIZER · %s · NS %s", m.conn.who, m.conn.namespace), fg: theme.Indigo100, bold: true},
	}
	var figure []seg
	switch {
	case m.report != nil && len(m.report.Findings) > 0:
		figure = append([]seg{{text: "Est. savings  ", fg: theme.Text}}, gradientSegs(estimate.FormatActions(m.report.ActionsSavedPerMonth)+" Actions", theme.Green, theme.Indigo100)...)
		figure = append(figure, seg{text: " / month", fg: theme.Text})
	case m.report != nil:
		figure = []seg{{text: "No findings yet  ", fg: theme.Text},
			{text: fmt.Sprintf("these types use about %s Actions / month", estimate.FormatActions(m.report.MonthlyActions)), fg: theme.Slate}}
	case m.running:
		figure = []seg{{text: "Est. savings  ", fg: theme.Text}, {text: "loading", fg: theme.Slate}}
	default:
		figure = []seg{{text: "Est. savings  ", fg: theme.Text}, {text: "unavailable", fg: theme.Slate}}
	}

	var right []seg
	switch {
	case m.running:
		right = []seg{{text: " ANALYZING ", fg: theme.SpaceBlack, bg: theme.Green, bold: true}}
		if m.progress.Total > 0 {
			right = append(right, seg{text: fmt.Sprintf(" %d/%d", m.progress.Done, m.progress.Total), fg: theme.Text})
		}
	case m.err != nil:
		right = []seg{{text: "FAILED", fg: theme.Red, bold: true}}
	case m.report != nil:
		right = []seg{{text: fmt.Sprintf("%s · %s", plural(len(m.report.Findings), "finding"), plural(len(m.report.Types), "type")), fg: theme.Indigo100}}
	}
	return m.stars.headerBand([][]seg{eyebrow, figure}, [][]seg{nil, right})
}

func gradientSegs(s, from, to string) []seg {
	a, b := theme.Hex(from), theme.Hex(to)
	rs := []rune(s)
	out := make([]seg, len(rs))
	for i, r := range rs {
		t := 0.0
		if len(rs) > 1 {
			t = float64(i) / float64(len(rs)-1)
		}
		out[i] = seg{text: string(r), fg: theme.Lerp(a, b, t).String(), bold: true}
	}
	return out
}

func (m *model) viewBody(height int) string {
	return splitBody(m.width, height, m.typeLines, func(w int) string {
		return ziggyPanel(w, height, m.frame, m.mood(), m.motion, m.ziggyLine())
	})
}

func (m *model) typeLines(width int) []string {
	// Content is the panel width less its border and padding. Narrow
	// terminals drop the per-run column first.
	inner := width - 4
	showRun := inner-35 >= 16
	nameW := inner - 24
	if showRun {
		nameW = inner - 35
	}
	head := fmt.Sprintf("%-*s %10s %12s", nameW, "WORKFLOW TYPES", "ACT/MONTH", "SAVES/MONTH")
	if showRun {
		head = fmt.Sprintf("%-*s %10s %10s %12s", nameW, "WORKFLOW TYPES", "ACT/RUN", "ACT/MONTH", "SAVES/MONTH")
	}
	lines := []string{theme.Label.Render(head)}
	switch {
	case m.err != nil:
		for _, l := range wrap(m.err.Error(), width-4) {
			lines = append(lines, theme.ErrorLine.Render(l))
		}
	case len(m.rows) == 0 && m.running:
		lines = append(lines, theme.Muted.Render("Loading…"))
	case len(m.rows) == 0:
		lines = append(lines, theme.Muted.Render("No Workflow Types started in the window."))
	}
	for i, r := range m.rows {
		name := truncate(r.name, nameW)
		monthly := "unknown"
		if r.monthly >= 0 {
			monthly = estimate.FormatActions(r.monthly)
		}
		// "No findings" reads differently from a saving of 0.
		saves := theme.Muted.Render(fmt.Sprintf("%12s", "no findings"))
		if len(r.findings) > 0 {
			saves = theme.Number.Render(fmt.Sprintf("%12s", estimate.FormatActions(r.saved)))
		}
		cells := fmt.Sprintf("%-*s %10s ", nameW, name, monthly)
		if showRun {
			cells = fmt.Sprintf("%-*s %10s %10s ", nameW, name, estimate.FormatActions(r.perRun), monthly)
		}
		if i == m.selected {
			lines = append(lines, theme.Selected.Render(cells)+saves)
		} else {
			lines = append(lines, theme.Body.Render(cells)+saves)
		}
		if len(r.findings) > 0 && i == m.selected {
			var badges []string
			for _, f := range r.findings {
				badges = append(badges, f.ID+" "+theme.Badge(string(f.Confidence)))
			}
			lines = append(lines, "  "+theme.Muted.Render(strings.Join(badges, "  ")))
		}
	}
	return lines
}

// viewFindingStrip renders the selected finding in at most maxHeight rows.
func (m *model) viewFindingStrip(maxHeight int) string {
	w := m.width - 2
	f := m.currentFinding()
	if f == nil {
		msg := "Findings appear here when the analysis finishes."
		if m.report != nil {
			msg = "No findings for this Workflow Type."
		}
		return theme.Panel.Width(w).Render(theme.Muted.Render(msg))
	}
	r := m.currentRow()
	title := f.Title
	if title == "" {
		title = f.ID
	}
	head := theme.Label.Render(f.ID) + theme.Body.Render(" · "+title)
	where := "from " + f.Source
	if p := f.Prevalence; p != nil {
		where = fmt.Sprintf("%d of %d sampled", p.Matched, p.Sampled)
	}
	count := theme.Muted.Render(fmt.Sprintf("%s · finding %d of %d", where, m.finding+1, len(r.findings)))
	var lines []string
	if gap := w - 2 - lipgloss.Width(head) - lipgloss.Width(count); gap >= 2 {
		lines = []string{head + strings.Repeat(" ", gap) + count}
	} else {
		lines = []string{truncate(head, w-2), count}
	}

	g, _ := kb.GuidanceFor(f.Guidance)
	fix := ""
	if g != nil {
		fix = g.Fix
	}
	if len(f.Evidence) > 0 {
		e := f.Evidence[0]
		lines = append(lines, theme.Body.Render(truncate(fmt.Sprintf("%s · fix: %s", evidenceShort(e), fix), w-2)))
	}
	saving := fmt.Sprintf("Saves p50 %s, p95 %s Actions per matching run", short(f.ActionsSavedPerExecution.P50), short(f.ActionsSavedPerExecution.P95))
	if p := f.ProjectedMonthly; p != nil {
		saving += fmt.Sprintf(" · est. %s Actions a month over %s runs", estimate.FormatActions(p.Actions), commas(p.Executions))
	}
	lines = append(lines, theme.Muted.Render(truncate(saving, w-2)))

	if m.detail {
		lines = append(lines, "", theme.Label.Render("EVIDENCE"))
		for _, e := range f.Evidence {
			lines = append(lines, theme.Body.Render(truncate(evidenceLong(e), w-2)))
		}
		if g != nil {
			lines = append(lines, "", theme.Label.Render("WHY IT COSTS"), theme.Body.Render(g.WhyItCosts),
				"", theme.Label.Render("TRADEOFFS"), theme.Body.Render(g.Tradeoffs))
		}
	}
	return theme.Panel.Width(w).Render(strings.Join(clip(lines, max(maxHeight-2, 3)), "\n"))
}

func (m *model) viewFooter() string {
	return footer(m.width, []hint{
		{"↑↓", "type"}, {"←→", "finding"}, {"⏎", "details"}, {"r", "re-run"}, {"n", "namespace"}, {"m", "motion"}, {"q", "quit"},
	}, m.status)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func clip(lines []string, n int) []string {
	if len(lines) > n {
		return lines[:n]
	}
	return lines
}

func wrap(s string, w int) []string {
	if w <= 0 {
		return []string{s}
	}
	return strings.Split(lipgloss.NewStyle().Width(w).Render(s), "\n")
}

func truncate(s string, w int) string {
	if w <= 1 || lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	if len(r) > w-1 {
		r = r[:w-1]
	}
	return string(r) + "…"
}

func joinIDs(ids []int64) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = fmt.Sprint(id)
	}
	return strings.Join(s, ", ")
}

func short(x float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", x), "0"), ".")
}

func commas(n int64) string {
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

// evidenceShort names what proves a finding in a few words.
func evidenceShort(e estimate.Evidence) string {
	switch {
	case len(e.EventIDs) > 0:
		return "Events " + joinIDs(e.EventIDs)
	case e.File != "":
		return fmt.Sprintf("%s:%d", e.File, e.Line)
	case e.Metric != "":
		return "Metric over " + e.Window
	case e.Setting != "":
		return e.Setting
	}
	return "Evidence"
}

// evidenceLong is one line of the details view.
func evidenceLong(e estimate.Evidence) string {
	switch {
	case e.WorkflowID != "":
		return fmt.Sprintf("%s · events %s · %s", e.WorkflowID, joinIDs(e.EventIDs), e.Note)
	case e.File != "":
		return fmt.Sprintf("%s:%d · %s", e.File, e.Line, e.Note)
	case e.Metric != "":
		return fmt.Sprintf("%s over %s · %s", e.Metric, e.Window, e.Note)
	case e.Setting != "":
		return e.Setting + " · " + e.Note
	}
	return e.Note
}
