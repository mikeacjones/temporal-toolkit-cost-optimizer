package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestKittyGridWidth(t *testing.T) {
	for _, l := range kittyGrid(kittyIDs[0], 20, 3) {
		if w := ansi.StringWidth(l); w != 20 {
			t.Errorf("placeholder row is %d cells wide, want 20", w)
		}
		if w := lipgloss.Width(l); w != 20 {
			t.Errorf("lipgloss measures %d, want 20", w)
		}
	}
}

func TestKittyPlaceSendsOnce(t *testing.T) {
	var buf bytes.Buffer
	k := &kittyImages{out: &buf}
	frames := [][]byte{bytes.Repeat([]byte{1}, 10000), {2}}
	k.place(frames, 20, 10)
	first := buf.Len()
	if !strings.Contains(buf.String(), "a=t,f=100") || !strings.Contains(buf.String(), "U=1") {
		t.Fatalf("expected a transmit and a placement, got %q", buf.String()[:80])
	}
	k.place(frames, 20, 10)
	if buf.Len() != first {
		t.Error("placing at the same size again shouldn't write anything")
	}
	k.place(frames, 24, 12)
	if strings.Count(buf.String(), "a=t,") != 2 {
		t.Error("resizing shouldn't resend the images")
	}
}
