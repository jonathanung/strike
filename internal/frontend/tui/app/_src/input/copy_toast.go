package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike/internal/frontend/tui/ui"
)

// copyToastDuration is how long the top-right clipboard badge stays up after
// an in-app mouse-drag copy.
const copyToastDuration = 1500 * time.Millisecond

// copyToastText is deliberately "sent", not "copied": OSC52 is fire-and-forget,
// so strike cannot confirm the terminal (or tmux/SSH hop) accepted the write.
const copyToastText = "Sent to clipboard"

// clearCopyToastMsg ends the copy badge started by showCopyToast with gen.
type clearCopyToastMsg struct{ gen int }

// showCopyToast raises the clipboard badge and arms its reset. A newer copy
// bumps the generation so an older timer cannot cut the fresh badge short.
//
// Contract: only app-owned drag selection (transcript + prompt) reaches this.
// Terminal-native selection (shift/option+drag, which bypasses mouse
// reporting) never notifies the app, so it gets the terminal's own copy
// feedback and no badge.
func (m *Model) showCopyToast() tea.Cmd {
	m.copyToastGen++
	m.copyToast = true
	gen := m.copyToastGen
	return tea.Tick(copyToastDuration, func(time.Time) tea.Msg {
		return clearCopyToastMsg{gen: gen}
	})
}

// applyCopyToast overlays the clipboard badge at the frame's top-right corner
// while the toast is active.
func (m Model) applyCopyToast(frame string) string {
	if !m.copyToast {
		return frame
	}
	th := m.th.Resolve()
	badge := ui.Badge(th, ui.ToneAccent, th.Icons.OK+themedSpace(th.Spacing.XS)+copyToastText)
	return ui.OverlayTopRight(th, frame, badge, m.width)
}
