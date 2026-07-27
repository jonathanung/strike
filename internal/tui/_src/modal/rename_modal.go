package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

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
	buf      string
	err      string
}

func newRenameModal(sessions host.Sessions, id, current string) *renameModal {
	return &renameModal{
		sessions: sessions,
		id:       strings.TrimSpace(id),
		buf:      strings.TrimSpace(current),
	}
}

func (m *renameModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
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
		title := strings.TrimSpace(m.buf)
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
	case "backspace":
		if m.buf != "" {
			r := []rune(m.buf)
			m.buf = string(r[:len(r)-1])
		}
		m.err = ""
		return m, nil
	default:
		if msg.Type == tea.KeyRunes {
			m.buf += string(msg.Runes)
			m.err = ""
		}
		return m, nil
	}
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
	lines := []string{
		st.Muted.Render("Rename " + label),
		st.Input.Render(m.buf) + st.InputCursor.Render(th.Icons.InputCursor),
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
