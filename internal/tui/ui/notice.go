package ui

import (
	"strings"

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
// The whole line is truncated to width; empty text yields "". Newlines in
// text are collapsed to spaces so the output is always exactly one row —
// callers budget a single line for it.
func Notice(th theme.Theme, level Level, text string, width int) string {
	if width <= 0 || text == "" {
		return ""
	}
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
	return truncate(style.Render(glyph+" "+text), width)
}

// collapseNewlines flattens multi-line text into one row: CRLF, LF, and CR
// each become a single space.
func collapseNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\r", " ")
}
