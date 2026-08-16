package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

const memoryModalVisible = 10

// memoryModal is a filterable browser for project memory entries (/memory list).
// Enter toggles a detail pane for the selected entry; esc closes from the list
// or returns from detail.
type memoryModal struct {
	all     []host.MemoryEntry
	filter  string
	cursor  int
	detail  bool
	tag     string
	loadErr string
}

func newMemoryModal(entries []host.MemoryEntry, tag string) *memoryModal {
	return &memoryModal{
		all: append([]host.MemoryEntry(nil), entries...),
		tag: strings.TrimSpace(tag),
	}
}

func (m *memoryModal) filtered() []host.MemoryEntry {
	if m.filter == "" {
		return m.all
	}
	q := strings.ToLower(m.filter)
	out := make([]host.MemoryEntry, 0, len(m.all))
	for _, e := range m.all {
		if memoryEntryMatches(e, q) {
			out = append(out, e)
		}
	}
	return out
}

func memoryEntryMatches(e host.MemoryEntry, q string) bool {
	if strings.Contains(strings.ToLower(e.Key), q) {
		return true
	}
	if strings.Contains(strings.ToLower(e.Value), q) {
		return true
	}
	for _, tag := range e.Tags {
		if strings.Contains(strings.ToLower(tag), q) {
			return true
		}
	}
	return false
}

func (m *memoryModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if m.detail {
		if isEscape(msg) || msg.String() == "enter" || msg.String() == "q" {
			m.detail = false
		}
		return m, nil
	}
	list := m.filtered()
	if isEscape(msg) || msg.String() == "q" {
		return nil, nil
	}
	switch msg.String() {
	case "up", "ctrl+p", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "ctrl+n", "j":
		if m.cursor < len(list)-1 {
			m.cursor++
		}
	case "backspace":
		if m.filter != "" {
			runes := []rune(m.filter)
			m.filter = string(runes[:len(runes)-1])
			m.cursor = 0
		}
	case "enter":
		if m.loadErr != "" || len(list) == 0 || m.cursor >= len(list) {
			return m, nil
		}
		m.detail = true
	default:
		if len(msg.Text) > 0 {
			m.filter += msg.Text
			m.cursor = 0
		}
	}
	return m, nil
}

func (m *memoryModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))
	title := "Memory"
	if m.tag != "" {
		title = detailJoin(th, "Memory", sanitizeDisplayData(m.tag))
	}

	if m.loadErr != "" {
		body := wrapToWidth(st.Error.Render(m.loadErr), inner)
		return ui.Dialog(th, ui.DialogOpts{
			Title: title,
			Hint:  "esc close",
			Width: width,
		}, body)
	}

	if m.detail {
		list := m.filtered()
		if m.cursor >= len(list) {
			m.cursor = max(0, len(list)-1)
		}
		if len(list) == 0 {
			m.detail = false
		} else {
			e := list[m.cursor]
			lines := []string{
				st.Accent.Render(sanitizeDisplayData(e.Key)),
				st.Text.Render(sanitizeDisplayData(e.Value)),
			}
			if len(e.Tags) > 0 {
				lines = append(lines, st.Muted.Render("tags: "+sanitizeDisplayData(strings.Join(e.Tags, ", "))))
			}
			body := wrapToWidth(strings.Join(lines, "\n"), inner)
			return ui.Dialog(th, ui.DialogOpts{
				Title: title,
				Hint:  dotJoin(th, "enter back", "esc back"),
				Width: width,
			}, body)
		}
	}

	list := m.filtered()
	if m.cursor >= len(list) {
		m.cursor = max(0, len(list)-1)
	}
	items := make([]ui.ListItem, len(list))
	for i, e := range list {
		detail := sanitizeDisplayData(e.Value)
		if len(e.Tags) > 0 {
			detail = detailJoin(th, detail, strings.Join(e.Tags, ", "))
		}
		items[i] = ui.ListItem{
			Label:  sanitizeDisplayData(e.Key),
			Detail: detail,
		}
	}
	empty := "no memory entries"
	if m.filter != "" {
		empty = "no matches for \"" + sanitizeDisplayData(m.filter) + "\""
	}
	body := ui.List(th, ui.ListOpts{
		Items:      items,
		Cursor:     m.cursor,
		Width:      inner,
		Visible:    memoryModalVisible,
		ShowFilter: true,
		Filter:     sanitizeDisplayData(m.filter),
		Total:      len(m.all),
		Empty:      empty,
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: title,
		Hint:  dotJoin(th, "type to filter", "↑/↓ move", "enter detail", "esc close"),
		Width: width,
	}, body)
}
