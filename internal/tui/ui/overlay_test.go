package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestOverlayCenterCentersForeground(t *testing.T) {
	bgLine := strings.Repeat(" ", 20)
	bg := strings.Join([]string{bgLine, bgLine, bgLine, bgLine, bgLine}, "\n")
	out := OverlayCenter(bg, "XXX", 20, 5)

	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("height = %d lines, want 5", len(lines))
	}
	mid := lines[2]
	if idx := strings.Index(mid, "XXX"); idx != 8 { // (20-3)/2
		t.Errorf("foreground at col %d, want 8: %q", idx, mid)
	}
	if w := lipgloss.Width(mid); w != 20 {
		t.Errorf("composited row width = %d, want 20", w)
	}
	for i, l := range lines {
		if i == 2 {
			continue
		}
		if strings.Contains(l, "X") {
			t.Errorf("foreground bled into row %d: %q", i, l)
		}
	}
}

func TestOverlayCenterPadsBackgroundToHeight(t *testing.T) {
	out := OverlayCenter("a", "Z", 10, 5)
	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("height = %d, want padded to 5", len(lines))
	}
	mid := lines[2]                               // y = (5-1)/2
	if idx := strings.Index(mid, "Z"); idx != 4 { // (10-1)/2
		t.Errorf("foreground at col %d, want 4: %q", idx, mid)
	}
}

func TestOverlayCenterKeepsAnsiBalancedAndWidthCorrect(t *testing.T) {
	// A background line carrying raw SGR escapes must not shift the box: the
	// compositor cuts by display width, not byte offset.
	raw := "\x1b[31m" + strings.Repeat("-", 20) + "\x1b[0m"
	out := OverlayCenter(raw, "XXX", 20, 1)

	plain := ansi.Strip(out)
	if idx := strings.Index(plain, "XXX"); idx != 8 {
		t.Errorf("foreground at col %d after stripping ANSI, want 8: %q", idx, plain)
	}
	if w := lipgloss.Width(out); w != 20 {
		t.Errorf("composited width = %d, want 20 despite escape codes", w)
	}
	// The colored background survives on both sides of the box.
	if !strings.Contains(out, "\x1b[31m") {
		t.Error("background color escape was dropped")
	}
}

func TestModalWidthCapsAndMargins(t *testing.T) {
	if got := ModalWidth(200); got != 72 {
		t.Errorf("ModalWidth(200) = %d, want cap 72", got)
	}
	if got := ModalWidth(40); got != 36 {
		t.Errorf("ModalWidth(40) = %d, want 36 (screen-4)", got)
	}
}
