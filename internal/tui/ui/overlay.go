package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// Scrim de-emphasizes a rendered frame for modal backgrounds. Each line is
// stripped of ANSI and recolored with the theme OverlayScrim token so contrast
// drops cheaply without re-layout or re-markdown.
func Scrim(th theme.Theme, s string) string {
	if s == "" {
		return s
	}
	th = th.Resolve()
	style := lipgloss.NewStyle().Foreground(th.OverlayScrim)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = style.Render(ansi.Strip(line))
	}
	return strings.Join(lines, "\n")
}

// OverlayCenter composites fg (a rendered box) over bg, centered on a
// width×height screen. The background is first de-emphasized with Scrim so
// open modals read as a polished overlay rather than a hard cutout. It is
// ANSI-aware: each background line is cut around the box with escape sequences
// kept balanced, so background styling never bleeds into the modal.
//
//	screen := ui.OverlayCenter(th, baseView, ui.Dialog(th, opts, body), width, height)
//
// fg is placed at the center; if the screen is smaller than fg it is pinned to
// the top-left and clipped by the terminal.
func OverlayCenter(th theme.Theme, bg, fg string, width, height int) string {
	bg = Scrim(th, bg)
	bgLines := strings.Split(bg, "\n")
	for len(bgLines) < height {
		bgLines = append(bgLines, "")
	}
	fgLines := strings.Split(fg, "\n")
	fgWidth := lipgloss.Width(fg)
	x := max(0, (width-fgWidth)/2)
	y := max(0, (height-len(fgLines))/2)

	for i, fgLine := range fgLines {
		row := y + i
		if row >= len(bgLines) {
			break
		}
		line := bgLines[row]
		left := ansi.Truncate(line, x, "")
		leftPad := max(0, x-ansi.StringWidth(left))
		right := ansi.TruncateLeft(line, x+fgWidth, "")
		linePad := max(0, fgWidth-ansi.StringWidth(fgLine))
		bgLines[row] = left + strings.Repeat(" ", leftPad) + fgLine + strings.Repeat(" ", linePad) + right
	}
	return strings.Join(bgLines, "\n")
}

// ModalWidth is the standard outer width for a centered dialog on a screen of
// the given width: capped at 72 columns, with a 2-column margin each side.
func ModalWidth(screenWidth int) int {
	return min(72, screenWidth-4)
}
