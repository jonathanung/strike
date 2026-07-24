package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
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
			if err := config.ValidateAgentName(name); err != nil {
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
		if err := config.ValidateSkillName(name); err != nil || spec.Name != "/"+name {
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
	width = max(0, width)
	innerWidth := max(1, width-4)
	title := lipgloss.NewStyle().Foreground(th.Accent).Bold(true).Render("Command palette")
	muted := lipgloss.NewStyle().Foreground(th.TextMuted)
	normal := lipgloss.NewStyle().Foreground(th.Text)
	selected := lipgloss.NewStyle().Foreground(th.Accent).Bold(true)

	filterLine := muted.Render("filter: ") + normal.Render(truncateDisplay(sanitizeDisplayData(m.filter)+"▏", max(1, innerWidth-8)))
	list := m.filtered()
	if m.cursor >= len(list) {
		m.cursor = max(0, len(list)-1)
	}
	rows := min(paletteMaxRows, len(list))
	start := max(0, min(m.cursor-rows/2, len(list)-rows))
	end := min(len(list), start+rows)
	lines := make([]string, 0, max(1, rows))
	for i := start; i < end; i++ {
		entry := list[i]
		marker := "  "
		labelStyle := normal
		if i == m.cursor {
			marker = "▸ "
		}
		if entry.DisabledReason != "" {
			labelStyle = muted
		} else if i == m.cursor {
			labelStyle = selected
		}
		detail := entry.Description
		if entry.DisabledReason != "" {
			detail = entry.DisabledReason
		}
		plain := marker + entry.Label
		if detail != "" {
			plain += " — " + detail
		}
		plain = truncateDisplay(plain, innerWidth)

		line := marker + labelStyle.Render(entry.Label)
		if detail != "" {
			line += muted.Render(" — " + detail)
		}
		if lipgloss.Width(line) > innerWidth {
			line = labelStyle.Render(plain)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = append(lines, muted.Render(truncateDisplay("no matching actions", innerWidth)))
	}

	hint := muted.Render(truncateDisplay("type to filter · ↑/↓ move · enter select · esc close", innerWidth))
	body := title + "\n" + filterLine + "\n\n" + strings.Join(lines, "\n") + "\n\n" + hint
	if width < 4 {
		return body
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(th.BorderFocus).
		Padding(0, 1).
		Width(max(1, width-2)).
		Render(body)
}
