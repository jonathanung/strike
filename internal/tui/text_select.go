package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/ui"
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
	if !ok || !showLeft || l.transcript <= 0 || m.viewport.Height <= 0 {
		return contentRect{}, false
	}
	x, y := m.panelContentOrigin(leftWidth, l.header, l.compact)
	w := m.viewport.Width
	if w <= 0 {
		w = l.transcriptInnerWidthFor(m.th.Resolve(), leftWidth)
	}
	h := m.viewport.Height
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
	outerY := l.header + l.transcript + l.notice + l.popup
	x, topPad := m.panelContentOrigin(leftWidth, outerY, l.compact)
	h := l.composer
	w := leftWidth
	if !l.compact {
		h = ui.PanelInnerHeight(leftWidth, l.composer)
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
	th := m.th.Resolve()
	gutter := th.Spacing.XS
	leftWidth = m.width
	showLeft = true
	if m.splitOrientation != orientVertical {
		geo := computePaneGeometry(m.width, gutter, m.focus)
		if geo.mode == paneSingle && m.focus == focusRight {
			return 0, layout{}, false, true
		}
		leftWidth = geo.leftCandidateWidth(m.width)
	} else {
		l0 := computeLayout(m.width, m.height, m.composer.Height(), m.completionPopupHeightFor(m.width), m.showDangerBanner(), m.noticeRowsFor(m.width))
		bodyHeight := l0.transcript + l0.notice + l0.popup + l0.composer
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
	l = computeLayout(leftWidth, m.height, m.composer.Height(), m.completionPopupHeightFor(leftWidth), m.showDangerBanner(), m.noticeRowsFor(leftWidth))
	if m.splitOrientation == orientVertical {
		bodyHeight := l.transcript + l.notice + l.popup + l.composer
		geo := computeVerticalPaneGeometry(m.width, bodyHeight, gutter, m.focus)
		if geo.mode == paneSplit {
			l = l.withBodyHeight(geo.leftHeight)
		}
	}
	return leftWidth, l, true, true
}

// panelContentOrigin is the top-left content cell of a left-stack panel whose
// outer top row is outerY (screen coordinates).
func (m Model) panelContentOrigin(leftWidth, outerY int, compact bool) (x, y int) {
	y = outerY
	x = 0
	if compact {
		return x, y
	}
	th := m.th.Resolve()
	// Panel top border + left border + horizontal padding (matches transcript).
	y++
	if leftWidth >= 3 {
		x = 1
		if leftWidth >= 6 {
			padX := th.Spacing.XS
			if padX < 0 {
				padX = 0
			}
			if maxPad := (leftWidth - 3) / 2; padX > maxPad {
				padX = maxPad
			}
			x += padX
		}
	}
	return x, y
}

// applyTextSelection paints the linear selection range onto a full frame.
func applyTextSelection(frame string, sel textSel, style lipgloss.Style) string {
	if !sel.active() {
		return frame
	}
	start, end := sel.bounds()
	lines := strings.Split(frame, "\n")
	for y := start.Y; y <= end.Y && y < len(lines); y++ {
		line := lines[y]
		w := ansi.StringWidth(line)
		colStart := 0
		colEnd := w
		if y == start.Y {
			colStart = start.X
		}
		if y == end.Y {
			colEnd = end.X + 1
		}
		if colStart < 0 {
			colStart = 0
		}
		if colEnd > w {
			colEnd = w
		}
		if colEnd <= colStart {
			continue
		}
		lines[y] = styleColumns(line, colStart, colEnd, style)
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
		line := lines[y]
		w := ansi.StringWidth(line)
		colStart := 0
		colEnd := w
		if y == start.Y {
			colStart = start.X
		}
		if y == end.Y {
			colEnd = end.X + 1
		}
		if colStart < 0 {
			colStart = 0
		}
		if colEnd > w {
			colEnd = w
		}
		if colEnd <= colStart {
			parts = append(parts, "")
			continue
		}
		parts = append(parts, ansi.Strip(ansi.Cut(line, colStart, colEnd)))
	}
	return strings.Join(parts, "\n")
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
	return left + style.Render(mid) + right
}
