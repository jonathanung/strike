package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const issuesWindowID = "issues"

// issuesWindow is the right-pane browser for project-local issues.
type issuesWindow struct {
	store  host.Issues
	status string
	items  []host.Issue
	cursor int
	detail bool
	width  int
	height int
	err    string
}

func newIssuesWindow() issuesWindow {
	return issuesWindow{}
}

func (w issuesWindow) id() string { return issuesWindowID }

func (w issuesWindow) title() string { return "issues" }

func (w issuesWindow) init() tea.Cmd { return nil }

func (w issuesWindow) update(msg tea.Msg) (window, tea.Cmd) {
	switch msg := msg.(type) {
	case projectDataRefreshMsg:
		return w.reload(), nil
	case tea.KeyPressMsg:
		return w.handleKey(msg)
	}
	return w, nil
}

func (w issuesWindow) resize(width, height int) window {
	w.width, w.height = max(0, width), max(0, height)
	return w
}

func (w issuesWindow) view(th theme.Theme) string {
	if w.width <= 0 {
		return ""
	}
	th = th.Resolve()
	st := th.S()
	if w.store == nil {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render("issues unavailable"),
		)
	}
	if w.err != "" && len(w.items) == 0 {
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
	items := make([]ui.ListItem, len(w.items))
	for i, iss := range w.items {
		items[i] = ui.ListItem{
			Label:  fmt.Sprintf("#%d", iss.ID),
			Detail: detailJoin(th, sanitizeDisplayData(iss.Status), sanitizeDisplayData(iss.Title)),
		}
	}
	empty := "no issues"
	if w.status != "" {
		empty = "no " + sanitizeDisplayData(w.status) + " issues"
	}
	return ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  w.cursor,
		Width:   w.width,
		Visible: visible,
		Empty:   empty,
	})
}

func (w issuesWindow) viewDetail(th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	if w.cursor < 0 || w.cursor >= len(w.items) {
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render("no issue selected"),
		)
	}
	iss := w.items[w.cursor]
	lines := []string{
		st.Accent.Render(fmt.Sprintf("#%d", iss.ID)) + st.Muted.Render(" ["+sanitizeDisplayData(iss.Status)+"]"),
		st.Text.Render(sanitizeDisplayData(iss.Title)),
	}
	if iss.Body != "" {
		lines = append(lines, st.Muted.Render(sanitizeDisplayData(iss.Body)))
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

func (w issuesWindow) bind(store host.Issues, status string) issuesWindow {
	w.store = store
	w.status = strings.TrimSpace(status)
	w.detail = false
	return w.reload()
}

func (w issuesWindow) reload() issuesWindow {
	if w.store == nil {
		w.items = nil
		w.err = ""
		w.cursor = 0
		w.detail = false
		return w
	}
	items, err := w.store.List(w.status)
	if err != nil {
		w.err = err.Error()
		w.items = nil
		w.cursor = 0
		w.detail = false
		return w
	}
	w.err = ""
	w.items = append([]host.Issue(nil), items...)
	if len(w.items) == 0 {
		w.cursor = 0
		w.detail = false
	} else if w.cursor >= len(w.items) {
		w.cursor = len(w.items) - 1
	} else if w.cursor < 0 {
		w.cursor = 0
	}
	return w
}

func (w issuesWindow) handleKey(msg tea.KeyPressMsg) (issuesWindow, tea.Cmd) {
	if w.store == nil {
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
			if w.cursor < len(w.items)-1 {
				w.cursor++
			}
		case "c":
			return w.setSelectedStatus("closed")
		case "o":
			return w.setSelectedStatus("open")
		}
		return w, nil
	}
	switch msg.String() {
	case "up", "k":
		if w.cursor > 0 {
			w.cursor--
		}
	case "down", "j":
		if w.cursor < len(w.items)-1 {
			w.cursor++
		}
	case "enter", "right", "l":
		if len(w.items) > 0 && w.cursor >= 0 && w.cursor < len(w.items) {
			w.detail = true
		}
	case "r":
		w = w.reload()
	case "c":
		return w.setSelectedStatus("closed")
	case "o":
		return w.setSelectedStatus("open")
	}
	return w, nil
}

func (w issuesWindow) setSelectedStatus(status string) (issuesWindow, tea.Cmd) {
	if w.cursor < 0 || w.cursor >= len(w.items) {
		return w, nil
	}
	iss := w.items[w.cursor]
	if iss.Status == status {
		return w, nil
	}
	updated, err := w.store.Update(iss.ID, nil, nil, &status)
	if err != nil {
		w.err = err.Error()
		return w, nil
	}
	w = w.reload()
	verb := "closed"
	if status == "open" {
		verb = "reopened"
	}
	return w, func() tea.Msg {
		return projectDataMutatedMsg{
			kind:   "issues",
			notice: fmt.Sprintf("issues: %s #%d %s", verb, updated.ID, updated.Title),
		}
	}
}

// configureIssuesWindow binds host.Issues onto the issues window slot.
func configureIssuesWindow(r windowRegistry, store host.Issues) windowRegistry {
	for i, w := range r.windows {
		iw, ok := w.(issuesWindow)
		if !ok {
			continue
		}
		next := iw.bind(store, iw.status)
		windows := append([]window(nil), r.windows...)
		windows[i] = next
		r.windows = windows
		return r
	}
	return r
}
