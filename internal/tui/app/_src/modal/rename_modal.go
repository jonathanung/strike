package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// sessionRenamedMsg is emitted after a durable rename so the live agents tree,
// panel chrome, and window title pick up the new label without a restart.
type sessionRenamedMsg struct {
	id    string
	title string
}

// renameModal is a focused title editor for one session (agents pane r, /rename).
type renameModal struct {
	sessions host.Sessions
	id       string
	input    textinput.Model
	err      string
}

func newRenameModal(sessions host.Sessions, id, current string, themes ...theme.Theme) *renameModal {
	th := theme.Default()
	if len(themes) > 0 {
		th = themes[0]
	}
	th = th.Resolve()
	in := newTextInput(th, "session title")
	in.SetValue(strings.TrimSpace(current))
	in.CursorEnd()
	in.Focus()
	return &renameModal{
		sessions: sessions,
		id:       strings.TrimSpace(id),
		input:    in,
	}
}

func (m *renameModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) {
		return nil, nil
	}
	switch msg.String() {
	case "enter":
		if m.sessions == nil {
			m.err = "session list unavailable"
			return m, nil
		}
		if m.id == "" {
			return nil, nil
		}
		title := strings.TrimSpace(m.input.Value())
		got, err := m.sessions.Rename(m.id, title)
		if err != nil {
			m.err = err.Error()
			return m, nil
		}
		id := got.ID
		if id == "" {
			id = m.id
		}
		final := strings.TrimSpace(got.Title)
		return nil, func() tea.Msg {
			return sessionRenamedMsg{id: id, title: final}
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.err = ""
	return m, cmd
}

func (m *renameModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := ui.PanelInnerWidth(th, width)
	if width < 4 {
		inner = max(1, width)
	}
	label := shortSessionID(m.id)
	if label == "" {
		label = "session"
	}
	sizeInput(&m.input, inner)
	lines := []string{
		st.Muted.Render("Rename " + label),
		m.input.View(),
	}
	if m.err != "" {
		lines = append(lines, st.Error.Render(sanitizeDisplayData(m.err)))
	}
	body := wrapToWidth(strings.Join(lines, "\n"), inner)
	if width < 4 {
		return body
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Rename session",
		Hint:  dotJoin(th, "type title", "enter save", "esc cancel"),
		Width: width,
	}, body)
}
