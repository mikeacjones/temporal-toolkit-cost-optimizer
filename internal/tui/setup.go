package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/cloudauth"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/config"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/secrets"
	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/theme"
)

// keyTTL is how long the API keys tcopt creates last. When one expires,
// the TUI offers to create the next with one keypress.
const keyTTL = 7 * 24 * time.Hour

// A new key usually reaches the Namespace within a minute, but can take
// longer. tcopt keeps checking until the user cancels, and says so once the
// wait passes activationSlow.
const activationSlow = 2 * time.Minute

type setupStep int

const (
	stepReconnecting setupStep = iota // trying the saved target
	stepChoose                        // pick how to connect
	stepLogin                         // waiting for browser approval
	stepLoading                       // loading the user's Namespaces
	stepPickNamespace
	stepConfirmKey
	stepActivating // key created, waiting until it can read
	stepProfile    // connecting with a CLI profile
)

type choiceKind int

const (
	choiceLogin choiceKind = iota
	choiceProfile
	choiceEnv
)

type choice struct {
	kind    choiceKind
	title   string
	detail  string
	profile string
}

type (
	savedMsg struct {
		target *config.Target
		err    error
	}
	loginStartedMsg struct {
		pending *cloudauth.Pending
		err     error
	}
	loginDoneMsg struct {
		pending *cloudauth.Pending
		err     error
	}
	sessionMsg struct {
		sess *cloudauth.Session
		err  error
	}
	namespacesMsg struct {
		list []cloudauth.Namespace
		err  error
	}
	keyCreatedMsg struct {
		settings config.TargetSettings
		secret   string
		err      error
	}
	activationMsg struct {
		target *config.Target
		err    error
		gen    int
	}
	profileMsg struct {
		target *config.Target
		info   config.ProfileInfo
		err    error
	}
)

// setupModel walks the user through choosing a Namespace and, for Temporal
// Cloud, logging in and creating an API key. Every step that creates
// something waits for the user to confirm it.
type setupModel struct {
	ctx    context.Context
	cancel context.CancelFunc

	width, height int
	frame         int
	motion        bool
	stars         *starfield

	step   setupStep
	err    error
	status string

	choices []choice
	cursor  int

	auth      *cloudauth.Auth
	sess      *cloudauth.Session
	pending   *cloudauth.Pending
	loginStop context.CancelFunc

	namespaces []cloudauth.Namespace
	filter     string
	chosen     cloudauth.Namespace
	renewing   bool // the saved target's key expired; we're replacing it

	saved       config.TargetSettings
	activeSince time.Time
	attempts    int
	lastErr     error // the latest failed check while activating
	checkGen    int   // ignores checks that a manual retry replaced
}

func newSetup(ctx context.Context, motion, reconnect bool) *setupModel {
	ctx, cancel := context.WithCancel(ctx)
	s := &setupModel{
		ctx:    ctx,
		cancel: cancel,
		motion: motion,
		stars:  newStarfield(2),
		auth:   cloudauth.New(),
		step:   stepChoose,
	}
	s.choices = buildChoices()
	if reconnect {
		if st, err := config.LoadSettings(); err == nil && st.Target.Source != "" {
			s.saved = st.Target
			s.step = stepReconnecting
		}
	}
	return s
}

func buildChoices() []choice {
	out := []choice{{
		kind:   choiceLogin,
		title:  "Log in to Temporal Cloud",
		detail: "Opens your browser. tcopt then creates a short-lived API key for the Namespace you pick.",
	}}
	if profiles, err := config.Profiles(""); err == nil {
		for _, p := range profiles {
			ns := p.Namespace
			if ns == "" {
				ns = "default"
			}
			out = append(out, choice{
				kind: choiceProfile, profile: p.Name,
				title:  "Temporal CLI profile " + p.Name,
				detail: fmt.Sprintf("%s at %s", ns, p.Address),
			})
		}
	}
	if addr := os.Getenv("TEMPORAL_ADDRESS"); addr != "" {
		out = append(out, choice{
			kind: choiceEnv, title: "TEMPORAL_* environment variables",
			detail: fmt.Sprintf("%s at %s", os.Getenv("TEMPORAL_NAMESPACE"), addr),
		})
	}
	return out
}

