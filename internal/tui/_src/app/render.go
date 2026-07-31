package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

func (m Model) View() tea.View {
	// Soft-coalesced updates reuse the last full frame so Bubble Tea's
	// post-Update View call does not rebuild Canvas at token/spinner rate (#496).
	if p := m.paint; p != nil && p.suppress && p.lastFrame != "" {
		p.viewCalls++
		p.lastViewBytes = len(p.lastFrame)
		return m.teaView(p.lastFrame)
	}
	if m.paint != nil {
		m.paint.viewCalls++
	}
	frame := m.renderFrame()
	if m.textSel.active() {
		frame = applyTextSelection(frame, m.textSel, m.th.S().TextSelection)
	}
	// Cache the visible frame without OSC52 so suppressed Views stay current
	// and one-shot clipboard sequences are never replayed (#496).
	m.noteCachedFrame(frame)
	if wm, ok := m.modal.(*authWaitModal); ok {
		if osc := wm.TakeCopyOSC(); osc != "" {
			frame = osc + frame
		}
	}
	if osc := m.cellClip.take(); osc != "" {
		frame = osc + frame
	}
	if m.paint != nil {
		m.paint.lastViewBytes = len(frame)
	}
	return m.teaView(frame)
}

// viewString returns the rendered frame content for tests and string-based
// assertions. Prefer this over View().Content at call sites that predate v2.
func viewString(m Model) string {
	return m.View().Content
}

// teaView wraps a frame string in the declarative Bubble Tea v2 View fields
// that replaced WithAltScreen / WithMouseCellMotion / WithReportFocus /
// SetWindowTitle program options and commands.
func (m Model) teaView(frame string) tea.View {
	v := tea.NewView(frame)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true
	v.WindowTitle = windowTitle(m)
	return v
}

// renderFrame builds the full-screen UI without OSC52 side effects or the
// active text-selection overlay (callers apply those separately). Unchanged
// layers may reuse the last composed string when frames.allowSkip says so (#494).
func (m Model) renderFrame() string {
	if m.paint != nil {
		m.paint.renderFrameCalls++
	}
	if !m.ready {
		if warning := m.dangerView(0); warning != "" {
			return warning + "\nstarting…"
		}
		return "starting…"
	}

	fc := m.frames
	if fc == nil {
		fc = newFrameCache()
	}

	gutter := m.th.Resolve().Spacing.XS
	leftWidth := m.width
	var hGeometry paneGeometry
	if m.splitOrientation != orientVertical {
		hGeometry = computePaneGeometry(m.width, gutter, m.focus)
		leftWidth = hGeometry.leftCandidateWidth(m.width)
	}
	l := computeLayout(leftWidth, m.height, m.composer.Height(), m.completionPopupHeightFor(leftWidth), m.showDangerBanner(), m.noticeRowsFor(leftWidth))
	bodyHeight := l.transcript + l.notice + l.popup + l.composer
	rightWidth, rightHeight := 0, bodyHeight
	showLeft, showRight := true, false
	vGutter := 0
	splitVertical := false
	hGutter := 0

	if m.splitOrientation == orientVertical {
		geo := computeVerticalPaneGeometry(m.width, bodyHeight, gutter, m.focus)
		if geo.mode == paneSplit {
			splitVertical = true
			l = l.withBodyHeight(geo.leftHeight)
			bodyHeight = l.transcript + l.notice + l.popup + l.composer
			rightWidth, rightHeight = geo.rightWidth, geo.rightHeight
			vGutter = geo.gutter
			showLeft, showRight = true, true
		} else if m.focus == focusRight {
			showLeft, showRight = false, true
			rightWidth = m.width
			if geo.rightHeight > 0 {
				rightHeight = geo.rightHeight
			}
		} else {
			showLeft, showRight = true, false
		}
	} else if hGeometry.mode == paneSplit {
		showLeft, showRight = true, true
		rightWidth, rightHeight = hGeometry.rightWidth, bodyHeight
		hGutter = hGeometry.gutter
	} else if m.focus == focusRight {
		showLeft, showRight = false, true
		rightWidth = m.width
	}

	leftCompact := leftWidth < compactWidth || m.height < compactHeight
	rightCompact := m.width < compactWidth || m.height < compactHeight
	bandHeight := bodyHeight
	if splitVertical && showLeft && showRight {
		bandHeight = bodyHeight + vGutter + rightHeight
	}
	headerOn := l.header > 0
	hintsOn := l.hints > 0
	dangerOn := l.danger > 0
	hasModal := m.modal != nil

	geoOK := fc.matchGeo(
		m.width, m.height, leftWidth, bodyHeight, rightWidth, rightHeight,
		hGutter, vGutter, m.focus, m.splitOrientation,
		showLeft, showRight, splitVertical, leftCompact, rightCompact, hasModal,
		headerOn, hintsOn, dangerOn, bandHeight,
	)
	skip := frameDirty(0)
	if geoOK {
		skip = fc.allowSkip
	}
	fc.storeGeo(
		m.width, m.height, leftWidth, bodyHeight, rightWidth, rightHeight,
		hGutter, vGutter, m.focus, m.splitOrientation,
		showLeft, showRight, splitVertical, leftCompact, rightCompact, hasModal,
		headerOn, hintsOn, dangerOn, bandHeight,
	)

	if showLeft && skip&dirtyLeft == 0 {
		left := make([]string, 0, 4)
		if l.transcript > 0 {
			left = append(left, m.transcriptView(leftCompact, leftWidth, l.transcript))
		}
		if l.notice > 0 {
			left = append(left, m.noticeView(leftWidth, l.notice))
		}
		if m.modal == nil && l.popup > 0 {
			if popup := m.completion.view(leftWidth, l.popup, m.th); popup != "" {
				left = append(left, popup)
			}
		}
		if l.composer > 0 {
			left = append(left, m.composerView(leftCompact, leftWidth, l.composer))
		}
		fc.leftBody = lipgloss.JoinVertical(lipgloss.Left, left...)
	}
	if !showLeft {
		fc.leftBody = ""
	}

	if showRight && skip&dirtyRight == 0 {
		rc := false
		if splitVertical || !showLeft {
			rc = rightCompact
		}
		fc.right = m.rightPaneView(rightWidth, rightHeight, rc)
		fc.rightComposeN++
	}
	if !showRight {
		fc.right = ""
	}

	var body string
	switch {
	case showLeft && showRight && splitVertical:
		body = lipgloss.JoinVertical(lipgloss.Left, fc.leftBody, paneGutter(m.th, m.width, vGutter), fc.right)
	case showLeft && showRight:
		body = lipgloss.JoinHorizontal(lipgloss.Top, fc.leftBody, paneGutter(m.th, hGutter, bodyHeight), fc.right)
	case showRight:
		body = fc.right
	default:
		body = fc.leftBody
	}

	if headerOn && skip&dirtyHeader == 0 {
		fc.header = m.headerView(m.width)
		fc.headerComposeN++
	}
	if !headerOn {
		fc.header = ""
	}

	contentParts := make([]string, 0, 2)
	if headerOn {
		contentParts = append(contentParts, fc.header)
	}
	if bandHeight > 0 && body != "" {
		contentParts = append(contentParts, body)
	}
	content := strings.Join(contentParts, "\n")
	contentHeight := l.header + bandHeight

	if skip&dirtyFooter == 0 {
		footer := make([]string, 0, 2)
		if hintsOn {
			footer = append(footer, m.hintsView(m.width))
		}
		if dangerOn {
			warning := m.dangerView(m.width)
			if warning != "" {
				footer = append(footer, warning)
			}
		}
		fc.footer = strings.Join(footer, "\n")
	}

	// One-shot skip: next paint fully recomposes unless Update marks again.
	fc.allowSkip = 0

	if m.modal != nil {
		var overlay string
		// Large surface modals (editor PTY, markdown reader) stamp host size
		// and use a near-full outer width; standard dialogs use ModalWidth.
		if sm, ok := m.modal.(interface{ setHostSize(int, int) }); ok {
			sm.setHostSize(m.width, contentHeight)
			overlay = m.modal.view(largeModalOuterWidth(m.width), m.th)
		} else {
			overlay = m.modal.view(max(8, ui.ModalWidth(m.width)), m.th)
		}
		content = ui.OverlayCenter(m.th, content, overlay, m.width, contentHeight)
	}
	parts := make([]string, 0, 2)
	if content != "" {
		parts = append(parts, content)
	}
	if fc.footer != "" {
		parts = append(parts, fc.footer)
	}
	return ui.Canvas(m.th, m.width, m.height, strings.Join(parts, "\n"))
}

