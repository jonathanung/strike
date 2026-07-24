package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const paletteMaxRows = 8

// paletteAvailability is the state needed to determine whether palette
// actions can currently be invoked.
type paletteAvailability struct {
	HasProvider bool
	TurnRunning bool
}

type paletteEntry struct {
	ID             string
	Label          string
	Description    string
	Action         paletteAction
	DisabledReason string
}

type paletteActionKind uint8

const (
	paletteActionUnknown paletteActionKind = iota
	paletteActionBuiltin
	paletteActionAgent
	paletteActionSkill
)

type paletteAction struct {
	Kind  paletteActionKind
	Value string
}

type paletteInvokeMsg struct {
	Action paletteAction
}

type paletteModal struct {
	entries []paletteEntry
	filter  string
	cursor  int
}

func newPaletteModal(specs []commandSpec, agents []string, availability paletteAvailability) *paletteModal {
	return &paletteModal{entries: buildPaletteEntries(specs, agents, availability)}
}

func (m *paletteModal) refresh(entries []paletteEntry) {
	oldCursor := m.cursor
	selectedID := ""
	if filtered := m.filtered(); oldCursor >= 0 && oldCursor < len(filtered) {
		selectedID = filtered[oldCursor].ID
	}

	m.entries = entries
	filtered := m.filtered()
	for i, entry := range filtered {
		if entry.ID == selectedID {
			m.cursor = i
			return
		}
	}
	m.cursor = max(0, min(oldCursor, len(filtered)-1))
}

func buildPaletteEntries(specs []commandSpec, agents []string, availability paletteAvailability) []paletteEntry {
	byID := make(map[commandID]commandSpec, len(specs))
	for _, spec := range specs {
		if spec.Source == commandSourceBuiltin {
			byID[spec.ID] = spec
		}
	}

	entries := make([]paletteEntry, 0, 4+len(agents)+len(specs))
	for _, id := range []commandID{commandProvider, commandModel, commandAuth} {
		spec, ok := byID[id]
		if !ok {
			continue
		}
		entry := paletteEntry{
			ID:          "command:" + string(id),
			Label:       sanitizeDisplayData(spec.Name),
			Description: sanitizeDisplayData(spec.Description),
			Action:      paletteAction{Kind: paletteActionBuiltin, Value: spec.Name},
		}
		switch {
		case availability.TurnRunning:
			entry.DisabledReason = "unavailable while a turn is running"
		case id == commandModel && !availability.HasProvider:
			entry.DisabledReason = "select a provider first"
		}
		entries = append(entries, entry)
	}

	agentSpec, hasAgentSpec := byID[commandAgent]
	if hasAgentSpec {
		for _, name := range agents {
			if !validAgentName(name) {
				continue
			}
			displayName := sanitizeDisplayData(name)
			entry := paletteEntry{
				ID:          "agent:" + name,
				Label:       "/agent " + displayName,
				Description: sanitizeDisplayData(agentSpec.Description),
				Action:      paletteAction{Kind: paletteActionAgent, Value: name},
			}
			if availability.TurnRunning {
				entry.DisabledReason = "unavailable while a turn is running"
			}
			entries = append(entries, entry)
		}
	}

	if spec, ok := byID[commandHelp]; ok {
		entries = append(entries, paletteEntry{
			ID:          "command:" + string(commandHelp),
			Label:       sanitizeDisplayData(spec.Name),
			Description: sanitizeDisplayData(spec.Description),
			Action:      paletteAction{Kind: paletteActionBuiltin, Value: spec.Name},
		})
	}

	for _, spec := range specs {
		if spec.Source != commandSourceSkill {
			continue
		}
		name := strings.TrimPrefix(spec.Name, "/")
		if !validSkillName(name) || spec.Name != "/"+name {
			continue
		}
		entry := paletteEntry{
			ID:          "skill:" + name,
			Label:       sanitizeDisplayData(spec.Name),
			Description: sanitizeDisplayData(spec.Description),
			Action:      paletteAction{Kind: paletteActionSkill, Value: name},
		}
		if availability.TurnRunning {
			entry.DisabledReason = "unavailable while a turn is running"
		} else if !availability.HasProvider {
			entry.DisabledReason = "select a provider first"
		}
		entries = append(entries, entry)
	}
	return entries
}

func (m *paletteModal) filtered() []paletteEntry {
	query := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(m.filter, "/")))
	if query == "" {
		return m.entries
	}

	buckets := [3][]paletteEntry{}
	for _, entry := range m.entries {
		rank := paletteMatchRank(entry, query)
		if rank >= 0 {
			buckets[rank] = append(buckets[rank], entry)
		}
	}
	matches := make([]paletteEntry, 0, len(m.entries))
	for _, bucket := range buckets {
		matches = append(matches, bucket...)
	}
	return matches
}

func paletteMatchRank(entry paletteEntry, query string) int {
	fields := []string{entry.Label, entry.Action.Value, entry.Description}
	best := -1
	for _, field := range fields {
		field = strings.ToLower(strings.TrimPrefix(field, "/"))
		rank := -1
		switch {
		case field == query:
			rank = 0
		case strings.HasPrefix(field, query):
			rank = 1
		case orderedSubsequence(field, query):
			rank = 2
		}
		if rank >= 0 && (best < 0 || rank < best) {
			best = rank
		}
	}
	return best
}

func (m *paletteModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	list := m.filtered()
	switch msg.String() {
	case "esc":
		return nil, nil
	case "up", "ctrl+p":
		if len(list) > 0 {
			m.cursor = (m.cursor + len(list) - 1) % len(list)
		}
	case "down", "ctrl+n":
		if len(list) > 0 {
			m.cursor = (m.cursor + 1) % len(list)
		}
	case "backspace":
		runes := []rune(m.filter)
		if len(runes) > 0 {
			m.filter = string(runes[:len(runes)-1])
			m.cursor = 0
		}
	case "enter":
		if m.cursor >= len(list) || list[m.cursor].DisabledReason != "" {
			return m, nil
		}
		entry := list[m.cursor]
		return nil, func() tea.Msg {
			return paletteInvokeMsg{Action: entry.Action}
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.filter += string(msg.Runes)
			m.cursor = 0
		}
	}
	return m, nil
}

func (m *paletteModal) view(width int, th theme.Theme) string {
	list := m.filtered()
	if m.cursor >= len(list) {
		m.cursor = max(0, len(list)-1)
	}
	items := make([]ui.ListItem, len(list))
	for i, entry := range list {
		detail := entry.Description
		if entry.DisabledReason != "" {
			detail = entry.DisabledReason
		}
		items[i] = ui.ListItem{
			Label:    entry.Label,
			Detail:   detail,
			Disabled: entry.DisabledReason != "",
		}
	}

	listWidth := max(1, ui.InnerWidth(width))
	if width < 4 {
		listWidth = max(1, width) // too narrow for a dialog frame
	}
	body := ui.List(th, ui.ListOpts{
		Items:      items,
		Cursor:     m.cursor,
		Width:      listWidth,
		Visible:    paletteMaxRows,
		ShowFilter: true,
		Filter:     sanitizeDisplayData(m.filter),
		Total:      len(m.entries),
		Empty:      "no matching actions",
	})
	if width < 4 {
		return body
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Command palette",
		Hint:  "type to filter · ↑/↓ move · enter select · esc close",
		Width: width,
	}, body)
}