func (s *setupModel) close() {
	s.cancel()
	if s.sess != nil {
		s.sess.Close()
	}
}

func (s *setupModel) Init() tea.Cmd {
	if s.step == stepReconnecting {
		s.status = "Connecting to " + s.saved.Namespace
		saved := s.saved
		return func() tea.Msg {
			t, err := config.DialSaved(saved)
			if err == nil {
				if err = config.CheckRead(context.Background(), t); err != nil {
					t.Close()
					t = nil
				}
			}
			return savedMsg{t, err}
		}
	}
	return nil
}

func (s *setupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.stars.resize(msg.Width)
	case tickMsg:
		s.frame++
		if s.motion {
			s.stars.step(s.step == stepLogin || s.step == stepActivating || s.step == stepLoading)
		}
	case tea.KeyMsg:
		return s, s.key(msg)

	case savedMsg:
		if msg.err == nil {
			return s, connected(msg.target, s.whoFor(s.saved))
		}
		if s.saved.Source == config.SourceCloudLogin && config.IsAuthError(msg.err) && !errors.Is(msg.err, config.ErrKeyMissing) &&
			time.Since(s.saved.KeyExpires.Add(-keyTTL)) < 30*time.Minute {
			// A key created moments ago may still be on its way to the
			// Namespace. Wait for it rather than offer another.
			s.step, s.activeSince, s.attempts = stepActivating, time.Now(), 0
			return s, s.activate(0)
		}
		if s.saved.Source == config.SourceCloudLogin && (errors.Is(msg.err, config.ErrKeyMissing) || config.IsAuthError(msg.err)) {
			// The key expired or was revoked. Offer a new one for the same
			// Namespace.
			s.renewing = true
			s.chosen = cloudauth.Namespace{Name: s.saved.Namespace, Address: s.saved.Address, APIKeyAuth: true}
			return s, s.resumeLogin()
		}
		s.step, s.err = stepChoose, fmt.Errorf("couldn't reconnect to %s: %w", s.saved.Namespace, msg.err)

	case loginStartedMsg:
		if msg.err != nil {
			s.step, s.err = stepChoose, msg.err
			return s, nil
		}
		s.pending = msg.pending
		if err := cloudauth.OpenBrowser(msg.pending.URL); err != nil {
			s.status = "Couldn't open a browser; open the link below"
		} else {
			s.status = "Opened the login page in your browser"
		}
		ctx, stop := context.WithDeadline(s.ctx, msg.pending.Expires)
		s.loginStop = stop
		p := msg.pending
		return s, func() tea.Msg { return loginDoneMsg{p, s.auth.Wait(ctx, p)} }
	case loginDoneMsg:
		if msg.pending != s.pending || s.step != stepLogin {
			return s, nil // a login the user cancelled or replaced
		}
		if s.loginStop != nil {
			s.loginStop()
		}
		if msg.err != nil {
			s.step, s.err = stepChoose, msg.err
			return s, nil
		}
		s.status = "Logged in"
		return s, s.connectSession()
	case sessionMsg:
		if msg.err != nil {
			if errors.Is(msg.err, cloudauth.ErrNotLoggedIn) {
				return s, s.startLogin()
			}
			s.step, s.err = stepChoose, msg.err
			return s, nil
		}
		s.sess = msg.sess
		if s.renewing {
			s.step = stepConfirmKey
			return s, nil
		}
		s.step = stepLoading
		sess := s.sess
		return s, func() tea.Msg {
			list, err := sess.Namespaces(context.Background())
			return namespacesMsg{list, err}
		}
	case namespacesMsg:
		if msg.err != nil {
			s.step, s.err = stepChoose, msg.err
			return s, nil
		}
		s.namespaces, s.cursor, s.filter = msg.list, 0, ""
		s.step = stepPickNamespace
	case keyCreatedMsg:
		if msg.err != nil {
			s.step, s.err = stepConfirmKey, msg.err
			return s, nil
		}
		s.saved = msg.settings
		s.step, s.activeSince, s.attempts, s.lastErr = stepActivating, time.Now(), 0, nil
		s.status = "Created API key " + msg.settings.KeyName
		return s, s.activate(0)
	case activationMsg:
		if msg.gen != s.checkGen || s.step != stepActivating {
			if msg.target != nil {
				msg.target.Close()
			}
			return s, nil // replaced by a manual check, or the user left
		}
		if msg.err == nil {
			s.status = "Connected"
			return s, connected(msg.target, s.whoFor(s.saved))
		}
		// Keep checking, whatever the error: the key exists, and going back
		// to the confirm screen would invite creating a second one.
		s.attempts++
		s.lastErr = msg.err
		return s, s.activate(activationBackoff(s.attempts))
	case profileMsg:
		if msg.err != nil {
			s.step, s.err = stepChoose, msg.err
			return s, nil
		}
		if msg.info.Name != "" {
			t := config.TargetSettings{
				Source: config.SourceProfile, Profile: msg.info.Name, Namespace: msg.target.Namespace,
			}
			if err := config.SaveTarget(t); err != nil {
				s.status = "Couldn't save settings: " + err.Error()
			}
		}
		who := "PROFILE " + msg.target.Profile
		if msg.target.Profile == config.EnvProfile {
			who = "ENV"
		}
		return s, connected(msg.target, who)
	}
	return s, nil
}

