package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// truncate shortens s to at most width display cells, appending an ellipsis
// when it must cut. ANSI- and wide-rune-aware; width <= 0 yields "".
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// padRight fits s to exactly width display cells: truncated (with an ellipsis)
// when wider, space-padded when narrower. The result always measures width,
// which is what makes panels and rows rectangular.
func padRight(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = truncate(s, width)
	if gap := width - lipgloss.Width(s); gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}

// wrapText hard-wraps s to width cells. It is a convenience for card/panel
// bodies; Panel still truncates every line, so width-safety never depends on
// the wrap being perfect.
func wrapText(s string, width int) string {
	if width < 1 {
		return s
	}
	return lipgloss.NewStyle().Width(width).Render(s)
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
	if th.Icons.Cursor == "" {
		return theme.DefaultIcons()
	}
	return th.Icons
}
