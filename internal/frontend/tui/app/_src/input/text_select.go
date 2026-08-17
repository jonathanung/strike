package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

// screenPos is a 0-based cell coordinate on the rendered frame.
type screenPos struct{ X, Y int }

// contentRect is an inclusive-origin exclusive-end screen rectangle.
type contentRect struct {
	X, Y, W, H int
}

func (r contentRect) valid() bool {
	return r.W > 0 && r.H > 0
}

func (r contentRect) contains(x, y int) bool {
	return r.valid() && x >= r.X && y >= r.Y && x < r.X+r.W && y < r.Y+r.H
}

func (r contentRect) clamp(x, y int) screenPos {
	if !r.valid() {
		return screenPos{X: x, Y: y}
	}
	if x < r.X {
		x = r.X
	} else if x >= r.X+r.W {
		x = r.X + r.W - 1
	}
	if y < r.Y {
		y = r.Y
	} else if y >= r.Y+r.H {
		y = r.Y + r.H - 1
	}
	return screenPos{X: x, Y: y}
}

// textSel is app-owned mouse text selection. Mouse tracking is enabled so the
// terminal cannot highlight chrome; selection is only started inside the
// transcript and prompt content rectangles.
type textSel struct {
	dragging bool
	has      bool // finished non-empty selection still shown
	a, b     screenPos
	region   contentRect // clamp drag to the region where the press began
}

func (s textSel) active() bool { return s.dragging || s.has }

func (s *textSel) clear() { *s = textSel{} }

func (s *textSel) start(p screenPos, region contentRect) {
	s.dragging = true
	s.has = false
	s.a, s.b = p, p
	s.region = region
}

func (s *textSel) drag(p screenPos) {
	if !s.dragging {
		return
	}
	s.b = s.region.clamp(p.X, p.Y)
}

// finish ends a drag. Returns true when the selection spans more than one cell
// (a bare click leaves no highlight).
func (s *textSel) finish() bool {
	if !s.dragging {
		return false
	}
	s.dragging = false
	if s.a.X == s.b.X && s.a.Y == s.b.Y {
		s.has = false
		return false
	}
	s.has = true
	return true
}

// bounds normalizes anchor/focus into reading-order start/end (inclusive).
func (s textSel) bounds() (start, end screenPos) {
	a, b := s.a, s.b
	if b.Y < a.Y || (b.Y == a.Y && b.X < a.X) {
		a, b = b, a
	}
	return a, b
}

// textSelectRegionAt returns the transcript or prompt content rect under (x,y).
func (m Model) textSelectRegionAt(x, y int) (contentRect, bool) {
	if m.modal != nil {
		return contentRect{}, false
	}
	if r, ok := m.transcriptContentRect(); ok && r.contains(x, y) {
		return r, true
	}
	if r, ok := m.promptContentRect(); ok && r.contains(x, y) {
		return r, true
	}
	return contentRect{}, false
}

// transcriptContentRect is the viewport body in screen coordinates.
func (m Model) transcriptContentRect() (contentRect, bool) {
	leftWidth, l, showLeft, ok := m.leftStackGeom()
	if !ok || !showLeft || l.transcript <= 0 || m.viewport.Height() <= 0 {
		return contentRect{}, false
	}
	x, y := m.transcriptBodyOrigin(leftWidth, l.header, l.compact)
	w := m.viewport.Width()
	if w <= 0 {
		w = l.transcriptInnerWidthFor(m.th.Resolve(), leftWidth)
	}
	h := m.viewport.Height()
	if h <= 0 {
		h = l.transcriptInnerHeight()
	}
	if w <= 0 || h <= 0 {
		return contentRect{}, false
	}
	return contentRect{X: x, Y: y, W: w, H: h}, true
}

// promptContentRect is the composer textarea body in screen coordinates.
func (m Model) promptContentRect() (contentRect, bool) {
	leftWidth, l, showLeft, ok := m.leftStackGeom()
	if !ok || !showLeft || l.composer <= 0 {
		return contentRect{}, false
	}
	outerY := l.header + l.transcript + l.notice + l.popup + l.tip
	x, topPad := m.panelContentOrigin(leftWidth, outerY, l.compact)
	h := l.composer
	w := leftWidth
	if !l.compact {
		h = ui.PanelInnerHeightFor(m.th.Resolve(), leftWidth, l.composer)
		w = ui.PanelInnerWidth(m.th.Resolve(), leftWidth)
	}
	if w <= 0 || h <= 0 {
		return contentRect{}, false
	}
	return contentRect{X: x, Y: topPad, W: w, H: h}, true
}