func connected(t *config.Target, who string) tea.Cmd {
	return func() tea.Msg { return connectedMsg{target: t, who: who} }
}

func (s *setupModel) whoFor(t config.TargetSettings) string {
	if t.Source == config.SourceProfile {
		return "PROFILE " + t.Profile
	}
	if t.Email != "" {
		return "LOGIN " + t.Email
	}
	return "CLOUD LOGIN"
}

func (s *setupModel) key(k tea.KeyMsg) tea.Cmd {
	key := k.String()
	if key == "ctrl+c" {
		return tea.Quit
	}
	switch s.step {
	case stepChoose:
		switch key {
		case "q":
			return tea.Quit
		case "up", "k":
			s.cursor = max(s.cursor-1, 0)
		case "down", "j":
			s.cursor = min(s.cursor+1, len(s.choices)-1)
		case "enter":
			s.err = nil
			return s.pick(s.choices[s.cursor])
		}
	case stepLogin:
		switch key {
		case "o":
			if s.pending != nil {
				_ = cloudauth.OpenBrowser(s.pending.URL)
				s.status = "Opened the login page again"
			}
		case "esc":
			if s.loginStop != nil {
				s.loginStop()
			}
			s.step, s.status = stepChoose, "Login cancelled"
		}
	case stepPickNamespace:
		visible := s.visibleNamespaces()
		switch key {
		case "up":
			s.cursor = max(s.cursor-1, 0)
		case "down":
			s.cursor = min(s.cursor+1, len(visible)-1)
		case "esc":
			if s.filter != "" {
				s.filter, s.cursor = "", 0
			} else {
				s.step = stepChoose
			}
		case "backspace":
			if s.filter != "" {
				s.filter = s.filter[:len(s.filter)-1]
				s.cursor = 0
			}
		case "enter":
			if s.cursor < len(visible) {
				ns := visible[s.cursor]
				if !ns.APIKeyAuth {
					s.status = ns.Name + " only accepts mTLS; tcopt needs API key auth"
					return nil
				}
				s.chosen, s.err = ns, nil
				s.step = stepConfirmKey
			}
		default:
			if len(k.Runes) > 0 && k.Type == tea.KeyRunes {
				s.filter += string(k.Runes)
				s.cursor = 0
			}
		}
	case stepConfirmKey:
		switch key {
		case "enter":
			s.err = nil
			s.step = stepActivating
			s.activeSince = time.Now()
			s.status = "Creating the API key"
			return s.createKey()
		case "esc":
			if s.renewing {
				s.renewing = false
				s.step = stepChoose
			} else {
				s.step = stepPickNamespace
			}
		}
	case stepActivating:
		switch key {
		case "r":
			if s.saved.KeyName != "" {
				s.status = "Checking now"
				return s.activate(0)
			}
		case "esc":
			// The key stays saved; next start reconnects with it.
			s.step, s.status = stepChoose, "The key is saved; tcopt will try it again next time"
		}
	case stepLoading, stepReconnecting, stepProfile:
		if key == "esc" {
			s.step, s.status = stepChoose, "Cancelled"
		}
	}
	return nil
}

