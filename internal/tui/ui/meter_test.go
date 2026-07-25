package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1k"},
		{1500, "1.5k"},
		{9999, "9.9k"},
		{10_000, "10k"},
		{215_000, "215k"},
		{1_000_000, "1M"},
		{1_500_000, "1.5M"},
		{10_000_000, "10M"},
		{-5, "0"},
	}
	for _, tt := range tests {
		if got := FormatTokens(tt.n); got != tt.want {
			t.Errorf("FormatTokens(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestMeterWidthSafety(t *testing.T) {
	th := theme.Default()
	if got := Meter(th, 0, 0.5); got != "" {
		t.Errorf("width 0 = %q, want empty", got)
	}
	if got := Meter(th, -3, 0.5); got != "" {
		t.Errorf("negative width = %q, want empty", got)
	}

	// Unknown ratio draws a full-width hollow bar (no fill glyphs).
	emptyGlyph := th.Resolve().Icons.MeterEmpty
	fillGlyph := th.Resolve().Icons.MeterFill
	unknown := ansi.Strip(Meter(th, 8, -1))
	if ansi.StringWidth(unknown) != 8 {
		t.Errorf("unknown meter display width = %d, want 8 (%q)", ansi.StringWidth(unknown), unknown)
	}
	if want := strings.Repeat(emptyGlyph, 8); unknown != want {
		t.Errorf("unknown meter = %q, want fully hollow %q", unknown, want)
	}
	if fillGlyph != "" && strings.Contains(unknown, fillGlyph) {
		t.Errorf("unknown meter contains fill glyph %q: %q", fillGlyph, unknown)
	}

	// Clamped full and empty ratios stay width-safe.
	for _, ratio := range []float64{0, 0.5, 0.75, 0.95, 1, 2} {
		got := ansi.Strip(Meter(th, 10, ratio))
		if w := ansi.StringWidth(got); w != 10 {
			t.Errorf("Meter(10, %v) display width = %d, want 10 (%q)", ratio, w, got)
		}
	}

	// Glyphs come from the theme icons.
	th.Icons.MeterFill = "X"
	th.Icons.MeterEmpty = "-"
	custom := ansi.Strip(Meter(th, 4, 0.5))
	if !strings.Contains(custom, "X") || !strings.Contains(custom, "-") {
		t.Errorf("custom meter glyphs not used: %q", custom)
	}
}
