package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike/internal/frontend/tui/theme"
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
	th := theme.Default()
	th.OverlayScrim = theme.AdaptiveColor{Light: "#112233", Dark: "#112233"}
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
	th := theme.Default()
	th.OverlayScrim = theme.AdaptiveColor{Light: "#aabbcc", Dark: "#aabbcc"}
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

func TestOverlayCenterPadsScrimToFullWidth(t *testing.T) {
	// Short bg lines must not leave an un-scrimmed spill strip on the right.
	th := theme.Default()
	th.OverlayScrim = theme.AdaptiveColor{Light: "#112233", Dark: "#112233"}
	bg := "short\nnarrow"
	out := OverlayCenter(th, bg, "X", 20, 4)
	lines := strings.Split(out, "\n")
	if len(lines) != 4 {
		t.Fatalf("height = %d, want 4", len(lines))
	}
	scrim := "38;2;17;34;51"
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 20 {
			t.Errorf("row %d width = %d, want full-bleed 20", i, w)
		}
		if strings.Contains(ansi.Strip(l), "X") {
			continue
		}
		if !strings.Contains(l, scrim) {
			t.Errorf("row %d missing OverlayScrim pad: %q", i, l)
		}
	}
}

func TestOverlayCenterDoesNotBleedModalSurfaceRight(t *testing.T) {
	// Solid Dialog rows end with an open surface background (paintSurface).
	// The right scrim must not inherit that fill out to the terminal edge.
	th := theme.Default().Resolve()
	th.Surface = theme.AdaptiveColor{Light: "#112233", Dark: "#112233"}
	th.SurfaceFocus = theme.AdaptiveColor{Light: "#445566", Dark: "#445566"}
	th.OverlayScrim = theme.AdaptiveColor{Light: "#99aabb", Dark: "#99aabb"}

	const screenW, screenH = 40, 7
	mw := ModalWidth(screenW)
	fg := Dialog(th, DialogOpts{Title: "Select model", Hint: "esc", Width: mw}, "item")
	bg := strings.Repeat(strings.Repeat(".", screenW)+"\n", screenH)
	out := OverlayCenter(th, bg, fg, screenW, screenH)

	surfaceBG := "48;2;17;34;51"
	focusBG := "48;2;68;85;102"
	fgW := lipgloss.Width(fg)
	x := (screenW - fgW) / 2

	for i, line := range strings.Split(out, "\n") {
		// Surface fills belong only inside the centered dialog columns.
		// paintSurface leaves bg SGR open; without a reset the right scrim
		// inherits it all the way to the terminal edge (#284).
		right := ansi.TruncateLeft(line, x+fgW, "")
		if sgrMarkerPaintsGlyph(right, surfaceBG, focusBG) {
			t.Errorf("row %d: modal surface bled past col %d:\nplain=%q\nraw=%q",
				i, x+fgW, ansi.Strip(line), line)
		}
		// Left gutter must also stay free of modal surface wash.
		left := ansi.Truncate(line, x, "")
		if sgrMarkerPaintsGlyph(left, surfaceBG, focusBG) {
			t.Errorf("row %d: modal surface bled into left gutter:\nplain=%q\nraw=%q",
				i, ansi.Strip(line), line)
		}
	}
}

// sgrMarkerPaintsGlyph reports whether any marker SGR is open when a glyph is drawn.
func sgrMarkerPaintsGlyph(s string, markers ...string) bool {
	open := false
	for i := 0; i < len(s); {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < '@' || s[j] > '~') {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				params := s[i+2 : j]
				seq := s[i : j+1]
				if params == "" || params == "0" {
					open = false
				} else {
					for _, m := range markers {
						if strings.Contains(seq, m) {
							open = true
							break
						}
					}
				}
				i = j + 1
				continue
			}
		}
		if open {
			return true
		}
		i++
	}
	return false
}

func TestOverlayTopRight(t *testing.T) {
	th := theme.Default().Resolve()
	margin := th.Spacing.XS
	row := strings.Repeat(".", 20)
	tests := []struct {
		name    string
		bg      string
		fg      string
		width   int
		wantTop string // stripped first row; "" means bg unchanged
	}{
		{"splices right aligned", row + "\n" + row, "XYZ", 20,
			strings.Repeat(".", 20-3-margin) + "XYZ" + strings.Repeat(".", margin)},
		{"pads short row", "ab", "XY", 10,
			"ab" + strings.Repeat(" ", 10-2-margin-2) + "XY"},
		{"too wide is noop", row, strings.Repeat("X", 30), 20, ""},
		{"empty fg is noop", row, "", 20, ""},
		{"zero width is noop", row, "X", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := OverlayTopRight(th, tt.bg, tt.fg, tt.width)
			if tt.wantTop == "" {
				if out != tt.bg {
					t.Fatalf("out = %q, want bg unchanged", out)
				}
				return
			}
			lines := strings.Split(out, "\n")
			if got := ansi.Strip(lines[0]); got != tt.wantTop {
				t.Fatalf("top row = %q, want %q", got, tt.wantTop)
			}
			bgLines := strings.Split(tt.bg, "\n")
			for i := 1; i < len(bgLines); i++ {
				if lines[i] != bgLines[i] {
					t.Errorf("row %d changed: %q", i, lines[i])
				}
			}
		})
	}
}

func TestOverlayTopRightKeepsBadgeStyling(t *testing.T) {
	th := theme.Default().Resolve()
	badge := Badge(th, ToneAccent, "sent")
	bg := lipgloss.NewStyle().Foreground(th.Accent).Render(strings.Repeat("a", 30))
	out := OverlayTopRight(th, bg, badge, 30)
	if !strings.Contains(out, badge) {
		t.Fatalf("badge styling not preserved: %q", out)
	}
	if w := ansi.StringWidth(out); w != 30 {
		t.Fatalf("row width = %d, want 30", w)
	}
}
