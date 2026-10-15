package tui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Kitty graphics with Unicode placeholders. tcopt sends each Ziggy frame to
// the terminal once, then draws it by printing placeholder characters in
// ordinary text cells. Bubble Tea lays those out like any other text, so the
// image moves and clips with the panel. Ghostty and Kitty support this.
//
// https://sw.kovidgoyal.net/kitty/graphics-protocol/#unicode-placeholders

const placeholder = '\U0010EEEE'

// Image IDs for the two frames. The placeholder's foreground color carries
// the ID, as 24-bit RGB.
var kittyIDs = []uint32{0x7C0F01, 0x7C0F02}

// kittySupported reports whether the terminal draws Kitty Unicode
// placeholders. TCOPT_IMAGES=off turns images off, and =kitty forces them on.
func kittySupported() bool {
	switch os.Getenv("TCOPT_IMAGES") {
	case "off", "0", "false":
		return false
	case "kitty":
		return true
	}
	// tmux and screen don't pass the protocol through by default.
	if os.Getenv("TMUX") != "" || strings.HasPrefix(os.Getenv("TERM"), "screen") {
		return false
	}
	return os.Getenv("TERM_PROGRAM") == "ghostty" || os.Getenv("TERM") == "xterm-ghostty" ||
		os.Getenv("TERM") == "xterm-kitty" || os.Getenv("KITTY_WINDOW_ID") != ""
}

// kittyImages tracks what the terminal has been sent.
type kittyImages struct {
	mu         sync.Mutex
	out        io.Writer
	sent       bool
	cols, rows int
}

var kitty = &kittyImages{out: os.Stdout}

// place makes sure the frames are on the terminal and sized to cols×rows
// cells. It writes straight to the terminal, outside Bubble Tea's renderer,
// and only when something changed.
func (k *kittyImages) place(frames [][]byte, cols, rows int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var b bytes.Buffer
	if !k.sent {
		for i, png := range frames {
			writeKittyTransmit(&b, kittyIDs[i], png)
		}
		k.sent = true
	}
	if cols != k.cols || rows != k.rows {
		for i := range frames {
			// A virtual placement for placeholders; a new one replaces the old.
			fmt.Fprintf(&b, "\x1b_Ga=p,U=1,i=%d,p=1,c=%d,r=%d,q=2\x1b\\", kittyIDs[i], cols, rows)
		}
		k.cols, k.rows = cols, rows
	}
	if b.Len() > 0 {
		_, _ = k.out.Write(b.Bytes())
	}
}

// clear deletes the frames from the terminal's memory when tcopt exits.
func (k *kittyImages) clear() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.sent {
		return
	}
	for _, id := range kittyIDs {
		fmt.Fprintf(k.out, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id)
	}
	k.sent, k.cols, k.rows = false, 0, 0
}

// writeKittyTransmit sends a PNG in base64 chunks of at most 4096 bytes.
func writeKittyTransmit(b *bytes.Buffer, id uint32, png []byte) {
	data := base64.StdEncoding.EncodeToString(png)
	for first := true; len(data) > 0; first = false {
		n := min(len(data), 4096)
		chunk := data[:n]
		data = data[n:]
		more := 0
		if len(data) > 0 {
			more = 1
		}
		if first {
			fmt.Fprintf(b, "\x1b_Ga=t,f=100,t=d,i=%d,q=2,m=%d;%s\x1b\\", id, more, chunk)
		} else {
			fmt.Fprintf(b, "\x1b_Gm=%d;%s\x1b\\", more, chunk)
		}
	}
}

// kittyGrid returns rows of placeholder cells that draw image id across
// cols×rows cells.
func kittyGrid(id uint32, cols, rows int) []string {
	color := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", id>>16&0xff, id>>8&0xff, id&0xff)
	out := make([]string, rows)
	for r := range rows {
		var b strings.Builder
		b.WriteString(color)
		for c := range cols {
			b.WriteRune(placeholder)
			b.WriteRune(placeholderDiacritics[r])
			b.WriteRune(placeholderDiacritics[c])
		}
		b.WriteString("\x1b[39m")
		out[r] = b.String()
	}
	return out
}