// leftStackGeom budgets the left chat stack the same way View does.
func (m Model) leftStackGeom() (leftWidth int, l layout, showLeft bool, ok bool) {
	if !m.ready || m.width <= 0 || m.height <= 0 {
		return 0, layout{}, false, false
	}
	gutter := m.paneGutter()
	leftWidth = m.width
	showLeft = true
	if m.splitOrientation != orientVertical {
		geo := computePaneGeometry(m.width, gutter, m.focus)
		if geo.mode == paneSingle && m.focus == focusRight {
			return 0, layout{}, false, true
		}
		leftWidth = geo.leftCandidateWidth(m.width)
	} else {
		l0 := computeLayout(m.width, m.height, m.composer.Height(), m.completionPopupHeightFor(m.width), m.showDangerBanner(), m.noticeRowsFor(m.width), m.tipRowsFor())
		bodyHeight := l0.transcript + l0.notice + l0.tip + l0.popup + l0.composer
		geo := computeVerticalPaneGeometry(m.width, bodyHeight, gutter, m.focus)
		if geo.mode == paneSingle && m.focus == focusRight {
			return 0, layout{}, false, true
		}
		leftWidth = m.width
		showLeft = !(geo.mode == paneSingle && m.focus == focusRight)
	}
	if !showLeft {
		return leftWidth, layout{}, false, true
	}
	l = computeLayout(leftWidth, m.height, m.composer.Height(), m.completionPopupHeightFor(leftWidth), m.showDangerBanner(), m.noticeRowsFor(leftWidth), m.tipRowsFor())
	if m.splitOrientation == orientVertical {
		bodyHeight := l.transcript + l.notice + l.tip + l.popup + l.composer
		geo := computeVerticalPaneGeometry(m.width, bodyHeight, gutter, m.focus)
		if geo.mode == paneSplit {
			l = l.withBodyHeight(geo.leftHeight)
		}
	}
	return leftWidth, l, true, true
}

// panelContentOrigin is the top-left content cell of a left-stack panel whose
// outer top row is outerY (screen coordinates). Matches ui.Panel chrome.
func (m Model) panelContentOrigin(leftWidth, outerY int, compact bool) (x, y int) {
	if compact {
		return 0, outerY
	}
	dx, dy := ui.PanelContentOrigin(m.th.Resolve(), leftWidth)
	return dx, outerY + dy
}

// transcriptBodyOrigin is the top-left cell of the transcript viewport under
// the uppercase kicker row (no boxed panel inset).
func (m Model) transcriptBodyOrigin(leftWidth, outerY int, compact bool) (x, y int) {
	if compact || leftWidth < 1 {
		return 0, outerY
	}
	return 0, outerY + 1
}

// applyTextSelection paints the linear selection range onto a full frame.
// Columns are always clipped to sel.region so multi-line drags cannot paint
// side panes, header, or footer chrome.
func applyTextSelection(frame string, sel textSel, style lipgloss.Style) string {
	if !sel.active() {
		return frame
	}
	start, end := sel.bounds()
	lines := strings.Split(frame, "\n")
	for y := start.Y; y <= end.Y && y < len(lines); y++ {
		colStart, colEnd := selectionCols(sel, y, start, end, ansi.StringWidth(lines[y]))
		if colEnd <= colStart {
			continue
		}
		lines[y] = styleColumns(lines[y], colStart, colEnd, style)
	}
	return strings.Join(lines, "\n")
}

// extractTextSelection returns plain text for the selection (newlines between rows).
func extractTextSelection(frame string, sel textSel) string {
	if !sel.active() {
		return ""
	}
	start, end := sel.bounds()
	lines := strings.Split(frame, "\n")
	var parts []string
	for y := start.Y; y <= end.Y && y < len(lines); y++ {
		colStart, colEnd := selectionCols(sel, y, start, end, ansi.StringWidth(lines[y]))
		if colEnd <= colStart {
			parts = append(parts, "")
			continue
		}
		parts = append(parts, ansi.Strip(ansi.Cut(lines[y], colStart, colEnd)))
	}
	return strings.Join(parts, "\n")
}

// selectionCols is the inclusive-start exclusive-end column range for row y,
// clipped to the selection region when one is set.
func selectionCols(sel textSel, y int, start, end screenPos, lineW int) (colStart, colEnd int) {
	colStart = 0
	colEnd = lineW
	if y == start.Y {
		colStart = start.X
	}
	if y == end.Y {
		colEnd = end.X + 1
	}
	if sel.region.valid() {
		// Also require the row to intersect the region vertically.
		if y < sel.region.Y || y >= sel.region.Y+sel.region.H {
			return 0, 0
		}
		r0, r1 := sel.region.X, sel.region.X+sel.region.W
		if colStart < r0 {
			colStart = r0
		}
		if colEnd > r1 {
			colEnd = r1
		}
	}
	if colStart < 0 {
		colStart = 0
	}
	if colEnd > lineW {
		colEnd = lineW
	}
	return colStart, colEnd
}

func styleColumns(line string, start, end int, style lipgloss.Style) string {
	w := ansi.StringWidth(line)
	if start < 0 {
		start = 0
	}
	if end > w {
		end = w
	}
	if end <= start {
		return line
	}
	left := ansi.Cut(line, 0, start)
	mid := ansi.Strip(ansi.Cut(line, start, end))
	right := ansi.Cut(line, end, w)
	// Lip Gloss v2 Render("") still emits open/close SGR (e.g. reverse). Skip
	// empty mids so zero-width style pairs cannot shift later ansi.Cut cells.
	if mid == "" {
		return left + right
	}
	return left + style.Render(mid) + right
}
