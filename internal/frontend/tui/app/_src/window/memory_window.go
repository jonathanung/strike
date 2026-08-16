package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

const memoryWindowID = "memory"

// memoryWindow is the right-pane browser for project-local memory entries.
type memoryWindow struct {
	mem     host.Memory
	tag     string
	entries []host.MemoryEntry
	cursor  int
	detail  bool
	width   int
	height  int
	err     string
}

func newMemoryWindow() memoryWindow {
	return memoryWindow{}
}

func (w memoryWindow) id() string { return memoryWindowID }

func (w memoryWindow) title() string {
	if w.tag != "" {
		return "memory"
	}
	return "memory"
}

func (w memoryWindow) init() tea.Cmd { return nil }

func (w memoryWindow) update(msg tea.Msg) (window, tea.Cmd) {
	switch msg := msg.(type) {
	case projectDataRefreshMsg:
		return w.reload(), nil
	case tea.KeyPressMsg:
		return w.handleKey(msg)
	}
	return w, nil
}

func (w memoryWindow) resize(width, height int) window {
	w.width, w.height = max(0, width), max(0, height)
	return w
}

func (w memoryWindow) view(th theme.Theme) string {
	if w.width <= 0 {
		return ""
	}
	th = th.Resolve()
	st := th.S()
	if w.mem == nil {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render("memory unavailable"),
		)
	}
	if w.err != "" && len(w.entries) == 0 {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Error.Render(welcomeTruncate(w.err, w.width, th.Icons.Ellipsis)),
		)
	}
	if w.detail {
		return w.viewDetail(th)
	}
	visible := w.height
	if visible < 1 {
		visible = 0
	}
	items := make([]ui.ListItem, len(w.entries))
	for i, e := range w.entries {
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
	if w.tag != "" {
		empty = "no entries with tag " + sanitizeDisplayData(w.tag)
	}
	return ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  w.cursor,
		Width:   w.width,
		Visible: visible,
		Empty:   empty,
	})
}

func (w memoryWindow) viewDetail(th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	if w.cursor < 0 || w.cursor >= len(w.entries) {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render("no entry selected"),
		)
	}
	e := w.entries[w.cursor]
	lines := []string{
		st.Accent.Render(sanitizeDisplayData(e.Key)),
		st.Text.Render(sanitizeDisplayData(e.Value)),
	}
	if len(e.Tags) > 0 {
		lines = append(lines, st.Muted.Render("tags: "+sanitizeDisplayData(strings.Join(e.Tags, ", "))))
	}
	body := wrapToWidth(strings.Join(lines, "\n"), w.width)
	if w.height > 0 {
		parts := strings.Split(body, "\n")
		if len(parts) > w.height {
			parts = parts[:w.height]
			body = strings.Join(parts, "\n")
		}
	}
	return body
}

func (w memoryWindow) bind(mem host.Memory, tag string) memoryWindow {
	w.mem = mem
	w.tag = strings.TrimSpace(tag)
	w.detail = false
	return w.reload()
}

func (w memoryWindow) reload() memoryWindow {
	if w.mem == nil {
		w.entries = nil
		w.err = ""
		w.cursor = 0
		w.detail = false
		return w
	}
	entries, err := w.mem.List(w.tag)
	if err != nil {
		w.err = err.Error()
		w.entries = nil
		w.cursor = 0
		w.detail = false
		return w
	}
	w.err = ""
	w.entries = append([]host.MemoryEntry(nil), entries...)
	if len(w.entries) == 0 {
		w.cursor = 0
		w.detail = false
	} else if w.cursor >= len(w.entries) {
		w.cursor = len(w.entries) - 1
	} else if w.cursor < 0 {
		w.cursor = 0
	}
	return w
}

func (w memoryWindow) handleKey(msg tea.KeyPressMsg) (memoryWindow, tea.Cmd) {
	if w.mem == nil {
		return w, nil
	}
	if w.detail {
		switch msg.String() {
		case "enter", "esc", "q", "left", "h":
			w.detail = false
		case "up", "k":
			if w.cursor > 0 {
				w.cursor--
			}
		case "down", "j":
			if w.cursor < len(w.entries)-1 {
				w.cursor++
			}
		}
		return w, nil
	}
	switch msg.String() {
	case "up", "k":
		if w.cursor > 0 {
			w.cursor--
		}
	case "down", "j":
		if w.cursor < len(w.entries)-1 {
			w.cursor++
		}
	case "enter", "right", "l":
		if len(w.entries) > 0 && w.cursor >= 0 && w.cursor < len(w.entries) {
			w.detail = true
		}
	case "r":
		w = w.reload()
	case "d":
		return w.deleteSelected()
	}
	return w, nil
}

func (w memoryWindow) deleteSelected() (memoryWindow, tea.Cmd) {
	if w.cursor < 0 || w.cursor >= len(w.entries) {
		return w, nil
	}
	key := w.entries[w.cursor].Key
	if err := w.mem.Delete(key); err != nil {
		w.err = err.Error()
		return w, nil
	}
	w = w.reload()
	return w, func() tea.Msg {
		return projectDataMutatedMsg{kind: "memory", notice: "memory: deleted " + key}
	}
}

// configureMemoryWindow binds host.Memory onto the memory window slot.
func configureMemoryWindow(r windowRegistry, mem host.Memory) windowRegistry {
	for i, w := range r.windows {
		mw, ok := w.(memoryWindow)
		if !ok {
			continue
		}
		next := mw.bind(mem, mw.tag)
		windows := append([]window(nil), r.windows...)
		windows[i] = next
		r.windows = windows
		return r
	}
	return r
}
