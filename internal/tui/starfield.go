package tui

import (
	"math/rand/v2"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/theme"
)

type star struct {
	x     float64
	row   int
	speed float64 // cells per frame at normal speed
	glyph rune
	color string
}

// starfield drifts dots across the header band. It moves faster while
// histories download.
type starfield struct {
	stars  []star
	width  int
	rows   int
	rng    *rand.Rand
	frame  int
	stride float64
}

var starGlyphs = []rune{'·', '·', '·', '⋅', '⠂', '⠄', '✦', '✧'}

func newStarfield(rows int) *starfield {
	return &starfield{rows: rows, rng: rand.New(rand.NewPCG(7, 11)), stride: 1}
}

func (s *starfield) resize(width int) {
	if width == s.width {
		return
	}
	s.width = width
	want := width * s.rows / 9
	s.stars = s.stars[:0]
	for range want {
		s.stars = append(s.stars, s.newStar(s.rng.Float64()*float64(width)))
	}
}

func (s *starfield) newStar(x float64) star {
	colors := []string{theme.Indigo100, theme.Slate, theme.Text, theme.Indigo100}
	return star{
		x:     x,
		row:   s.rng.IntN(s.rows),
		speed: 0.04 + s.rng.Float64()*0.12,
		glyph: starGlyphs[s.rng.IntN(len(starGlyphs))],
		color: colors[s.rng.IntN(len(colors))],
	}
}

// step advances the field one frame. fast is true while fetching.
func (s *starfield) step(fast bool) {
	s.frame++
	target := 1.0
	if fast {
		target = 4.0
	}
	// Ease the speed change so it doesn't jump.
	s.stride += (target - s.stride) * 0.15
	for i := range s.stars {
		s.stars[i].x -= s.stars[i].speed * s.stride
		if s.stars[i].x < 0 {
			s.stars[i] = s.newStar(float64(s.width) - 1)
		}
	}
}

type starCell struct {
	r     rune
	color string
}

// cells returns the star glyph and color at each cell; r is 0 where empty.
func (s *starfield) cells() [][]starCell {
	out := make([][]starCell, s.rows)
	for i := range out {
		out[i] = make([]starCell, s.width)
	}
	for _, st := range s.stars {
		x := int(st.x)
		if x < 0 || x >= s.width {
			continue
		}
		out[st.row][x] = starCell{st.glyph, st.color}
	}
	return out
}

// seg is a run of header text in one foreground color. bg, when set,
// replaces the band's gradient behind it, as for a pill.
type seg struct {
	text string
	fg   string
	bg   string
	bold bool
}

// headerBand renders the header: an ultraviolet-to-black gradient with stars
// behind the text. left and right hold one line of segments per band row;
// right is aligned to the right edge. Each character gets the band's color
// at its own column, so the gradient runs unbroken behind the text.
func (s *starfield) headerBand(left, right [][]seg) string {
	bg0, bg1 := theme.Hex(theme.Ultraviolet), theme.Hex(theme.SpaceBlack)
	bgAt := func(x int) lipgloss.Color {
		t := 0.0
		if s.width > 1 {
			t = float64(x) / float64(s.width-1)
		}
		return lipgloss.Color(theme.Lerp(bg0, bg1, t*t).String())
	}
	cells := s.cells()
	var b strings.Builder
	for row := range s.rows {
		line := make([]starCell, s.width)
		bold := make([]bool, s.width)
		bgs := make([]string, s.width)
		copy(line, cells[row])
		place := func(segs []seg, x int) {
			// Keep a clear cell on each side so stars don't touch the text.
			for _, cx := range []int{x - 1, x + segsWidth(segs)} {
				if cx >= 0 && cx < s.width {
					line[cx] = starCell{}
				}
			}
			for _, sg := range segs {
				for _, r := range sg.text {
					if x >= 0 && x < s.width {
						line[x] = starCell{r, sg.fg}
						bold[x] = sg.bold
						bgs[x] = sg.bg
					}
					x++
				}
			}
		}
		if row < len(left) {
			place(left[row], 1)
		}
		if row < len(right) {
			place(right[row], s.width-1-segsWidth(right[row]))
		}
		for x, c := range line {
			st := lipgloss.NewStyle().Background(bgAt(x))
			if bgs[x] != "" {
				st = st.Background(lipgloss.Color(bgs[x]))
			}
			if c.r == 0 {
				b.WriteString(st.Render(" "))
				continue
			}
			b.WriteString(st.Foreground(lipgloss.Color(c.color)).Bold(bold[x]).Render(string(c.r)))
		}
		if row < s.rows-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func segsWidth(segs []seg) int {
	n := 0
	for _, sg := range segs {
		n += lipgloss.Width(sg.text)
	}
	return n
}
