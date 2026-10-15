package tui

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/cloudauth"
)

func renderSetup(t *testing.T, width, height int, setup func(*setupModel)) string {
	t.Helper()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TEMPORAL_CONFIG_FILE", "/nonexistent/temporal.toml")
	t.Setenv("TEMPORAL_ADDRESS", "")
	s := newSetup(context.Background(), false, true)
	s.Update(tea.WindowSizeMsg{Width: width, Height: height})
	setup(s)
	return s.View()
}

func checkFits(t *testing.T, out string, width, height int, want ...string) {
	t.Helper()
	lines := strings.Split(out, "\n")
	if len(lines) > height {
		t.Errorf("%d lines, want at most %d", len(lines), height)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > width {
			t.Errorf("line %d is %d wide", i, w)
		}
	}
	plain := ansi.Strip(out)
	for _, s := range want {
		if !strings.Contains(plain, s) {
			t.Errorf("missing %q", s)
		}
	}
	if os.Getenv("TCOPT_SHOW_VIEW") != "" {
		t.Logf("\n%s", out)
	}
}

func TestSetupScreensFit(t *testing.T) {
	ns := []cloudauth.Namespace{
		{Name: "orders.a1b2c", Region: "aws-us-east-1", APIKeyAuth: true},
		{Name: "payments.a1b2c", Region: "aws-us-west-2", APIKeyAuth: true},
		{Name: "legacy-mtls.a1b2c", Region: "gcp-us-central1"},
	}
	cases := map[string]struct {
		setup func(*setupModel)
		want  []string
	}{
		"choose": {func(*setupModel) {}, []string{"SETUP", "Log in to Temporal Cloud", "↑↓ choose"}},
		"login": {func(s *setupModel) {
			s.step = stepLogin
			s.pending = &cloudauth.Pending{URL: "https://login.tmprl.cloud/activate?user_code=ABCD-EFGH", UserCode: "ABCD-EFGH", Expires: time.Now().Add(15 * time.Minute)}
		}, []string{"ABCD-EFGH", "WAITING ON YOU", "open the browser again"}},
		"pick": {func(s *setupModel) {
			s.step, s.namespaces = stepPickNamespace, ns
			s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pay")})
		}, []string{"payments.a1b2c", "1 of 3 Namespaces", "Filter: pay"}},
		"confirm": {func(s *setupModel) {
			s.step, s.chosen = stepConfirmKey, ns[0]
		}, []string{"CREATE AN API KEY", "orders.a1b2c", "in 7 days", "create key"}},
	}
	for name, tc := range cases {
		for _, w := range []int{80, 120} {
			t.Run(name, func(t *testing.T) {
				checkFits(t, renderSetup(t, w, 24, tc.setup), w, 24, tc.want...)
			})
		}
	}
}

func TestPickSkipsMTLSOnlyNamespaces(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := newSetup(context.Background(), false, false)
	s.step = stepPickNamespace
	s.namespaces = []cloudauth.Namespace{{Name: "legacy-mtls.a1b2c"}}
	s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if s.step != stepPickNamespace || !strings.Contains(s.status, "mTLS") {
		t.Errorf("step %v, status %q: an mTLS-only Namespace should be refused", s.step, s.status)
	}
}
