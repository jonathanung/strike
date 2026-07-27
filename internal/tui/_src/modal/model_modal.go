package tui

import (
	"context"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// modelsLoadedMsg delivers the multi-provider catalog result for the model picker.
type modelsLoadedMsg struct {
	fallback string // current provider at open (matches modal identity)
	models   []host.ModelInfo
	err      error
}

// loadModelsCmd fetches models for every listed provider from the host
// catalog. A nil catalog degrades to a clear error rather than a panic.
// providers should be authenticated (plus current) names; empty names are ignored.
func loadModelsCmd(catalog host.Catalog, providers []string, fallback string) tea.Cmd {
	return func() tea.Msg {
		if catalog == nil {
			return modelsLoadedMsg{fallback: fallback, err: errNoCatalog}
		}
		models, err := catalog.ModelsForProviders(context.Background(), providers)
		if err != nil {
			return modelsLoadedMsg{fallback: fallback, err: err}
		}
		return modelsLoadedMsg{fallback: fallback, models: models}
	}
}

// authenticatedModelProviders lists Authed provider names from host.Auth,
// always including current when non-empty so single-provider / unauthed edge
// cases still load the active provider's catalog.
func authenticatedModelProviders(auth host.Auth, current string) []string {
	var out []string
	seen := map[string]bool{}
	if auth != nil {
		for _, s := range auth.Statuses() {
			name := strings.TrimSpace(s.Name)
			if name == "" || !s.Authed || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	current = strings.TrimSpace(current)
	if current != "" && !seen[current] {
		out = append(out, current)
	}
	return out
}

// parseModelArg splits "provider/model" (first slash) or returns fallback + bare id.
func parseModelArg(arg, fallbackProvider string) (provider, model string) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return fallbackProvider, ""
	}
	if prov, bare, ok := strings.Cut(arg, "/"); ok && prov != "" && bare != "" {
		return prov, bare
	}
	return fallbackProvider, arg
}

const modelModalVisible = 10

// modelModal is the centered model picker listing models from all authenticated
// providers. Type to filter, enter to switch (including cross-provider), ctrl+d
// to save provider+model as global defaults. Rows show provider, context window,
// cost, and capability flags when the catalog provides them.
type modelModal struct {
	provider string // current/fallback provider (for freeform + identity)
	current  string // current model id
	all      []host.ModelInfo
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

func (m *modelModal) filtered() []host.ModelInfo {
	if m.filter == "" {
		return m.all
	}
	q := strings.ToLower(m.filter)
	var out []host.ModelInfo
	for _, info := range m.all {
		if strings.Contains(strings.ToLower(info.ID), q) {
			out = append(out, info)
			continue
		}
		if info.Name != "" && strings.Contains(strings.ToLower(info.Name), q) {
			out = append(out, info)
			continue
		}
		if info.Provider != "" && strings.Contains(strings.ToLower(info.Provider), q) {
			out = append(out, info)
			continue
		}
		if info.Provider != "" && strings.Contains(strings.ToLower(info.Provider+"/"+info.ID), q) {
			out = append(out, info)
		}
	}
	return out
}

// modelPickerLabel is the primary row text: display name when set, else id.
func modelPickerLabel(info host.ModelInfo) string {
	if name := strings.TrimSpace(info.Name); name != "" && name != info.ID {
		return name
	}
	if name := strings.TrimSpace(info.Name); name != "" {
		return name
	}
	return info.ID
}

// modelRowProvider returns the provider to use when selecting info.
func modelRowProvider(info host.ModelInfo, fallback string) string {
	if p := strings.TrimSpace(info.Provider); p != "" {
		return p
	}
	return fallback
}

func (m *modelModal) isCurrent(info host.ModelInfo) bool {
	if info.ID != m.current {
		return false
	}
	p := modelRowProvider(info, m.provider)
	return p == "" || p == m.provider
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
		// nothing, Enter accepts the typed filter as a model id (or provider/model).
		if len(list) == 0 {
			raw := strings.TrimSpace(m.filter)
			if raw == "" || m.loading {
				return m, nil
			}
			provider, model := parseModelArg(raw, m.provider)
			ops := m.ops
			return nil, func() tea.Msg {
				ops <- protocol.SelectModel{Provider: provider, Model: model}
				return nil
			}
		}
		if m.cursor >= len(list) {
			return m, nil
		}
		info := list[m.cursor]
		ops, provider, model := m.ops, modelRowProvider(info, m.provider), info.ID
		return nil, func() tea.Msg {
			ops <- protocol.SelectModel{Provider: provider, Model: model}
			return nil
		}
	case "ctrl+d":
		if m.cursor >= len(list) {
			return m, nil
		}
		info := list[m.cursor]
		provider, model := modelRowProvider(info, m.provider), info.ID
		return m, saveDefaultsThroughCmd(m.settings, provider, model, "", "", "", provider+"/"+model)
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
		for i, info := range list {
			items[i] = ui.ListItem{
				Label:   modelPickerLabel(info),
				Detail:  formatModelDetail(th, info),
				Current: m.isCurrent(info),
			}
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
	titleDetail := "all providers"
	if n := modelProviderCount(m.all); n == 1 {
		if p := strings.TrimSpace(m.all[0].Provider); p != "" {
			titleDetail = p
		} else {
			titleDetail = m.provider
		}
	} else if n == 0 && m.provider != "" {
		titleDetail = m.provider
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: detailJoin(th, "Select model", titleDetail),
		Hint:  dotJoin(th, "type to filter or freeform id", "↑/↓ move", "enter select", "ctrl+d set default", "esc close"),
		Width: width,
	}, body)
}

func modelProviderCount(infos []host.ModelInfo) int {
	seen := map[string]bool{}
	for _, info := range infos {
		p := strings.TrimSpace(info.Provider)
		if p == "" {
			continue
		}
		seen[p] = true
	}
	return len(seen)
}

// formatModelDetail builds the muted trailing text for a picker row:
// "openai · id · 128k · $2.5/$10 · tools · reason · vision · 4 var".
// Missing fields omitted.
func formatModelDetail(th theme.Theme, info host.ModelInfo) string {
	var parts []string
	if p := strings.TrimSpace(info.Provider); p != "" {
		parts = append(parts, p)
	}
	label := modelPickerLabel(info)
	if label != info.ID {
		parts = append(parts, info.ID)
	}
	if info.Context > 0 {
		parts = append(parts, ui.FormatTokens(info.Context))
	}
	if info.HasCost {
		parts = append(parts, formatModelCost(info.InputCost, info.OutputCost))
	}
	if info.ToolCall {
		parts = append(parts, "tools")
	}
	if info.Reasoning {
		parts = append(parts, "reason")
	}
	if info.Attachment {
		parts = append(parts, "vision")
	}
	if n := len(info.VariantIDs); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" var")
	}
	return dotJoin(th, parts...)
}

// formatModelCost renders input/output USD-per-MTok as "$2.5/$10".
func formatModelCost(input, output float64) string {
	return formatUSD(input) + "/" + formatUSD(output)
}

func formatUSD(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		s = "0"
	}
	return "$" + s
}