// paletteResultFocus reveals a newly produced left-side notice when the right
// pane is the only visible pane. Existing notices do not move focus: only the
// result of the selected palette action does.
func (m Model) paletteResultFocus(priorNotice string, priorNoticeErr bool, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	gutter := m.th.Resolve().Spacing.XS
	singleRight := m.focus == focusRight
	if m.splitOrientation == orientVertical {
		bodyGuess := max(0, m.height-2)
		geo := computeVerticalPaneGeometry(m.width, bodyGuess, gutter, m.focus)
		singleRight = singleRight && geo.mode == paneSingle
	} else {
		geometry := computePaneGeometry(m.width, gutter, m.focus)
		singleRight = singleRight && geometry.mode == paneSingle
	}
	producedNotice := m.modal == nil && m.notice != "" && (m.notice != priorNotice || m.noticeErr != priorNoticeErr)
	if !singleRight || !producedNotice {
		return m, cmd
	}
	focusCmd := m.setPaneFocus(focusLeft)
	m.reflow()
	return m, tea.Batch(cmd, focusCmd)
}

// completionPopupHeight returns the reserved height of the completion popup
// for the current View, mirroring what reflow computed.
func (m Model) completionPopupHeight() int {
	leftWidth := m.width
	if m.splitOrientation != orientVertical {
		geometry := computePaneGeometry(m.width, m.th.Resolve().Spacing.XS, m.focus)
		leftWidth = geometry.leftCandidateWidth(m.width)
	}
	return m.completionPopupHeightFor(leftWidth)
}

func (m Model) completionPopupHeightFor(width int) int {
	if m.modal != nil || m.completion == nil || m.completion.rows <= 0 {
		return 0
	}
	borderRows := 0
	if width >= 4 {
		borderRows = 2
	}
	return m.completion.rows + borderRows
}

// compact reports whether the screen is below the breakpoints for bordered
// chrome; below it, panels degrade to plain viewport+composer.
func (m Model) compact() bool {
	leftWidth := m.width
	if m.splitOrientation != orientVertical {
		geometry := computePaneGeometry(m.width, m.th.Resolve().Spacing.XS, m.focus)
		leftWidth = geometry.leftCandidateWidth(m.width)
	}
	return leftWidth < compactWidth || m.height < compactHeight
}
