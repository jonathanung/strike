package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestSparklineEmptyIsHollowNotZero(t *testing.T) {
	th := theme.Default()
	got := ansi.Strip(Sparkline(th, 8, nil))
	want := strings.Repeat(th.Resolve().Icons.MeterEmpty, 8)
	if got != want {
		t.Errorf("empty samples = %q, want hollow %q", got, want)
	}
	got = ansi.Strip(Sparkline(th, 8, []float64{}))
	if got != want {
		t.Errorf("zero-len samples = %q, want hollow %q", got, want)
	}
}

func TestSparklineWidthSafe(t *testing.T) {
	th := theme.Default()
	for _, width := range []int{1, 4, 12, 32} {
		out := Sparkline(th, width, []float64{1, 3, 2, 8, 0, 5})
		if got := lipgloss.Width(out); got != width {
			t.Errorf("width %d: lipgloss.Width = %d\n%s", width, got, ansi.Strip(out))
		}
	}
	if Sparkline(th, 0, []float64{1}) != "" {
		t.Error("width 0 should return empty")
	}
	if Sparkline(th, -1, []float64{1}) != "" {
		t.Error("negative width should return empty")
	}
}

func TestSparklineUsesThemeGlyphs(t *testing.T) {
	th := theme.Default()
	th.Icons.Sparkline = "12345678"
	th.Icons.MeterEmpty = "."
	th = th.Resolve()
	out := ansi.Strip(Sparkline(th, 4, []float64{1, 2, 3, 4}))
	for _, r := range out {
		if r < '1' || r > '8' {
			t.Errorf("unexpected glyph %q in %q", r, out)
		}
	}
	empty := ansi.Strip(Sparkline(th, 3, nil))
	if empty != "..." {
		t.Errorf("empty with custom MeterEmpty = %q, want ...", empty)
	}
}

func TestSparklineMeasuredZeroUsesFloorGlyph(t *testing.T) {
	th := theme.Default()
	th.Icons.Sparkline = "abcdefgh"
	th = th.Resolve()
	out := ansi.Strip(Sparkline(th, 4, []float64{0, 0, 0, 0}))
	if out != "aaaa" {
		t.Errorf("all-zero series = %q, want floor glyph aaaa (measured zero, not unknown)", out)
	}
}
