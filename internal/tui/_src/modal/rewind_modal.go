package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const rewindModalVisible = 10

// rewindChoice is one /rewind fork-at-turn option.
type rewindChoice struct {
	keepEvents int
	turn       int
	preview    string
}

// rewindModal lets the user pick a completed turn and fork a new session from
// that event offset. The source session is left intact.
type rewindModal struct {
	sessionID string
	sessions  host.Sessions
	choices   []rewindChoice
	cursor    int
	loadErr   string
	onPick    func(keepEvents int) tea.Cmd
}

func newRewindModal(sessions host.Sessions, sessionID string, onPick func(keepEvents int) tea.Cmd) *rewindModal {
	m := &rewindModal{
		sessionID: strings.TrimSpace(sessionID),
		sessions:  sessions,
		onPick:    onPick,
	}
	m.load()
	return m
}

func (m *rewindModal) load() {
	m.loadErr = ""
	m.choices = nil
	m.cursor = 0
	if m.sessions == nil {
		m.loadErr = "session rewind is unavailable"
		return
	}
	if m.sessionID == "" {
		m.loadErr = "no session to rewind"
		return
	}
	raw, err := m.sessions.ReplayJSONL(m.sessionID)
	if err != nil {
		m.loadErr = err.Error()
		return
	}
	events, err := decodeSessionJSONL(raw)
	if err != nil {
		m.loadErr = err.Error()
		return
	}
	points := protocol.RewindPoints(events)
	if len(points) == 0 {
		m.loadErr = "nothing to rewind (need a completed turn)"
		return
	}
	for _, p := range points {
		m.choices = append(m.choices, rewindChoice{
			keepEvents: p.KeepEvents,
			turn:       p.Turn,
			preview:    p.Preview,
		})
	}
	// Default to the previous completed turn (one step back), else the only point.
	if len(m.choices) >= 2 {
		m.cursor = len(m.choices) - 2
	} else {
		m.cursor = 0
	}
}

func (m *rewindModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	if isEscape(msg) {
		return nil, nil
	}
	if m.loadErr != "" || len(m.choices) == 0 {
		return m, nil
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
		keep := m.choices[m.cursor].keepEvents
		if m.onPick == nil {
			return nil, nil
		}
		return nil, m.onPick(keep)
	default:
		return m, nil
	}
}

func (m *rewindModal) view(width int, th theme.Theme) string {
	inner := max(1, ui.PanelInnerWidth(th, width))
	if m.loadErr != "" {
		body := th.S().Warning.Render(m.loadErr)
		return ui.Dialog(th, ui.DialogOpts{
			Title: "Rewind (fork from turn)",
			Hint:  "esc close",
			Width: width,
		}, body)
	}
	items := make([]ui.ListItem, len(m.choices))
	for i, c := range m.choices {
		label := fmt.Sprintf("turn %d", c.turn)
		detail := c.preview
		if detail == "" {
			detail = "(empty prompt)"
		}
		items[i] = ui.ListItem{Label: label, Detail: detail}
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   inner,
		Visible: min(rewindModalVisible, len(m.choices)),
		Empty:   "no turns",
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Rewind (fork from turn)",
		Hint:  dotJoin(th, "up/down/j/k move", "enter fork", "esc close"),
		Width: width,
	}, body)
}
