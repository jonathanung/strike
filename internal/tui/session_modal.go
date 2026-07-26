package tui

import (
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const sessionModalVisible = 10

// sessionResumeMsg requests a full process-level reopen of a root session
// (engine.Restore + transcript replay). Distinct from openSessionView, which
// only paints a subagent transcript in the left pane.
type sessionResumeMsg struct {
	id string
}

// sessionModal is the centered picker for past root sessions (/session).
// Titles come from durable auto-title metadata. Enter resumes via
// sessionResumeMsg so the composition root reopens with model history.
type sessionModal struct {
	current string
	all     []host.Session
	filter  string
	cursor  int
	loadErr string
	now     time.Time
}

func newSessionModal(sessions host.Sessions, current string) *sessionModal {
	m := &sessionModal{current: strings.TrimSpace(current), now: time.Now()}
	if sessions == nil {
		m.loadErr = "session list unavailable"
		return m
	}
	list, err := sessions.List(true)
	if err != nil {
		m.loadErr = err.Error()
		return m
	}
	// Optional cheap PR state refresh; cache stays on disk when offline/fails.
	if r, ok := sessions.(host.PRStateRefresher); ok {
		list = r.RefreshPRStates(list)
	}
	m.all = list
	for i, s := range m.all {
		if s.ID == m.current {
			m.cursor = i
			break
		}
	}
	return m
}

func (m *sessionModal) filtered() []host.Session {
	if m.filter == "" {
		return m.all
	}
	q := strings.ToLower(m.filter)
	var out []host.Session
	for _, s := range m.all {
		title := strings.ToLower(sessionPickerLabel(s))
		id := strings.ToLower(s.ID)
		if strings.Contains(title, q) || strings.Contains(id, q) {
			out = append(out, s)
		}
	}
	return out
}

func (m *sessionModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
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
		if m.loadErr != "" || len(list) == 0 {
			return m, nil
		}
		if m.cursor >= len(list) {
			return m, nil
		}
		id := list[m.cursor].ID
		if id == "" || id == m.current {
			return nil, nil
		}
		return nil, func() tea.Msg {
			return sessionResumeMsg{id: id}
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.filter += string(msg.Runes)
			m.cursor = 0
		}
		return m, nil
	}
}

func (m *sessionModal) view(width int, th theme.Theme) string {
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))

	var body string
	switch {
	case m.loadErr != "":
		body = wrapToWidth(st.Error.Render(m.loadErr), inner)
	case len(m.all) == 0:
		body = st.Muted.Render("no past sessions")
	default:
		list := m.filtered()
		if m.cursor >= len(list) {
			m.cursor = max(0, len(list)-1)
		}
		items := make([]ui.ListItem, len(list))
		for i, s := range list {
			items[i] = ui.ListItem{
				Label:   sessionPickerLabel(s),
				Detail:  sessionPickerDetail(th, s, m.now),
				Suffix:  sessionPRBadge(th, s),
				Current: s.ID == m.current,
			}
		}
		body = ui.List(th, ui.ListOpts{
			Items:      items,
			Cursor:     m.cursor,
			Width:      inner,
			Visible:    sessionModalVisible,
			ShowFilter: true,
			Filter:     m.filter,
			Total:      len(m.all),
			Empty:      "no matches for \"" + m.filter + "\"",
		})
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Resume session",
		Hint:  dotJoin(th, "type to filter", "↑/↓ move", "enter resume", "esc close"),
		Width: width,
	}, body)
}

func sessionPickerLabel(s host.Session) string {
	if t := strings.TrimSpace(s.Title); t != "" {
		return t
	}
	return "untitled"
}

func sessionPickerDetail(th theme.Theme, s host.Session, now time.Time) string {
	parts := []string{shortSessionID(s.ID)}
	if !s.UpdatedAt.IsZero() {
		parts = append(parts, formatRelativeTime(now, s.UpdatedAt))
	}
	if s.Open {
		parts = append(parts, "open")
	}
	return dotJoin(th, parts...)
}

// sessionPRBadge returns a compact theme-token badge for a linked PR, with an
// OSC 8 hyperlink when pr_url is set. Empty when the session has no PR.
func sessionPRBadge(th theme.Theme, s host.Session) string {
	if s.PRURL == "" && s.PRNumber == 0 {
		return ""
	}
	th = th.Resolve()
	state := strings.ToLower(strings.TrimSpace(s.PRState))
	tone := ui.ToneAccent
	label := "pr"
	switch state {
	case "open":
		tone = ui.ToneAccent
		label = "open"
	case "merged":
		tone = ui.ToneSuccess
		label = "merged"
	case "closed":
		tone = ui.ToneMuted
		label = "closed"
	}
	if s.PRNumber > 0 {
		label = "#" + strconv.Itoa(s.PRNumber) + " " + label
	}
	badge := ui.Badge(th, tone, label)
	if s.PRURL != "" {
		return withHyperlink(s.PRURL, badge)
	}
	return badge
}

// formatRelativeTime is a short "5m ago" / "3h ago" / "2d ago" label for pickers.
func formatRelativeTime(now, then time.Time) string {
	if then.IsZero() {
		return ""
	}
	if now.IsZero() {
		now = time.Now()
	}
	d := now.Sub(then)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m <= 1 {
			return "1m ago"
		}
		return strconv.Itoa(m) + "m ago"
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h <= 1 {
			return "1h ago"
		}
		return strconv.Itoa(h) + "h ago"
	default:
		days := int(d.Hours() / 24)
		if days <= 1 {
			return "1d ago"
		}
		return strconv.Itoa(days) + "d ago"
	}
}
