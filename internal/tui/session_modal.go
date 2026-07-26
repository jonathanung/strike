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
// Default list is scoped to the current launch project; ctrl+a toggles
// all-projects mode when the host supports it.
type sessionModal struct {
	sessions    host.Sessions
	current     string
	all         []host.Session
	filter      string
	cursor      int
	loadErr     string
	now         time.Time
	allProjects bool
	canAll      bool
}

func newSessionModal(sessions host.Sessions, current string) *sessionModal {
	m := &sessionModal{
		sessions: sessions,
		current:  strings.TrimSpace(current),
		now:      time.Now(),
	}
	if sessions == nil {
		m.loadErr = "session list unavailable"
		return m
	}
	_, m.canAll = sessions.(host.AllProjectsSessions)
	m.reload()
	return m
}

func (m *sessionModal) reload() {
	m.loadErr = ""
	if m.sessions == nil {
		m.loadErr = "session list unavailable"
		m.all = nil
		return
	}
	var (
		list []host.Session
		err  error
	)
	if m.allProjects {
		if all, ok := m.sessions.(host.AllProjectsSessions); ok {
			list, err = all.ListAllProjects(true)
		} else {
			list, err = m.sessions.List(true)
		}
	} else {
		list, err = m.sessions.List(true)
	}
	if err != nil {
		m.loadErr = err.Error()
		m.all = nil
		return
	}
	// Optional cheap PR state refresh; cache stays on disk when offline/fails.
	if r, ok := m.sessions.(host.PRStateRefresher); ok {
		list = r.RefreshPRStates(list)
	}
	m.all = list
	m.cursor = 0
	for i, s := range m.all {
		if s.ID == m.current {
			m.cursor = i
			break
		}
	}
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
		proj := strings.ToLower(s.ProjectKey)
		if strings.Contains(title, q) || strings.Contains(id, q) || strings.Contains(proj, q) {
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
	case "ctrl+a":
		if !m.canAll {
			return m, nil
		}
		m.allProjects = !m.allProjects
		m.filter = ""
		m.reload()
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
		if m.allProjects {
			body = st.Muted.Render("no past sessions")
		} else {
			body = st.Muted.Render("no sessions for this project")
		}
	default:
		list := m.filtered()
		if m.cursor >= len(list) {
			m.cursor = max(0, len(list)-1)
		}
		items := make([]ui.ListItem, len(list))
		for i, s := range list {
			items[i] = ui.ListItem{
				Label:   sessionPickerLabel(s),
				Detail:  sessionPickerDetail(th, s, m.now, m.allProjects),
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
	title := "Resume session"
	if m.allProjects {
		title = "Resume session (all projects)"
	}
	hints := []string{"type to filter", "↑/↓ move", "enter resume", "esc close"}
	if m.canAll {
		if m.allProjects {
			hints = append([]string{"ctrl+a this project"}, hints...)
		} else {
			hints = append([]string{"ctrl+a all projects"}, hints...)
		}
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: title,
		Hint:  dotJoin(th, hints...),
		Width: width,
	}, body)
}

func sessionPickerLabel(s host.Session) string {
	if t := strings.TrimSpace(s.Title); t != "" {
		return t
	}
	return "untitled"
}

func sessionPickerDetail(th theme.Theme, s host.Session, now time.Time, showProject bool) string {
	parts := []string{shortSessionID(s.ID)}
	if !s.UpdatedAt.IsZero() {
		parts = append(parts, formatRelativeTime(now, s.UpdatedAt))
	}
	if s.Open {
		parts = append(parts, "open")
	}
	if showProject {
		if p := sessionProjectLabel(s.ProjectKey); p != "" {
			parts = append(parts, p)
		}
	}
	return dotJoin(th, parts...)
}

// sessionProjectLabel shortens a project key (usually an absolute path) for the
// picker detail column.
func sessionProjectLabel(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "unknown project"
	}
	// Prefer the final path segment when the key looks like a filesystem path.
	if i := strings.LastIndexAny(key, `/\`); i >= 0 && i+1 < len(key) {
		return key[i+1:]
	}
	return key
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
