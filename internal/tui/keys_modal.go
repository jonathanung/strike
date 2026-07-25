package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const keysModalMaxRows = 10

// keysModal is a filterable keybind cheatsheet.
type keysModal struct {
	entries []keybindEntry
	filter  string
	cursor  int
}

func newKeysModal(keys keyMap) *keysModal {
	return &keysModal{entries: keybindCatalog(keys)}
}

func (m *keysModal) filtered() []keybindEntry {
	query := strings.ToLower(strings.TrimSpace(m.filter))
	if query == "" {
		return m.entries
	}
	buckets := [3][]keybindEntry{}
	for _, entry := range m.entries {
		rank := keysMatchRank(entry, query)
		if rank >= 0 {
			buckets[rank] = append(buckets[rank], entry)
		}
	}
	matches := make([]keybindEntry, 0, len(m.entries))
	for _, bucket := range buckets {
		matches = append(matches, bucket...)
	}
	return matches
}

func keysMatchRank(entry keybindEntry, query string) int {
	fields := []string{entry.Keys, entry.Action, entry.Category, entry.ID}
	best := -1
	for _, field := range fields {
		field = strings.ToLower(field)
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

func (m *keysModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	list := m.filtered()
	if isEscape(msg) || msg.String() == "q" || msg.String() == "f1" {
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
		// Informational only — enter closes like esc.
		return nil, nil
	default:
		if msg.Type == tea.KeyRunes {
			m.filter += string(msg.Runes)
			m.cursor = 0
		}
	}
	return m, nil
}

func (m *keysModal) view(width int, th theme.Theme) string {
	list := m.filtered()
	if m.cursor >= len(list) {
		m.cursor = max(0, len(list)-1)
	}
	items := make([]ui.ListItem, len(list))
	for i, entry := range list {
		items[i] = ui.ListItem{
			Label:  sanitizeDisplayData(entry.Keys),
			Detail: sanitizeDisplayData(detailJoin(th, entry.Category, entry.Action)),
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
		Visible:    keysModalMaxRows,
		ShowFilter: true,
		Filter:     sanitizeDisplayData(m.filter),
		Total:      len(m.entries),
		Empty:      "no matching keybinds",
	})
	if width < 4 {
		return body
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Keyboard shortcuts",
		Hint:  dotJoin(th, "type to filter", "up/down move", "esc close"),
		Width: width,
	}, body)
}
