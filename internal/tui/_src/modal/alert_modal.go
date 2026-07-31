package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// alertModal is a one-shot dismissible message (enter/esc/q). Used for
// startup soft-fails such as session worktree outside a git repository.
type alertModal struct {
	title string
	body  string
	tone  ui.Tone
}

func newAlertModal(title, body string, tone ui.Tone) *alertModal {
	return &alertModal{title: title, body: body, tone: tone}
}

func (m *alertModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "enter" || msg.String() == "q" {
		return nil, func() tea.Msg { return alertDismissedMsg{} }
	}
	return m, nil
}

func (m *alertModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))
	body := wrapToWidth(st.Text.Render(m.body), inner)
	title := m.title
	if title == "" {
		title = "Notice"
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: title,
		Hint:  dotJoin(th, "enter/esc dismiss"),
		Width: width,
		Tone:  m.tone,
	}, body)
}

// startupAlertMsg opens Options.StartupAlert once after Init.
type startupAlertMsg struct{}

// alertDismissedMsg is delivered when alertModal closes so first-run setup
// can still open if it was deferred behind the alert.
type alertDismissedMsg struct{}
