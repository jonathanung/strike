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
	tip        int  // strike tip strip above composer (droppable; #664)
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

// splitOrientation is how the left stack and right pane share the body region.
type splitOrientation uint8

const (
	// orientHorizontal is left|right (default).
	orientHorizontal splitOrientation = iota
	// orientVertical is top/bottom.
	orientVertical
)

// paneGeometry is the horizontal allocation before vertical regions are
// budgeted. In single-pane mode, only the focused pane receives the terminal
// width. When Orientation is vertical, LeftHeight/RightHeight describe the
// body split (full width each); width fields still report full terminal width.
type paneGeometry struct {
	mode        paneMode
	orientation splitOrientation
	leftWidth   int
	gutter      int
	rightWidth  int
	leftHeight  int // body rows for left/top when vertical split; 0 if unused
	rightHeight int // body rows for right/bottom when vertical split; 0 if unused
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

// computeVerticalPaneGeometry splits the body region top/bottom at full width.
// minTop/minBot keep the composer stack and side pane usable; below that only
// the focused pane is shown (same single-pane idea as the horizontal path).
func computeVerticalPaneGeometry(width, bodyHeight, gutter int, focus paneFocus) paneGeometry {
	width, bodyHeight, gutter = max(0, width), max(0, bodyHeight), max(0, gutter)
	const minTop, minBot = 6, 5
	if bodyHeight < minTop+gutter+minBot {
		if focus == focusRight {
			return paneGeometry{
				mode:        paneSingle,
				orientation: orientVertical,
				rightWidth:  width,
				rightHeight: bodyHeight,
			}
		}
		return paneGeometry{
			mode:        paneSingle,
			orientation: orientVertical,
			leftWidth:   width,
			leftHeight:  bodyHeight,
		}
	}
	available := max(0, bodyHeight-gutter)
	bottom := max(minBot, available/3)
	top := available - bottom
	return paneGeometry{
		mode:        paneSplit,
		orientation: orientVertical,
		leftWidth:   width,
		rightWidth:  width,
		gutter:      gutter,
		leftHeight:  top,
		rightHeight: bottom,
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
// permissions-bypassed banner is shown, and optional trailing ints:
//
//	noticeRows — desired height of a live notice (wrapped content). Omit or
//	pass 0 for the default blank reservation (1 row, droppable under shortfall).
//	A positive value reserves that many rows and keeps at least one under
//	pressure until last resort.
//	tipRows — optional second value: strike tip strip above the composer (#664).
//	Droppable chrome; never steals the last composer row.
func computeLayout(width, height, composerRows, popupHeight int, danger bool, noticeRows ...int) layout {
	height = max(0, height)
	noticeH := 1
	activeNotice := false
	tipH := 0
	if len(noticeRows) > 0 && noticeRows[0] > 0 {
		noticeH = noticeRows[0]
		activeNotice = true
	}
	if len(noticeRows) > 1 && noticeRows[1] > 0 {
		tipH = noticeRows[1]
	}
	l := layout{
		compact: width < compactWidth || height < compactHeight,
		header:  1,
		notice:  noticeH,
		tip:     tipH,
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
	used := l.header + l.notice + l.tip + l.hints + l.danger + l.popup + l.composer
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

	// The transcript has already absorbed all available remainder. Tip is the
	// first droppable chrome. Preserve active notice and danger as long as
	// possible. Multi-line notices may shrink toward one row before other chrome.
	reduce(&l.tip, 0)
	reduce(&l.popup, 0)
	reduce(&l.composer, 1)
	reduce(&l.hints, 0)
	if activeNotice {
		reduce(&l.notice, 1)
	} else {
		reduce(&l.notice, 0)
	}
	reduce(&l.composer, 0)
	reduce(&l.header, 0)
	reduce(&l.notice, 0)
	reduce(&l.danger, 0)
	return l
}

// withBodyHeight returns a copy of l whose transcript/notice/tip/popup/composer
// sum to bodyHeight by shrinking transcript first (used when the left stack
// shares vertical space with the right pane).
func (l layout) withBodyHeight(bodyHeight int) layout {
	bodyHeight = max(0, bodyHeight)
	cur := l.transcript + l.notice + l.tip + l.popup + l.composer
	if cur <= bodyHeight {
		l.transcript += bodyHeight - cur
		return l
	}
	shortfall := cur - bodyHeight
	reduce := func(region *int, floor int) {
		if shortfall == 0 {
			return
		}
		delta := min(shortfall, max(0, *region-floor))
		*region -= delta
		shortfall -= delta
	}
	reduce(&l.transcript, 0)
	reduce(&l.tip, 0)
	reduce(&l.popup, 0)
	reduce(&l.composer, 1)
	reduce(&l.notice, 0)
	reduce(&l.composer, 0)
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
