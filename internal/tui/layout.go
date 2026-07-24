package tui

import "github.com/jonathanung/strike-cli/internal/tui/ui"

// layout is the explicit height budget for the stacked screen regions. Every
// region has a fixed height except the transcript, which flexes to fill what
// remains; the sum of all regions equals the screen height. computeLayout is a
// pure function so the budget is unit-testable (layout_test.go).
type layout struct {
	compact    bool // borders dropped (small screen)
	header     int  // header strip
	transcript int  // transcript region outer height (flex)
	notice     int  // reserved notice row
	popup      int  // completion popup (0 when closed)
	composer   int  // composer region outer height (includes its border when not compact)
	hints      int  // keybinding footer
	danger     int  // danger banner (0 unless permissions are bypassed)
}

// computeLayout budgets the screen. composerRows is the textarea's row count,
// popupHeight the completion popup's reserved height, and danger whether the
// permissions-bypassed banner is shown. The transcript region absorbs the
// remaining height (never negative).
func computeLayout(width, height, composerRows, popupHeight int, danger bool) layout {
	l := layout{
		compact: width < compactWidth || height < compactHeight,
		header:  1,
		notice:  1,
		hints:   1,
		popup:   max(0, popupHeight),
	}
	if danger {
		l.danger = 1
	}
	composerBorder := 0
	if !l.compact {
		composerBorder = 2 // rounded top+bottom border rows around the composer
	}
	l.composer = max(0, composerRows) + composerBorder
	used := l.header + l.notice + l.hints + l.danger + l.popup + l.composer
	l.transcript = max(0, height-used)
	return l
}

// transcriptInnerHeight is the viewport height inside the transcript region:
// the outer height less its border rows in bordered mode.
func (l layout) transcriptInnerHeight() int {
	if l.compact {
		return l.transcript
	}
	return max(0, l.transcript-2)
}

// transcriptInnerWidth is the viewport width inside the transcript region: the
// full width in compact mode, else the panel's inner content width.
func (l layout) transcriptInnerWidth(width int) int {
	if l.compact {
		return max(1, width)
	}
	return max(1, ui.InnerWidth(width))
}
