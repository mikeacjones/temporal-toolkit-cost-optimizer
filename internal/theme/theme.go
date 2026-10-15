// Package theme holds the buildkit's color tokens as Lip Gloss styles. Lip
// Gloss detects the terminal's color support and falls back from truecolor
// to 256 colors to 16, and honors NO_COLOR.
package theme

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// Buildkit tokens (assets/css/temporal-theme.css).
const (
	SpaceBlack  = "#141414"
	Ultraviolet = "#444ce7"
	UVHover     = "#6169ed"
	Indigo100   = "#cacbf9"
	Green       = "#1ff1a5"
	Yellow      = "#fecb2f"
	Pink        = "#ff6bff"
	Slate       = "#92a4c3"
	Border      = "#243349"
	Red         = "#ff5555"
	Text        = "#f8fafc"
)

var (
	// Eyebrow is the mono uppercase label line in the header.
	Eyebrow = lipgloss.NewStyle().Foreground(lipgloss.Color(Indigo100))
	// Label is a panel's uppercase title.
	Label = lipgloss.NewStyle().Foreground(lipgloss.Color(Indigo100)).Bold(true)
	// Body is regular text.
	Body = lipgloss.NewStyle().Foreground(lipgloss.Color(Text))
	// Muted is secondary text.
	Muted = lipgloss.NewStyle().Foreground(lipgloss.Color(Slate))
	// Number is a figure the screen is about.
	Number = lipgloss.NewStyle().Foreground(lipgloss.Color(Green)).Bold(true)
	// ErrorLine is the only red element: failed calls and stopped Workers.
	ErrorLine = lipgloss.NewStyle().Foreground(lipgloss.Color(Red))
	// Selected marks the focused row.
	Selected = lipgloss.NewStyle().Foreground(lipgloss.Color(Text)).Background(lipgloss.Color(Border)).Bold(true)

	// Panel is a square, flat card.
	Panel = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color(Border)).Padding(0, 1)
	// FocusedPanel uses the buildkit's focus color.
	FocusedPanel = Panel.BorderForeground(lipgloss.Color(Green))

	// Key is a key hint in the footer.
	Key = lipgloss.NewStyle().Foreground(lipgloss.Color(Green)).Bold(true)
)

// Pill is a green status pill with black uppercase text, such as " ANALYZING ".
func Pill(text string) string {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(SpaceBlack)).
		Background(lipgloss.Color(Green)).
		Bold(true).
		Render(" " + text + " ")
}

// PendingPill is the one pink element: an approval waiting on the user.
func PendingPill(text string) string {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(SpaceBlack)).
		Background(lipgloss.Color(Pink)).
		Bold(true).
		Render(" " + text + " ")
}

// Badge renders a confidence badge. The word always shows, so color never
// carries the meaning alone.
func Badge(confidence string) string {
	switch confidence {
	case "high":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(Green)).Render("[HIGH]")
	case "medium":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(Yellow)).Render("[MED]")
	case "advisory":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(Slate)).Render("[ADV]")
	case "agent":
		return lipgloss.NewStyle().Foreground(lipgloss.Color(Indigo100)).Render("[AGENT]")
	}
	return fmt.Sprintf("[%s]", confidence)
}

// RGB is a color as 8-bit channels.
type RGB struct{ R, G, B uint8 }

// Hex parses "#rrggbb".
func Hex(s string) RGB {
	var c RGB
	_, _ = fmt.Sscanf(s, "#%02x%02x%02x", &c.R, &c.G, &c.B)
	return c
}

// String returns "#rrggbb".
func (c RGB) String() string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// Lerp blends from a to b; t runs from 0 to 1.
func Lerp(a, b RGB, t float64) RGB {
	mix := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return RGB{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B)}
}
