package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestOverlayCenterCentersForeground(t *testing.T) {
	bgLine := strings.Repeat(" ", 20)
	bg := strings.Join([]string{bgLine, bgLine, bgLine, bgLine, bgLine}, "\n")
	out := OverlayCenter(theme.Default(), bg, "XXX", 20, 5)

	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("height = %d lines, want 5", len(lines))
	}
	mid := lines[2]
	if idx := strings.Index(ansi.Strip(mid), "XXX"); idx != 8 { // (20-3)/2
		t.Errorf("foreground at col %d, want 8: %q", idx, mid)
	}
	if w := lipgloss.Width(mid); w != 20 {
		t.Errorf("composited row width = %d, want 20", w)
	}
	for i, l := range lines {
		if i == 2 {
			continue
		}
		if strings.Contains(ansi.Strip(l), "X") {
			t.Errorf("foreground bled into row %d: %q", i, l)
		}
	}
}

func TestOverlayCenterPadsBackgroundToHeight(t *testing.T) {
	out := OverlayCenter(theme.Default(), "a", "Z", 10, 5)
	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("height = %d, want padded to 5", len(lines))
	}
	mid := lines[2]                                           // y = (5-1)/2
	if idx := strings.Index(ansi.Strip(mid), "Z"); idx != 4 { // (10-1)/2
		t.Errorf("foreground at col %d, want 4: %q", idx, mid)
	}
}

func TestOverlayCenterKeepsAnsiBalancedAndWidthCorrect(t *testing.T) {
	// A background line carrying raw SGR escapes must not shift the box: the
	// compositor cuts by display width, not byte offset. Scrim strips bg ANSI
	// first, so width stays stable under the modal.
	raw := "\x1b[31m" + strings.Repeat("-", 20) + "\x1b[0m"
	out := OverlayCenter(theme.Default(), raw, "XXX", 20, 1)

	plain := ansi.Strip(out)
	if idx := strings.Index(plain, "XXX"); idx != 8 {
		t.Errorf("foreground at col %d after stripping ANSI, want 8: %q", idx, plain)
	}
	if w := lipgloss.Width(out); w != 20 {
		t.Errorf("composited width = %d, want 20 despite escape codes", w)
	}
}

func TestOverlayCenterScrimsBackgroundNotForeground(t *testing.T) {
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(saved) })

	th := theme.Default()
	th.OverlayScrim = lipgloss.AdaptiveColor{Light: "#112233", Dark: "#112233"}
	bgLine := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Render(strings.Repeat("B", 20))
	bg := strings.Join([]string{bgLine, bgLine, bgLine, bgLine, bgLine}, "\n")
	fg := lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00")).Render("MODAL")
	out := OverlayCenter(th, bg, fg, 20, 5)

	scrim := "38;2;17;34;51"
	green := "38;2;0;255;0"
	red := "38;2;255;0;0"
	if !strings.Contains(out, scrim) {
		t.Errorf("background was not recolored with OverlayScrim: %q", out)
	}
	if !strings.Contains(out, green) {
		t.Errorf("foreground lost its color under the scrim: %q", out)
	}
	if strings.Contains(out, red) {
		t.Errorf("background red survived scrim: %q", out)
	}
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "MODAL") {
		t.Errorf("modal text missing: %q", plain)
	}
	// Non-modal rows are fully scrimmed glyphs.
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if strings.Contains(ansi.Strip(l), "MODAL") {
			continue
		}
		if !strings.Contains(l, scrim) {
			t.Errorf("row %d missing scrim: %q", i, l)
		}
	}
}

func TestScrimEmptyAndPreservesPlainText(t *testing.T) {
	if got := Scrim(theme.Default(), ""); got != "" {
		t.Errorf("Scrim empty = %q", got)
	}
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(saved) })

	th := theme.Default()
	th.OverlayScrim = lipgloss.AdaptiveColor{Light: "#aabbcc", Dark: "#aabbcc"}
	out := Scrim(th, "hello\nworld")
	if plain := ansi.Strip(out); plain != "hello\nworld" {
		t.Errorf("Scrim plain = %q, want hello\\nworld", plain)
	}
	if !strings.Contains(out, "38;2;170;187;204") {
		t.Errorf("Scrim missing OverlayScrim SGR: %q", out)
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
