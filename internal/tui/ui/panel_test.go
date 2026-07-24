package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	return lines[len(lines)-1]
}

func TestPanelEmbedsTitleInTopBorder(t *testing.T) {
	out := Panel(theme.Default(), PanelOpts{Title: "session", Width: 40}, "body")
	top := firstLine(out)
	if !strings.HasPrefix(top, "╭─ session ") {
		t.Errorf("top border does not embed the title: %q", top)
	}
	if !strings.HasSuffix(top, "╮") {
		t.Errorf("top border does not close with ╮: %q", top)
	}
	if !strings.Contains(top, "─╮") {
		t.Errorf("title is not followed by a border rule: %q", top)
	}
	if w := lipgloss.Width(top); w != 40 {
		t.Errorf("top border width = %d, want 40", w)
	}
}

func TestPanelEmbedsFooterInBottomBorder(t *testing.T) {
	out := Panel(theme.Default(), PanelOpts{Title: "keys", Footer: "esc close", Width: 40}, "body")
	bottom := lastLine(out)
	if !strings.HasPrefix(bottom, "╰─ esc close ") {
		t.Errorf("bottom border does not embed the footer: %q", bottom)
	}
	if !strings.HasSuffix(bottom, "╯") {
		t.Errorf("bottom border does not close with ╯: %q", bottom)
	}
	if w := lipgloss.Width(bottom); w != 40 {
		t.Errorf("bottom border width = %d, want 40", w)
	}
}

func TestPanelRendersExactlyRequestedWidth(t *testing.T) {
	body := "the quick brown fox jumps over the lazy dog, then keeps on going for a while"
	for _, width := range []int{3, 4, 5, 6, 8, 12, 24, 40, 80, 120} {
		out := Panel(theme.Default(), PanelOpts{Title: "session", Footer: "esc", Width: width}, body)
		for i, line := range strings.Split(out, "\n") {
			if w := lipgloss.Width(line); w != width {
				t.Errorf("width %d: line %d = %d cells, want exactly %d\n%q", width, i, w, width, line)
			}
		}
	}
}

func TestPanelNeverExceedsWidthWithWideRunesAndLongTitle(t *testing.T) {
	body := strings.Repeat("界", 60)
	title := "an exceedingly long panel title that must be truncated to fit"
	for _, width := range []int{6, 10, 20, 30} {
		out := Panel(theme.Default(), PanelOpts{Title: title, Width: width}, body)
		if w := lipgloss.Width(out); w != width {
			t.Errorf("width %d: rendered width = %d, want %d", width, w, width)
		}
	}
}

func TestPanelDegradesAtTinyWidthsWithoutPanic(t *testing.T) {
	for _, width := range []int{0, 1, 2} {
		out := Panel(theme.Default(), PanelOpts{Title: "x", Width: width}, "content")
		if w := lipgloss.Width(out); w > max(width, 0) {
			t.Errorf("width %d: rendered width = %d, want <= %d", width, w, max(width, 0))
		}
		if strings.Contains(out, "╭") {
			t.Errorf("width %d should be too narrow for a border, got %q", width, out)
		}
	}
}

func TestPanelFixedHeightClampsAndPads(t *testing.T) {
	tall := strings.Join([]string{"1", "2", "3", "4", "5", "6"}, "\n")
	clamped := Panel(theme.Default(), PanelOpts{Title: "t", Width: 20, Height: 5}, tall)
	if got := lineCount(clamped); got != 5 {
		t.Errorf("clamped height = %d lines, want 5", got)
	}
	padded := Panel(theme.Default(), PanelOpts{Title: "t", Width: 20, Height: 8}, "just one line")
	if got := lineCount(padded); got != 8 {
		t.Errorf("padded height = %d lines, want 8", got)
	}
	for i, line := range strings.Split(padded, "\n") {
		if w := lipgloss.Width(line); w != 20 {
			t.Errorf("padded line %d width = %d, want 20", i, w)
		}
	}
}

func TestPanelBorderColorSelectionPrecedence(t *testing.T) {
	th := theme.Default()
	tests := []struct {
		name string
		opts PanelOpts
		want lipgloss.AdaptiveColor
	}{
		{"default", PanelOpts{}, th.Border},
		{"focused", PanelOpts{Focused: true}, th.BorderFocus},
		{"dim", PanelOpts{Dim: true}, th.BorderMuted},
		{"tone override", PanelOpts{Tone: ToneWarning}, th.Warning},
		{"tone beats focus", PanelOpts{Focused: true, Tone: ToneError}, th.Error},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := panelBorderColor(th, tt.opts); got != tt.want {
				t.Errorf("panelBorderColor = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPanelFocusStateChangesRenderedBorder(t *testing.T) {
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(saved) })

	th := theme.Default()
	focused := Panel(th, PanelOpts{Title: "x", Width: 24, Focused: true}, "body")
	unfocused := Panel(th, PanelOpts{Title: "x", Width: 24}, "body")
	dim := Panel(th, PanelOpts{Title: "x", Width: 24, Dim: true}, "body")

	if focused == unfocused {
		t.Error("focused and unfocused panels render identically; border color not applied")
	}
	if unfocused == dim {
		t.Error("dim and normal panels render identically; BorderMuted not applied")
	}
	for name, out := range map[string]string{"focused": focused, "unfocused": unfocused, "dim": dim} {
		if w := lipgloss.Width(out); w != 24 {
			t.Errorf("%s panel width = %d with color enabled, want 24", name, w)
		}
	}
}

func TestInnerWidthAccountsForBorderAndPadding(t *testing.T) {
	tests := map[int]int{0: 0, 2: 2, 4: 2, 5: 3, 6: 2, 40: 36, 80: 76}
	for width, want := range tests {
		if got := InnerWidth(width); got != want {
			t.Errorf("InnerWidth(%d) = %d, want %d", width, got, want)
		}
	}
}
