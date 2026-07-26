package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const helpModalMaxRows = 10

type helpEntry struct {
	Label       string
	Description string
}

// helpModal is a filterable slash-command cheatsheet built from the live catalog.
type helpModal struct {
	entries []helpEntry
	filter  string
	cursor  int
}

func newHelpModal(specs []commandSpec) *helpModal {
	entries := make([]helpEntry, 0, len(specs)+1)
	for _, spec := range specs {
		label := sanitizeDisplayData(spec.Name)
		if hint := strings.TrimSpace(spec.ArgsHint); hint != "" {
			label = label + " " + sanitizeDisplayData(hint)
		}
		entries = append(entries, helpEntry{
			Label:       label,
			Description: sanitizeDisplayData(spec.Description),
		})
	}
	entries = append(entries, helpEntry{
		Label:       "tab",
		Description: "cycle agents",
	})
	return &helpModal{entries: entries}
}

func (m *helpModal) filtered() []helpEntry {
	query := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(m.filter, "/")))
	if query == "" {
		return m.entries
	}
	buckets := [3][]helpEntry{}
	for _, entry := range m.entries {
		rank := helpMatchRank(entry, query)
		if rank >= 0 {
			buckets[rank] = append(buckets[rank], entry)
		}
	}
	matches := make([]helpEntry, 0, len(m.entries))
	for _, bucket := range buckets {
		matches = append(matches, bucket...)
	}
	return matches
}

func helpMatchRank(entry helpEntry, query string) int {
	fields := []string{entry.Label, entry.Description}
	best := -1
	for _, field := range fields {
		field = strings.ToLower(strings.TrimPrefix(field, "/"))
		rank := -1
		switch {
		case field == query:
			rank = 0
		case strings.HasPrefix(field, query):
			rank = 1
		case strings.Contains(field, query):
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

func (m *helpModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	list := m.filtered()
	if isEscape(msg) || msg.String() == "q" {
		return nil, nil
	}
	switch msg.String() {
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
		return nil, nil
	default:
		if msg.Type == tea.KeyRunes {
			m.filter += string(msg.Runes)
			m.cursor = 0
		}
	}
	return m, nil
}

func (m *helpModal) view(width int, th theme.Theme) string {
	list := m.filtered()
	if m.cursor >= len(list) {
		m.cursor = max(0, len(list)-1)
	}
	items := make([]ui.ListItem, len(list))
	for i, entry := range list {
		items[i] = ui.ListItem{
			Label:  entry.Label,
			Detail: entry.Description,
		}
	}

	listWidth := max(1, ui.PanelInnerWidth(th, width))
	if width < 4 {
		listWidth = max(1, width)
	}
	body := ui.List(th, ui.ListOpts{
		Items:      items,
		Cursor:     m.cursor,
		Width:      listWidth,
		Visible:    helpModalMaxRows,
		ShowFilter: true,
		Filter:     sanitizeDisplayData(m.filter),
		Total:      len(m.entries),
		Empty:      "no matching commands",
	})
	if width < 4 {
		return body
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Commands",
		Hint:  dotJoin(th, "type to filter", "up/down move", "esc close"),
		Width: width,
	}, body)
}
