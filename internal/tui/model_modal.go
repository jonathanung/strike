package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/models"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// modelsLoadedMsg delivers the models.dev catalog result for a provider.
type modelsLoadedMsg struct {
	provider string
	ids      []string
	err      error
}

func loadModelsCmd(provider string) tea.Cmd {
	return func() tea.Msg {
		catalog, err := models.Load(context.Background())
		if err != nil {
			return modelsLoadedMsg{provider: provider, err: err}
		}
		ids := catalog.ModelIDs(provider)
		if len(ids) == 0 {
			return modelsLoadedMsg{provider: provider, err: fmt.Errorf("no models listed for %s on models.dev", provider)}
		}
		return modelsLoadedMsg{provider: provider, ids: ids}
	}
}

const modelModalVisible = 10

// modelModal is the centered model picker for the current provider, backed
// by the models.dev catalog. Type to filter, enter to switch, ctrl+d to
// save provider+model as global defaults.
type modelModal struct {
	provider string
	current  string
	all      []string
	filter   string
	cursor   int
	loading  bool
	loadErr  string
	ops      chan<- protocol.Op
}

func newModelModal(provider, current string, ops chan<- protocol.Op) *modelModal {
	return &modelModal{provider: provider, current: current, loading: true, ops: ops}
}

func (m *modelModal) filtered() []string {
	if m.filter == "" {
		return m.all
	}
	var out []string
	for _, id := range m.all {
		if strings.Contains(strings.ToLower(id), strings.ToLower(m.filter)) {
			out = append(out, id)
		}
	}
	return out
}

func (m *modelModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	list := m.filtered()
	switch msg.String() {
	case "esc":
		return nil, nil
	case "up", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "ctrl+n":
		if m.cursor < len(list)-1 {
			m.cursor++
		}
		return m, nil
	case "backspace":
		if m.filter != "" {
			m.filter = m.filter[:len(m.filter)-1]
			m.cursor = 0
		}
		return m, nil
	case "enter":
		if m.cursor >= len(list) {
			return m, nil
		}
		ops, provider, model := m.ops, m.provider, list[m.cursor]
		return nil, func() tea.Msg {
			ops <- protocol.SelectModel{Provider: provider, Model: model}
			return nil
		}
	case "ctrl+d":
		if m.cursor >= len(list) {
			return m, nil
		}
		provider, model := m.provider, list[m.cursor]
		return m, func() tea.Msg {
			err := config.SetGlobalDefaults(provider, model, "")
			return defaultsSavedMsg{text: provider + "/" + model, err: err}
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.filter += string(msg.Runes)
			m.cursor = 0
		}
		return m, nil
	}
}

func (m *modelModal) view(width int, th theme.Theme) string {
	title := lipgloss.NewStyle().Foreground(th.Accent).Bold(true).
		Render("Select model — " + m.provider)
	muted := lipgloss.NewStyle().Foreground(th.TextMuted)

	var body string
	switch {
	case m.loading:
		body = muted.Render("loading models.dev catalog…")
	case m.loadErr != "":
		body = lipgloss.NewStyle().Foreground(th.Error).Width(width - 4).Render(m.loadErr)
	default:
		list := m.filtered()
		if m.cursor >= len(list) {
			m.cursor = max(0, len(list)-1)
		}
		start := max(0, min(m.cursor-modelModalVisible/2, len(list)-modelModalVisible))
		end := min(len(list), start+modelModalVisible)
		var rows []string
		for i := start; i < end; i++ {
			cursor, style := "  ", lipgloss.NewStyle().Foreground(th.Text)
			if i == m.cursor {
				cursor, style = "▸ ", lipgloss.NewStyle().Foreground(th.Accent).Bold(true)
			}
			label := list[i]
			if label == m.current {
				label += " (current)"
			}
			rows = append(rows, cursor+style.Render(label))
		}
		if len(rows) == 0 {
			rows = []string{muted.Render("no matches for \"" + m.filter + "\"")}
		}
		filterLine := muted.Render("filter: ") + lipgloss.NewStyle().Foreground(th.Text).Render(m.filter+"▏")
		counter := muted.Render(fmt.Sprintf("%d/%d", len(list), len(m.all)))
		body = filterLine + " " + counter + "\n\n" + strings.Join(rows, "\n")
	}
	hint := muted.Render("type to filter · ↑/↓ move · enter select · ctrl+d set default · esc close")

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(th.BorderFocus).
		Padding(0, 1).
		Width(width)
	return box.Render(title + "\n\n" + body + "\n\n" + hint)
}