func (s *setupModel) pick(c choice) tea.Cmd {
	switch c.kind {
	case choiceLogin:
		return s.resumeLogin()
	case choiceProfile, choiceEnv:
		s.step = stepProfile
		s.status = "Connecting with " + c.title
		profile := c.profile
		return func() tea.Msg {
			t, err := config.DialProfile(profile, "", "")
			if err == nil {
				if err = config.CheckRead(context.Background(), t); err != nil {
					t.Close()
					t = nil
				}
			}
			return profileMsg{t, config.ProfileInfo{Name: profile}, err}
		}
	}
	return nil
}

// resumeLogin reuses a stored login when it's still valid, and starts the
// browser flow otherwise.
func (s *setupModel) resumeLogin() tea.Cmd {
	if err := s.auth.Load(); err != nil {
		return s.startLogin()
	}
	s.step = stepLoading
	s.status = "Using your saved Temporal Cloud login"
	return s.connectSession()
}

func (s *setupModel) startLogin() tea.Cmd {
	s.step, s.pending = stepLogin, nil
	s.status = "Starting login"
	return func() tea.Msg {
		p, err := s.auth.Start(s.ctx)
		return loginStartedMsg{p, err}
	}
}

func (s *setupModel) connectSession() tea.Cmd {
	auth := s.auth
	return func() tea.Msg {
		sess, err := auth.Connect(context.Background())
		return sessionMsg{sess, err}
	}
}

func (s *setupModel) createKey() tea.Cmd {
	sess, ns := s.sess, s.chosen
	return func() tea.Msg {
		key, err := sess.CreateKey(context.Background(), cloudauth.KeyName(), keyTTL)
		if err != nil {
			return keyCreatedMsg{err: err}
		}
		if err := secrets.Set(config.NamespaceKeyName(ns.Name), key.Secret); err != nil {
			return keyCreatedMsg{err: fmt.Errorf("save the key in the keychain: %w", err)}
		}
		t := config.TargetSettings{
			Source: config.SourceCloudLogin, Namespace: ns.Name, Address: ns.Address,
			Email: sess.Email, KeyID: key.ID, KeyName: key.Name, KeyExpires: key.Expires,
		}
		if err := config.SaveTarget(t); err != nil {
			return keyCreatedMsg{err: fmt.Errorf("save settings: %w", err)}
		}
		return keyCreatedMsg{settings: t, secret: key.Secret}
	}
}

// activationBackoff spaces checks out: every 3 s at first, up to 15 s.
func activationBackoff(attempts int) time.Duration {
	return min(time.Duration(attempts)*3*time.Second, 15*time.Second)
}

// activate checks the new key after a delay.
func (s *setupModel) activate(delay time.Duration) tea.Cmd {
	s.checkGen++
	saved, gen := s.saved, s.checkGen
	return tea.Tick(delay, func(time.Time) tea.Msg {
		t, err := config.DialSaved(saved)
		if err == nil {
			if err = config.CheckRead(context.Background(), t); err != nil {
				t.Close()
				t = nil
			}
		}
		return activationMsg{t, err, gen}
	})
}

func (s *setupModel) visibleNamespaces() []cloudauth.Namespace {
	if s.filter == "" {
		return s.namespaces
	}
	f := strings.ToLower(s.filter)
	var out []cloudauth.Namespace
	for _, ns := range s.namespaces {
		if strings.Contains(strings.ToLower(ns.Name), f) {
			out = append(out, ns)
		}
	}
	return out
}

func (s *setupModel) mood() mood {
	switch {
	case s.err != nil:
		return moodError
	case s.step == stepLogin || s.step == stepConfirmKey:
		return moodWaiting
	case s.step == stepLoading || s.step == stepActivating || s.step == stepReconnecting || s.step == stepProfile:
		return moodFetching
	}
	return moodIdle
}

func (s *setupModel) ziggyLine() string {
	if s.err != nil {
		return s.err.Error()
	}
	switch s.step {
	case stepReconnecting:
		return "Reconnecting to " + s.saved.Namespace + "."
	case stepChoose:
		return "Pick how to reach the Namespace you want to analyze. tcopt only reads it."
	case stepLogin:
		return "Waiting on you: approve the login in your browser."
	case stepLoading:
		return "Loading your Namespaces."
	case stepPickNamespace:
		return "Pick the Namespace to analyze."
	case stepConfirmKey:
		if s.renewing {
			return "Your key for " + s.chosen.Name + " expired. Waiting on you: create a new one?"
		}
		return "Waiting on you: create the API key?"
	case stepActivating:
		return "Waiting for the new key to reach the Namespace."
	case stepProfile:
		return "Connecting with your profile."
	}
	return ""
}

