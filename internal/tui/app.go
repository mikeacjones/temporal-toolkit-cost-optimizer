package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/collect"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/config"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/history"
)

type (
	tickMsg struct{}
	// changeTargetMsg asks to go back to setup and pick another Namespace.
	changeTargetMsg struct{}
	// connectedMsg hands a working connection from setup to the overview.
	connectedMsg struct {
		target *config.Target
		who    string
	}
)

// app shows either the setup screens or the overview, and owns the
// animation tick so switching screens never starts a second one.
type app struct {
	ctx           context.Context
	opts          Options
	width, height int

	setup    *setupModel
	overview *model
}

func newApp(ctx context.Context, opts Options) *app {
	a := &app{ctx: ctx, opts: opts}
	if t := opts.Target; t != nil {
		who := "PROFILE " + t.Profile
		if t.Profile == config.EnvProfile {
			who = "ENV"
		}
		a.overview = a.newOverview(t, who)
	} else {
		a.setup = newSetup(ctx, opts.Motion, true)
	}
	return a
}

func (a *app) newOverview(t *config.Target, who string) *model {
	r := history.NewReader(t.Client.WorkflowService(), t.Namespace, a.opts.RPS, a.opts.CacheDir)
	conn := connection{who: who, namespace: t.Namespace, reader: r, target: t}
	opts := a.opts.Analyze
	src := collect.Resolve(t, a.opts.Repos)
	opts.Collectors, opts.Caveats = collect.Collectors(src)
	opts.RepoPaths = src.Repos
	return newModel(a.ctx, conn, opts, a.opts.Motion)
}

func (a *app) close() {
	if a.overview != nil {
		a.overview.close()
	}
	if a.setup != nil {
		a.setup.close()
	}
}

func (a *app) current() tea.Model {
	if a.overview != nil {
		return a.overview
	}
	return a.setup
}

func (a *app) Init() tea.Cmd {
	return tea.Batch(tick(), a.current().Init())
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
	case tickMsg:
		_, cmd := a.current().Update(msg)
		return a, tea.Batch(cmd, tick())
	case changeTargetMsg:
		if a.overview != nil {
			a.overview.close()
			a.overview = nil
		}
		a.setup = newSetup(a.ctx, a.opts.Motion, false)
		return a, tea.Batch(a.resize(), a.setup.Init())
	case connectedMsg:
		if a.setup != nil {
			a.setup.close()
			a.setup = nil
		}
		a.overview = a.newOverview(msg.target, msg.who)
		return a, tea.Batch(a.resize(), a.overview.Init())
	}
	_, cmd := a.current().Update(msg)
	return a, cmd
}

// resize sends the current size to a screen that was just created.
func (a *app) resize() tea.Cmd {
	w, h := a.width, a.height
	return func() tea.Msg { return tea.WindowSizeMsg{Width: w, Height: h} }
}

func (a *app) View() string { return a.current().View() }
