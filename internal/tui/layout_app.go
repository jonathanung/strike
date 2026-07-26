package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

func (m *Model) reflow() {
	gutter := m.th.Resolve().Spacing.XS
	leftWidth := m.width
	if m.splitOrientation != orientVertical {
		geometry := computePaneGeometry(m.width, gutter, m.focus)
		leftWidth = geometry.leftCandidateWidth(m.width)
	}
	compact := leftWidth < compactWidth || m.height < compactHeight
	composerWidth := leftWidth
	if !compact {
		composerWidth = ui.PanelInnerWidth(m.th, leftWidth)
	}
	m.composer.SetWidth(max(1, composerWidth))
	contentWidth := max(1, m.composer.Width())
	lineCounter := textarea.New()
	lineCounter.Prompt = ""
	lineCounter.ShowLineNumbers = false
	lineCounter.SetWidth(contentWidth)
	visualLines := 0
	for _, line := range strings.Split(m.composer.Value(), "\n") {
		lineCounter.SetValue(line)
		visualLines += max(1, lineCounter.LineInfo().Height)
		if visualLines >= composerMaxHeight {
			break
		}
	}
	composerRows := min(composerMaxHeight, max(composerMinHeight, visualLines))
	m.composer.SetHeight(composerRows)

	popupHeight := 0
	if m.completion != nil && m.modal == nil {
		m.completion.rows = 0
		if leftWidth > 0 {
			borderRows := 0
			if leftWidth >= 4 {
				borderRows = 2
			}
			available := max(0, m.height-2-composerRows-borderRows)
			n := len(m.completion.Candidates)
			if n == 0 && m.completion.emptyHint != "" {
				n = 1 // reserve one row for the empty-state explanation
			}
			m.completion.rows = min(completionMaxRows, min(n, available))
			if m.completion.rows > 0 {
				popupHeight = m.completion.rows + borderRows
			}
		}
	}

	if m.ready {
		l := computeLayout(leftWidth, m.height, composerRows, popupHeight, m.showDangerBanner(), m.noticeRowsFor(leftWidth))
		bodyHeight := l.transcript + l.notice + l.popup + l.composer
		rightWidth, rightHeight := m.width, bodyHeight
		rightCompact := m.width < compactWidth || m.height < compactHeight

		if m.splitOrientation == orientVertical {
			geo := computeVerticalPaneGeometry(m.width, bodyHeight, gutter, m.focus)
			if geo.mode == paneSplit {
				l = l.withBodyHeight(geo.leftHeight)
				rightWidth = geo.rightWidth
				rightHeight = geo.rightHeight
				rightCompact = false
			} else if m.focus == focusRight {
				rightWidth = geo.rightWidth
				rightHeight = geo.rightHeight
				if rightHeight == 0 {
					rightHeight = bodyHeight
				}
			} else {
				// Left-only single: keep full body on the left stack.
				rightWidth, rightHeight = 0, 0
			}
		} else {
			geometry := computePaneGeometry(m.width, gutter, m.focus)
			rightWidth = geometry.rightWidth
			if rightWidth == 0 {
				rightWidth = m.width
			}
			rightCompact = geometry.mode == paneSingle && (m.width < compactWidth || m.height < compactHeight)
			rightHeight = bodyHeight
		}

		m.viewport.Width = max(1, l.transcriptInnerWidthFor(m.th, leftWidth))
		m.viewport.Height = max(0, l.transcriptInnerHeight())
		if rightWidth > 0 && rightHeight > 0 {
			m.windows = m.resizeRightWindows(rightWidth, rightHeight, rightCompact)
		}
	}
}

// resizeRightWindows sizes every right-pane window. Stack groups that fit get
// per-member slots; otherwise all windows share the full pane inner box.
func (m Model) resizeRightWindows(rightWidth, rightHeight int, compact bool) windowRegistry {
	r := m.windows
	fullInnerW, fullInnerH := rightWidth, rightHeight
	if !compact {
		fullInnerW = max(0, ui.PanelInnerWidth(m.th, rightWidth))
		fullInnerH = ui.PanelInnerHeight(rightWidth, rightHeight)
	}
	r = r.resize(fullInnerW, fullInnerH)

	g := r.activeGroup()
	pairHorizontal := m.splitOrientation == orientVertical
	stackGutter := m.th.Resolve().Spacing.XS
	slots := computeMemberSlots(rightWidth, rightHeight, stackGutter, len(g.members), pairHorizontal)
	if compact || len(slots) == 0 {
		return r
	}
	dims := make(map[int]memberSlot, len(g.members))
	for i, wi := range g.members {
		outer := slots[i]
		innerW, innerH := outer.width, outer.height
		if !compact {
			innerW = max(0, ui.PanelInnerWidth(m.th, outer.width))
			innerH = ui.PanelInnerHeight(outer.width, outer.height)
		}
		dims[wi] = memberSlot{width: innerW, height: innerH}
	}
	return r.resizeMembers(dims)
}

// toggleOrientation flips horizontal/vertical body split and refreshes layout.
func (m *Model) toggleOrientation() {
	if m.splitOrientation == orientVertical {
		m.splitOrientation = orientHorizontal
	} else {
		m.splitOrientation = orientVertical
	}
	m.keyMap = buildKeyMap(m.keyOverrides, m.splitOrientation)
	m.reflow()
	m.refreshViewport()
}

func cloneKeybindMap(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for id, chords := range in {
		out[id] = append([]string(nil), chords...)
	}
	return out
}

// armPermissionAutoApprove starts the modal countdown when mode is armed and
// the permission name is not excluded.
func (m *Model) armPermissionAutoApprove(pm *permissionModal, permission string) tea.Cmd {
	if pm == nil || m.permissionAutoApproveSeconds <= 0 {
		return nil
	}
	if permissionAutoApproveExcluded(permission, m.permissionAutoApproveExclude) {
		return nil
	}
	return pm.armAutoApprove(m.permissionAutoApproveSeconds)
}

func permissionAutoApproveExcluded(permission string, exclude []string) bool {
	if len(exclude) == 0 {
		return false
	}
	want := strings.ToLower(strings.TrimSpace(permission))
	for _, name := range exclude {
		if strings.EqualFold(strings.TrimSpace(name), want) {
			return true
		}
	}
	return false
}