func (s *setupModel) View() string {
	if s.width == 0 {
		return ""
	}
	header := s.viewHeader()
	foot := footer(s.width, s.hints(), s.status)
	h := max(s.height-lipgloss.Height(header)-lipgloss.Height(foot), 6)
	body := splitBody(s.width, h, s.lines, func(w int) string {
		return ziggyPanel(w, h, s.frame, s.mood(), s.motion, s.ziggyLine())
	})
	return lipgloss.JoinVertical(lipgloss.Left, header, body, foot)
}

func (s *setupModel) viewHeader() string {
	titles := map[setupStep]string{
		stepReconnecting: "Reconnecting", stepChoose: "Connect to a Namespace", stepLogin: "Log in to Temporal Cloud",
		stepLoading: "Loading Namespaces", stepPickNamespace: "Pick a Namespace", stepConfirmKey: "Create an API key",
		stepActivating: "Activating the key", stepProfile: "Connecting",
	}
	left := [][]seg{
		{{text: "✦ ", fg: theme.Green}, {text: "TEMPORAL COST OPTIMIZER · SETUP", fg: theme.Indigo100, bold: true}},
		{{text: titles[s.step], fg: theme.Text, bold: true}},
	}
	var right []seg
	if s.mood() == moodWaiting && s.err == nil {
		right = []seg{{text: " WAITING ON YOU ", fg: theme.SpaceBlack, bg: theme.Pink, bold: true}}
	}
	return s.stars.headerBand(left, [][]seg{nil, right})
}

func (s *setupModel) hints() []hint {
	switch s.step {
	case stepChoose:
		return []hint{{"↑↓", "choose"}, {"⏎", "select"}, {"q", "quit"}}
	case stepLogin:
		return []hint{{"o", "open the browser again"}, {"esc", "cancel"}}
	case stepPickNamespace:
		return []hint{{"type", "filter"}, {"↑↓", "choose"}, {"⏎", "select"}, {"esc", "back"}}
	case stepConfirmKey:
		return []hint{{"⏎", "create key"}, {"esc", "back"}}
	case stepActivating:
		return []hint{{"r", "check now"}, {"esc", "stop waiting"}}
	}
	return []hint{{"esc", "cancel"}}
}

