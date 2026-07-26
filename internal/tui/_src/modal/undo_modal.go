package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// undoChoice is one /undo option.
type undoChoice struct {
	label        string
	detail       string
	restoreFiles bool
}

// undoModal picks chat-only rewind vs rewind + per-file restore.
type undoModal struct {
	choices []undoChoice
	cursor  int
	ops     chan<- protocol.Op
}

func newUndoModal(ops chan<- protocol.Op) *undoModal {
	return &undoModal{
		ops: ops,
		choices: []undoChoice{
			{
				label:        "chat only",
				detail:       "drop the last turn from history; keep disk changes",
				restoreFiles: false,
			},
			{
				label:        "chat and files",
				detail:       "drop the last turn and restore files edited in that turn",
				restoreFiles: true,
			},
		},
		// Prefer full restore when the user opens the picker.
		cursor: 1,
	}
}

func (m *undoModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	if isEscape(msg) {
		return nil, nil
	}
	switch msg.String() {
	case "up", "k", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j", "ctrl+n":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
		}
		return m, nil
	case "enter":
		if m.cursor < 0 || m.cursor >= len(m.choices) {
			return m, nil
		}
		ops, restore := m.ops, m.choices[m.cursor].restoreFiles
		return nil, func() tea.Msg {
			ops <- protocol.Rewind{RestoreFiles: restore}
			return nil
		}
	case "1":
		ops := m.ops
		return nil, func() tea.Msg {
			ops <- protocol.Rewind{RestoreFiles: false}
			return nil
		}
	case "2":
		ops := m.ops
		return nil, func() tea.Msg {
			ops <- protocol.Rewind{RestoreFiles: true}
			return nil
		}
	default:
		return m, nil
	}
}

func (m *undoModal) view(width int, th theme.Theme) string {
	inner := max(1, ui.PanelInnerWidth(th, width))
	items := make([]ui.ListItem, len(m.choices))
	for i, c := range m.choices {
		items[i] = ui.ListItem{
			Label:  c.label,
			Detail: c.detail,
		}
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   inner,
		Visible: len(m.choices),
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Undo last turn",
		Hint:  dotJoin(th, "up/down/j/k move", "1/2 or enter select", "esc close"),
		Width: width,
	}, body)
}
