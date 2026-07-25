package tui

import (
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

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

type paneMode uint8

const (
	paneSingle paneMode = iota
	paneSplit
)

// paneGeometry is the horizontal allocation before vertical regions are
// budgeted. In single-pane mode, only the focused pane receives the terminal
// width.
type paneGeometry struct {
	mode       paneMode
	leftWidth  int
	gutter     int
	rightWidth int
}

// computePaneGeometry allocates the two panes without depending on composer or
// completion height. A split needs room for a 60-column left pane and a
// 32-column right pane in addition to the themed gutter.
func computePaneGeometry(width, gutter int, focus paneFocus) paneGeometry {
	width, gutter = max(0, width), max(0, gutter)
	if width < 60+gutter+32 {
		if focus == focusRight {
			return paneGeometry{mode: paneSingle, rightWidth: width}
		}
		return paneGeometry{mode: paneSingle, leftWidth: width}
	}
	available := max(0, width-gutter)
	right := max(32, available/3)
	return paneGeometry{
		mode:       paneSplit,
		leftWidth:  max(0, available-right),
		gutter:     gutter,
		rightWidth: right,
	}
}

// leftCandidateWidth is the width used to retain composer and transcript
// state while the right pane is alone on screen.
func (g paneGeometry) leftCandidateWidth(width int) int {
	if g.leftWidth > 0 {
		return g.leftWidth
	}
	return max(0, width)
}

// computeLayout budgets the screen. composerRows is the textarea's row count,
// popupHeight the completion popup's reserved height, danger whether the
// permissions-bypassed banner is shown, and noticeActive whether the reserved
// notice row contains a live notice. noticeActive is optional for compatibility
// with callers that do not distinguish a blank reservation.
func computeLayout(width, height, composerRows, popupHeight int, danger bool, noticeActive ...bool) layout {
	height = max(0, height)
	activeNotice := len(noticeActive) > 0 && noticeActive[0]
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
	shortfall := max(0, used-height)
	reduce := func(region *int, floor int) {
		if shortfall == 0 {
			return
		}
		delta := min(shortfall, max(0, *region-floor))
		*region -= delta
		shortfall -= delta
	}

	// The transcript has already absorbed all available remainder. Preserve the
	// active notice and danger banner as long as possible when space is scarce.
	reduce(&l.popup, 0)
	reduce(&l.composer, 1)
	reduce(&l.hints, 0)
	if !activeNotice {
		reduce(&l.notice, 0)
	}
	reduce(&l.composer, 0)
	reduce(&l.header, 0)
	reduce(&l.notice, 0)
	reduce(&l.danger, 0)
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
	return l.transcriptInnerWidthFor(theme.Default(), width)
}

func (l layout) transcriptInnerWidthFor(th theme.Theme, width int) int {
	if l.compact {
		return max(1, width)
	}
	return max(1, ui.PanelInnerWidth(th, width))
}
