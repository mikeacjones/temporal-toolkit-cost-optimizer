package tui

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/theme"
)

// ziggyPNGs holds the eyes-open and blink frames. Builds without the `brand`
// tag leave it empty and show no Ziggy, until brand approves shipping the
// mascot.
var ziggyPNGs [][]byte

// mood is Ziggy's state, which sets the glow and the bob speed.
type mood int

const (
	moodIdle mood = iota
	moodFetching
	moodFound
	moodWaiting
	moodError
)

func (m mood) glow() string {
	switch m {
	case moodFetching, moodFound:
		return theme.Green
	case moodWaiting:
		return theme.Pink
	}
	return ""
}

// Ziggy's size limits, in cells.
const (
	ziggyMaxCols = 30
	ziggyMinRows = 4
)

var (
	decodeOnce sync.Once
	frames     []*image.NRGBA
)

func ziggyFrames() []*image.NRGBA {
	decodeOnce.Do(func() {
		for _, b := range ziggyPNGs {
			img, err := png.Decode(bytes.NewReader(b))
			if err != nil {
				frames = nil
				return
			}
			n := image.NewNRGBA(img.Bounds())
			for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
				for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
					n.Set(x, y, img.At(x, y))
				}
			}
			frames = append(frames, n)
		}
	})
	return frames
}

// ziggySize picks the largest sprite that fits maxCols×maxRows cells. A
// cell is about twice as tall as it is wide.
func ziggySize(maxCols, maxRows int) (cols, rows int) {
	fs := ziggyFrames()
	if len(fs) == 0 {
		return 0, 0
	}
	w, h := fs[0].Bounds().Dx(), fs[0].Bounds().Dy()
	cols = min(maxCols, ziggyMaxCols)
	rows = (cols*h/w + 1) / 2
	if rows > maxRows {
		rows = maxRows
		cols = rows * 2 * w / h
	}
	if rows < ziggyMinRows {
		return 0, 0
	}
	return cols, rows
}

// blinking reports whether this frame shows Ziggy's eyes closed: a short
// blink about every 10 s, and a double blink later in the cycle. Ticks come
// about 8 times a second.
func blinking(frame int, motion bool) bool {
	if !motion {
		return false
	}
	switch frame % 80 {
	case 7, 61, 64:
		return true
	}
	return false
}

// bobbing reports whether Ziggy is at the low point of the bob, which runs
// faster while histories download.
func bobbing(frame int, md mood, motion bool) bool {
	if !motion || md == moodError {
		return false
	}
	period := 16
	if md == moodFetching {
		period = 6
	}
	return (frame/period)%2 == 1
}

// renderZiggy draws Ziggy in at most maxCols×maxRows cells. Terminals with
// Kitty graphics get the real image; others get half blocks, with a glow in
// the mood's color. It returns "" when there's no room or no color.
func renderZiggy(frame int, md mood, motion bool, maxCols, maxRows int) string {
	fs := ziggyFrames()
	if len(fs) == 0 || lipgloss.ColorProfile() > termenv.ANSI256 {
		return ""
	}
	idx := 0
	if blinking(frame, motion) && len(fs) > 1 {
		idx = 1
	}
	low := bobbing(frame, md, motion)

	if kittySupported() {
		// The bob moves a whole cell, so leave a row for it.
		cols, rows := ziggySize(maxCols, maxRows-1)
		if cols == 0 {
			return ""
		}
		kitty.place(ziggyPNGs, cols, rows)
		lines := kittyGrid(kittyIDs[idx], cols, rows)
		blank := strings.Repeat(" ", cols)
		if low {
			lines = append([]string{blank}, lines...)
		} else {
			lines = append(lines, blank)
		}
		return strings.Join(lines, "\n")
	}

	cols, rows := ziggySize(maxCols, maxRows)
	if cols == 0 {
		return ""
	}
	return halfBlocks(sample(fs[idx], cols, rows*2), md, low)
}

// pixel is one sample of the sprite; on is false where it's transparent.
type pixel struct {
	r, g, b uint8
	on      bool
}

var (
	sampleMu    sync.Mutex
	sampleCache = map[[4]int][][]pixel{}
)

// sample averages the image down to w×h pixels, weighting color by alpha.
func sample(img *image.NRGBA, w, h int) [][]pixel {
	key := [4]int{img.Rect.Dx(), int(img.Pix[len(img.Pix)/2]), w, h}
	sampleMu.Lock()
	defer sampleMu.Unlock()
	if px, ok := sampleCache[key]; ok {
		return px
	}
	b := img.Bounds()
	out := make([][]pixel, h)
	for y := range h {
		out[y] = make([]pixel, w)
		y0, y1 := b.Min.Y+y*b.Dy()/h, b.Min.Y+(y+1)*b.Dy()/h
		for x := range w {
			x0, x1 := b.Min.X+x*b.Dx()/w, b.Min.X+(x+1)*b.Dx()/w
			var r, g, bl, a, n float64
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					i := img.PixOffset(xx, yy)
					al := float64(img.Pix[i+3])
					r += float64(img.Pix[i]) * al
					g += float64(img.Pix[i+1]) * al
					bl += float64(img.Pix[i+2]) * al
					a += al
					n++
				}
			}
			if n == 0 || a/n < 128 {
				continue
			}
			out[y][x] = pixel{uint8(r / a), uint8(g / a), uint8(bl / a), true}
		}
	}
	sampleCache[key] = out
	return out
}

// halfBlocks draws pixel rows in pairs with ▀ and ▄. low shifts the sprite
// down one pixel for the bob.
func halfBlocks(sp [][]pixel, md mood, low bool) string {
	shift := 0
	if low {
		shift = 1
	}
	glowColor := md.glow()
	dim := ""
	if glowColor != "" {
		dim = theme.Lerp(theme.Hex(theme.SpaceBlack), theme.Hex(glowColor), 0.35).String()
	}
	on := func(y, x int) bool {
		return y >= 0 && y < len(sp) && x >= 0 && x < len(sp[y]) && sp[y][x].on
	}
	glowAt := func(y, x int) bool {
		if glowColor == "" {
			return false
		}
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if on(y+dy, x+dx) {
					return true
				}
			}
		}
		return false
	}
	color := func(y, x int) (string, bool) {
		y -= shift
		if on(y, x) {
			p := sp[y][x]
			c := theme.RGB{R: p.r, G: p.g, B: p.b}
			if md == moodError {
				// Partly gray when something is offline.
				c = theme.Lerp(c, theme.Hex(theme.Slate), 0.5)
			}
			return c.String(), true
		}
		if glowAt(y, x) {
			return dim, true
		}
		return "", false
	}

	var b strings.Builder
	width, rows := len(sp[0]), len(sp)/2
	for row := range rows {
		for x := range width {
			tc, ton := color(row*2, x)
			bc, bon := color(row*2+1, x)
			switch {
			case ton && bon:
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(tc)).Background(lipgloss.Color(bc)).Render("▀"))
			case ton:
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(tc)).Render("▀"))
			case bon:
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(bc)).Render("▄"))
			default:
				b.WriteByte(' ')
			}
		}
		if row < rows-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
