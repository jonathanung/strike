package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// overlayCenter composites fg (a rendered box) over bg, centered on a
// width×height screen. ANSI-aware: background lines are cut around the box
// with escape sequences kept balanced, so styles don't bleed into the modal.
func overlayCenter(bg, fg string, width, height int) string {
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

// modalWidth is the standard width for centered dialogs.
func modalWidth(screenWidth int) int {
	return min(72, screenWidth-4)
}
