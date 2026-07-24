package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// StatusBar renders one row of exactly width cells with left flush-left and
// right flush-right, the middle filled with spaces. When they cannot both
// fit, left is kept and right is truncated (then dropped) so the row never
// overflows. left and right may already be styled; StatusBar only measures
// and spaces them.
//
//	left := ui.Badge(th, ui.ToneAccent, "echo/echo-1")
//	right := th.S().Muted.Render("⠋ working")
//	ui.StatusBar(th, width, left, right)
func StatusBar(th theme.Theme, width int, left, right string) string {
	_ = th // reserved for future base styling; kept for API symmetry
	if width <= 0 {
		return ""
	}
	lw := lipgloss.Width(left)
	if lw >= width {
		return truncate(left, width)
	}
	rw := lipgloss.Width(right)
	if lw+1+rw <= width {
		return left + strings.Repeat(" ", width-lw-rw) + right
	}
	r := truncate(right, width-lw-1)
	gap := width - lw - lipgloss.Width(r)
	return left + strings.Repeat(" ", gap) + r
}
