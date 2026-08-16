package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const issuesModalVisible = 10

// issuesModal is a filterable browser for project issues (/issues list).
// Enter toggles a detail pane for the selected issue; esc closes from the list
// or returns from detail.
type issuesModal struct {
	all     []host.Issue
	filter  string
	cursor  int
	detail  bool
	status  string
	loadErr string
}

func newIssuesModal(items []host.Issue, status string) *issuesModal {
	return &issuesModal{
		all:    append([]host.Issue(nil), items...),
		status: strings.TrimSpace(status),
	}
}

func (m *issuesModal) filtered() []host.Issue {
	if m.filter == "" {
		return m.all
	}
	q := strings.ToLower(m.filter)
	out := make([]host.Issue, 0, len(m.all))
	for _, iss := range m.all {
		if issueMatches(iss, q) {
			out = append(out, iss)
		}
	}
	return out
}

func issueMatches(iss host.Issue, q string) bool {
	if strings.Contains(strings.ToLower(iss.Title), q) {
		return true
	}
	if strings.Contains(strings.ToLower(iss.Body), q) {
		return true
	}
	if strings.Contains(strings.ToLower(iss.Status), q) {
		return true
	}
	id := fmt.Sprintf("#%d", iss.ID)
	return strings.Contains(id, q) || strings.Contains(fmt.Sprintf("%d", iss.ID), q)
}

func (m *issuesModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
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

func (m *issuesModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))
	title := "Issues"
	if m.status != "" {
		title = detailJoin(th, "Issues", sanitizeDisplayData(m.status))
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
			iss := list[m.cursor]
			lines := []string{
				st.Accent.Render(fmt.Sprintf("#%d", iss.ID)) + st.Muted.Render(" ["+sanitizeDisplayData(iss.Status)+"]"),
				st.Text.Render(sanitizeDisplayData(iss.Title)),
			}
			if iss.Body != "" {
				lines = append(lines, st.Muted.Render(sanitizeDisplayData(iss.Body)))
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
	for i, iss := range list {
		items[i] = ui.ListItem{
			Label:  fmt.Sprintf("#%d", iss.ID),
			Detail: detailJoin(th, sanitizeDisplayData(iss.Status), sanitizeDisplayData(iss.Title)),
		}
	}
	empty := "no issues"
	if m.filter != "" {
		empty = "no matches for \"" + sanitizeDisplayData(m.filter) + "\""
	} else if m.status != "" {
		empty = "no " + sanitizeDisplayData(m.status) + " issues"
	}
	body := ui.List(th, ui.ListOpts{
		Items:      items,
		Cursor:     m.cursor,
		Width:      inner,
		Visible:    issuesModalVisible,
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
