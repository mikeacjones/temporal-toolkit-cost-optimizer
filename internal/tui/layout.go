package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/theme"
)

// ziggyWidth is the width of the right-hand panel where Ziggy talks.
const ziggyWidth = 36

// hint is one key hint in the footer.
type hint struct{ key, label string }

// footer renders key hints on the left and the latest acknowledgment on the
// right.
func footer(width int, hints []hint, status string) string {
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		parts = append(parts, theme.Key.Render(h.key)+" "+theme.Muted.Render(h.label))
	}
	left := " " + strings.Join(parts, "  ")
	right := theme.Muted.Render(status + " ")
	// Stop one column short: writing the bottom-right cell makes some
	// terminals scroll.
	gap := max(width-1-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return left + strings.Repeat(" ", gap) + right
}

// ziggyPanel renders Ziggy and their line in a panel of the given outer
// size. Ziggy is left out when there isn't room.
func ziggyPanel(width, height, frame int, md mood, motion bool, line string) string {
	inner := height - 2
	say := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Text)).Width(width - 4).Render("“" + line + "”")
	if md == moodError {
		say = theme.ErrorLine.Width(width - 4).Render(line)
	}
	content := say
	if zig := renderZiggy(frame, md, motion, width-4, inner-1-lipgloss.Height(say)); zig != "" {
		content = lipgloss.JoinVertical(lipgloss.Center, zig, "", say)
	}
	content = strings.Join(clip(strings.Split(content, "\n"), inner), "\n")
	return theme.Panel.Width(width - 2).Height(inner).Align(lipgloss.Center).Render(content)
}

// mainPanel renders the focused left-hand panel with its lines clipped to
// fit.
func mainPanel(width, height int, lines []string) string {
	inner := height - 2
	return theme.FocusedPanel.Width(width - 2).Height(inner).Render(strings.Join(clip(lines, inner), "\n"))
}

// splitBody lays out the main panel and Ziggy's panel side by side, or the
// main panel alone in a narrow terminal.
func splitBody(width, height int, left func(w int) []string, right func(w int) string) string {
	leftW := width - ziggyWidth
	if leftW < 40 {
		return mainPanel(width, height, left(width))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, mainPanel(leftW, height, left(leftW)), right(ziggyWidth))
}
