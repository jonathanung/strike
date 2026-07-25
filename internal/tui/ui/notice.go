package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// Level is the severity of a Notice, selecting its glyph and color.
type Level int

const (
	LevelInfo    Level = iota // ◦ informational (accent-alt)
	LevelSuccess              // ✓ positive (success)
	LevelWarning              // ◦ caution (warning)
	LevelError                // ✗ failure (error)
)

// Notice renders a single-line status message prefixed with a level glyph and
// colored to match. It is the reserved feedback row above the composer and
// the style for error/info transcript cells.
//
//	ui.Notice(th, ui.LevelError, "no model selected — use /provider", width)
//	ui.Notice(th, ui.LevelSuccess, "saved as default: echo/echo-1", width)
//
// Equivalent to NoticeLines with maxLines=1. Empty text yields "". Newlines in
// text are collapsed to spaces.
func Notice(th theme.Theme, level Level, text string, width int) string {
	return NoticeLines(th, level, text, width, 1)
}

// NoticeLines renders a level-colored notice wrapped to width, at most maxLines
// rows. The level glyph prefixes the first line; continuation lines use a hanging
// indent so text columns align. When the wrapped text exceeds maxLines, the last
// kept line ends with an ellipsis. Empty text, non-positive width, or maxLines < 1
// yields "".
//
//	ui.NoticeLines(th, ui.LevelInfo, helpText, width, 5)
func NoticeLines(th theme.Theme, level Level, text string, width, maxLines int) string {
	if width <= 0 || maxLines < 1 || text == "" {
		return ""
	}
	th = th.Resolve()
	text = collapseNewlines(text)
	ic := resolveIcons(th)
	st := th.S()

	glyph, style := ic.Info, st.AccentAlt
	switch level {
	case LevelSuccess:
		glyph, style = ic.OK, st.Success
	case LevelWarning:
		glyph, style = ic.Info, st.Warning
	case LevelError:
		glyph, style = ic.Err, st.Error
	}

	gap := strings.Repeat(" ", th.Spacing.XS)
	prefix := glyph + gap
	prefixW := lipgloss.Width(prefix)
	if prefixW >= width {
		return truncate(th, style.Render(prefix+text), width)
	}
	bodyWidth := width - prefixW

	wrapped := wrapText(text, bodyWidth)
	lines := strings.Split(wrapped, "\n")
	overflow := len(lines) > maxLines
	if overflow {
		lines = lines[:maxLines]
		lines[maxLines-1] = fitEllipsis(th, lines[maxLines-1], bodyWidth)
	}

	indent := strings.Repeat(" ", prefixW)
	out := make([]string, len(lines))
	for i, line := range lines {
		if i == 0 {
			out[i] = truncate(th, style.Render(prefix+line), width)
		} else {
			out[i] = truncate(th, style.Render(indent+line), width)
		}
	}
	return strings.Join(out, "\n")
}

// fitEllipsis forces s to end with an ellipsis within width cells.
func fitEllipsis(th theme.Theme, s string, width int) string {
	if width <= 0 {
		return ""
	}
	ell := resolveIcons(th).Ellipsis
	if lipgloss.Width(s) < width {
		if lipgloss.Width(s+ell) <= width {
			return s + ell
		}
	}
	return ansi.Truncate(s, width, ell)
}

// collapseNewlines flattens multi-line text into one row: CRLF, LF, and CR
// each become a single space.
func collapseNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\r", " ")
}