func (s *setupModel) lines(width int) []string {
	var out []string
	if s.err != nil {
		for _, l := range wrap(s.err.Error(), width-4) {
			out = append(out, theme.ErrorLine.Render(l))
		}
		out = append(out, "")
	}
	text := func(str string) []string {
		var ls []string
		for _, l := range wrap(str, width-4) {
			ls = append(ls, theme.Body.Render(l))
		}
		return ls
	}
	muted := func(str string) []string {
		var ls []string
		for _, l := range wrap(str, width-6) {
			ls = append(ls, "  "+theme.Muted.Render(l))
		}
		return ls
	}

	switch s.step {
	case stepReconnecting:
		out = append(out, text("Connecting to "+s.saved.Namespace+" with your saved settings.")...)
	case stepChoose:
		out = append(out, theme.Label.Render("HOW SHOULD TCOPT REACH THE NAMESPACE?"), "")
		for i, c := range s.choices {
			if i == s.cursor {
				out = append(out, theme.Selected.Render(truncate("› "+c.title, width-4)))
			} else {
				out = append(out, theme.Body.Render(truncate("  "+c.title, width-4)))
			}
			out = append(out, muted(c.detail)...)
			out = append(out, "")
		}
	case stepLogin:
		out = append(out, theme.Label.Render("LOG IN TO TEMPORAL CLOUD"), "")
		if s.pending == nil {
			out = append(out, theme.Muted.Render("Asking Temporal Cloud for a login code…"))
			break
		}
		out = append(out, text("Your browser should show the Temporal Cloud login page. Check that it shows this code, then approve:")...)
		out = append(out, "", "  "+theme.Number.Render(s.pending.UserCode), "")
		out = append(out, text("If the page didn't open, go to:")...)
		// Wrap the URL rather than cut it, so it still works when copied.
		for _, l := range strings.Split(ansi.Hardwrap(s.pending.URL, width-6, false), "\n") {
			out = append(out, "  "+theme.Eyebrow.Render(l))
		}
		left := max(time.Until(s.pending.Expires), 0)
		out = append(out, "", theme.Muted.Render(fmt.Sprintf("Waiting for approval. The code expires in %d:%02d.",
			int(left.Minutes()), int(left.Seconds())%60)))
	case stepLoading:
		out = append(out, theme.Muted.Render("Loading the Namespaces you can access…"))
	case stepPickNamespace:
		who := ""
		if s.sess != nil {
			who = " · " + s.sess.Email
		}
		out = append(out, theme.Label.Render("PICK A NAMESPACE TO ANALYZE")+theme.Muted.Render(who), "")
		out = append(out, theme.Body.Render("Filter: ")+theme.Number.Render(s.filter)+theme.Muted.Render("▏"), "")
		visible := s.visibleNamespaces()
		if len(visible) == 0 {
			out = append(out, theme.Muted.Render("No Namespaces match."))
		}
		// Keep the cursor in view.
		room := max(s.height-14, 3)
		start := max(0, s.cursor-room+1)
		for i := start; i < len(visible) && i < start+room; i++ {
			ns := visible[i]
			note := ns.Region
			if !ns.APIKeyAuth {
				note = "mTLS only"
			}
			nameW := max(width-4-len(note)-3, 10)
			row := fmt.Sprintf("%-*s  %s", nameW, truncate(ns.Name, nameW), note)
			switch {
			case i == s.cursor:
				out = append(out, theme.Selected.Render("›"+row))
			case !ns.APIKeyAuth:
				out = append(out, theme.Muted.Render(" "+row))
			default:
				out = append(out, theme.Body.Render(" "+row))
			}
		}
		out = append(out, "", theme.Muted.Render(fmt.Sprintf("%d of %d Namespaces", len(visible), len(s.namespaces))))
	case stepConfirmKey:
		out = append(out, theme.Label.Render("CREATE AN API KEY"), "")
		out = append(out, text("tcopt reads histories with an API key. It will create one with your own permissions:")...)
		out = append(out, "")
		email := ""
		if s.sess != nil {
			email = s.sess.Email
		}
		expires := time.Now().Add(keyTTL)
		for _, kv := range [][2]string{
			{"Name", cloudauth.KeyName()},
			{"Owner", email},
			{"Namespace", s.chosen.Name},
			{"Expires", fmt.Sprintf("in 7 days, %s", expires.Format("Mon Jan 2 15:04"))},
		} {
			out = append(out, "  "+theme.Muted.Render(fmt.Sprintf("%-10s", kv[0]))+theme.Body.Render(kv[1]))
		}
		out = append(out, "")
		out = append(out, text("The key goes in your OS keychain. tcopt only reads the Namespace. When the key expires, tcopt offers to create the next one.")...)
	case stepActivating:
		out = append(out, theme.Label.Render("ACTIVATING THE KEY"), "")
		if s.saved.KeyName == "" {
			out = append(out, theme.Muted.Render("Creating the API key…"))
			break
		}
		out = append(out, text(fmt.Sprintf("Created %s. A new key usually reaches the Namespace within a minute.", s.saved.KeyName))...)
		waited := time.Since(s.activeSince).Round(time.Second)
		out = append(out, "", theme.Muted.Render(fmt.Sprintf("Checked %d times over %s. tcopt keeps checking until it works.", s.attempts, waited)))
		if waited > activationSlow {
			out = append(out, "")
			out = append(out, text("This is taking longer than usual. Press r to check now, or esc to stop waiting; tcopt saved the key and will try it next time.")...)
		}
		if s.lastErr != nil {
			out = append(out, "")
			for _, l := range wrap("Last check: "+s.lastErr.Error(), width-4) {
				out = append(out, theme.Muted.Render(l))
			}
		}
	case stepProfile:
		out = append(out, theme.Muted.Render(s.status+"…"))
	}
	return out
}
