package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// modelsLoadedMsg delivers the catalog result for a provider.
type modelsLoadedMsg struct {
	provider string
	ids      []string
	err      error
}

// loadModelsCmd fetches a provider's model ids from the host catalog. A nil
// catalog degrades to a clear error rather than a panic.
func loadModelsCmd(catalog host.Catalog, provider string) tea.Cmd {
	return func() tea.Msg {
		if catalog == nil {
			return modelsLoadedMsg{provider: provider, err: errNoCatalog}
		}
		ids, err := catalog.ModelIDs(context.Background(), provider)
		if err != nil {
			return modelsLoadedMsg{provider: provider, err: err}
		}
		return modelsLoadedMsg{provider: provider, ids: ids}
	}
}

const modelModalVisible = 10

// modelModal is the centered model picker for the current provider, backed by
// the host model catalog. Type to filter, enter to switch, ctrl+d to save
// provider+model as global defaults.
type modelModal struct {
	provider string
	current  string
	all      []string
	filter   string
	cursor   int
	loading  bool
	loadErr  string
	ops      chan<- protocol.Op
	settings host.Settings
}

func newModelModal(provider, current string, ops chan<- protocol.Op, settings host.Settings) *modelModal {
	return &modelModal{provider: provider, current: current, loading: true, ops: ops, settings: settings}
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
	if isEscape(msg) {
		return nil, nil
	}
	switch msg.String() {
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
		// Freeform: when the catalog is empty/error or the filter matches
		// nothing, Enter accepts the typed filter as a model id.
		if len(list) == 0 {
			model := strings.TrimSpace(m.filter)
			if model == "" || m.loading {
				return m, nil
			}
			ops, provider := m.ops, m.provider
			return nil, func() tea.Msg {
				ops <- protocol.SelectModel{Provider: provider, Model: model}
				return nil
			}
		}
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
		return m, saveDefaultsThroughCmd(m.settings, provider, model, "", "", provider+"/"+model)
	default:
		if msg.Type == tea.KeyRunes {
			m.filter += string(msg.Runes)
			m.cursor = 0
		}
		return m, nil
	}
}

func (m *modelModal) view(width int, th theme.Theme) string {
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))

	var body string
	switch {
	case m.loading:
		body = st.Muted.Render("loading models" + th.Icons.Ellipsis)
	case m.loadErr != "":
		body = wrapToWidth(st.Error.Render(m.loadErr), inner)
		body += "\n" + st.Muted.Render("type a model id and press enter")
		if m.filter != "" {
			body += "\n" + st.Text.Render("→ "+m.filter)
		}
	default:
		list := m.filtered()
		if m.cursor >= len(list) {
			m.cursor = max(0, len(list)-1)
		}
		items := make([]ui.ListItem, len(list))
		for i, id := range list {
			items[i] = ui.ListItem{Label: id, Current: id == m.current}
		}
		body = ui.List(th, ui.ListOpts{
			Items:      items,
			Cursor:     m.cursor,
			Width:      inner,
			Visible:    modelModalVisible,
			ShowFilter: true,
			Filter:     m.filter,
			Total:      len(m.all),
			Empty:      "no matches for \"" + m.filter + "\"",
		})
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: detailJoin(th, "Select model", m.provider),
		Hint:  dotJoin(th, "type to filter or freeform id", "↑/↓ move", "enter select", "ctrl+d set default", "esc close"),
		Width: width,
	}, body)
}
