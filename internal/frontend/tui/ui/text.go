package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/common"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

// truncate shortens s to at most width display cells, appending an ellipsis
// when it must cut. ANSI- and wide-rune-aware; width <= 0 yields "".
// Wide-neutral historic scripts are padded first so measured width matches
// common double-cell terminal paint (#689).
func truncate(th theme.Theme, s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = common.PadWideGlyphs(s)
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, resolveIcons(th).Ellipsis)
}

// padRight fits s to exactly width display cells: truncated (with an ellipsis)
// when wider, space-padded when narrower. The result always measures width,
// which is what makes panels and rows rectangular.
func padRight(th theme.Theme, s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = truncate(th, s, width)
	if gap := width - lipgloss.Width(s); gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}

// WrapText word-wraps s to width display cells, preferring breaks at spaces so
// words are not split mid-token across lines. Tokens longer than width are
// hard-broken. Trailing pad spaces from the layout engine are stripped so
// callers can indent without overflowing the budget. width < 1 returns s
// unchanged. Panel still truncates every line as a width-safety net.
//
// Wide-neutral historic scripts (e.g. Egyptian Hieroglyphs) are padded before
// wrapping so lipgloss width matches double-cell terminal glyphs (#689).
func WrapText(s string, width int) string {
	if width < 1 {
		return s
	}
	s = common.PadWideGlyphs(s)
	out := lipgloss.NewStyle().Width(width).Render(s)
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

// wrapText is the unexported alias used inside this package.
func wrapText(s string, width int) string {
	return WrapText(s, width)
}

// clamp constrains v to [lo, hi]; if hi < lo it returns lo.
func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return min(max(v, lo), hi)
}

// lineCount is the number of lines a rendered block occupies.
func lineCount(s string) int {
	return strings.Count(s, "\n") + 1
}

// resolveIcons returns th.Icons, falling back to theme.DefaultIcons() for a
// zero-value theme so components stay usable without a configured theme.
func resolveIcons(th theme.Theme) theme.Icons {
	return th.Resolve().Icons
}
