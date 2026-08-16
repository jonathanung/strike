package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

const sessionModalVisible = 10

// sessionResumeMsg requests a full process-level reopen of a root session
// (engine.Restore + transcript replay). Distinct from openSessionView, which
// only paints a subagent transcript in the left pane.
type sessionResumeMsg struct {
	id string
}

type sessionModalPhase int

const (
	sessionPhaseBrowse sessionModalPhase = iota
	sessionPhaseRename
	sessionPhaseConfirmDelete
)

// sessionModal is the centered picker for past root sessions (/session).
// Titles come from durable auto-title metadata. Enter resumes via
// sessionResumeMsg so the composition root reopens with model history.
// Default list is scoped to the current launch project; ctrl+a toggles
// all-projects mode when the host supports it. Type filters live; ctrl+r
// renames; ctrl+x deletes with confirm (force required for open/active).
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
	phase       sessionModalPhase
	renameBuf   string
	deleteID    string
	deleteForce bool
	statusErr   string // transient action error (rename/delete)
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
	m.selectCurrentOnOpen()
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
	if m.cursor >= len(m.all) {
		m.cursor = max(0, len(m.all)-1)
	}
}

func (m *sessionModal) selectCurrentOnOpen() {
	for i, s := range m.all {
		if s.ID == m.current {
			m.cursor = i
			return
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

func (m *sessionModal) selected() (host.Session, bool) {
	list := m.filtered()
	if len(list) == 0 || m.cursor < 0 || m.cursor >= len(list) {
		return host.Session{}, false
	}
	return list[m.cursor], true
}

func (m *sessionModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	switch m.phase {
	case sessionPhaseRename:
		return m.updateRename(msg)
	case sessionPhaseConfirmDelete:
		return m.updateConfirmDelete(msg)
	default:
		return m.updateBrowse(msg)
	}
}

func (m *sessionModal) updateBrowse(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	list := m.filtered()
	if isEscape(msg) {
		return nil, nil
	}
	switch msg.String() {
	case "up", "ctrl+p":
		m.statusErr = ""
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "ctrl+n":
		m.statusErr = ""
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
		m.statusErr = ""
		m.reload()
		m.selectCurrentOnOpen()
		return m, nil
	case "ctrl+r":
		s, ok := m.selected()
		if !ok || m.loadErr != "" {
			return m, nil
		}
		m.phase = sessionPhaseRename
		m.renameBuf = strings.TrimSpace(s.Title)
		m.statusErr = ""
		return m, nil
	case "ctrl+x":
		s, ok := m.selected()
		if !ok || m.loadErr != "" {
			return m, nil
		}
		m.phase = sessionPhaseConfirmDelete
		m.deleteID = s.ID
		m.deleteForce = false
		m.statusErr = ""
		return m, nil
	case "backspace":
		m.statusErr = ""
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
		if len(msg.Text) > 0 {
			m.statusErr = ""
			m.filter += msg.Text
			m.cursor = 0
		}
		return m, nil
	}
}

func (m *sessionModal) updateRename(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) {
		m.phase = sessionPhaseBrowse
		m.renameBuf = ""
		m.statusErr = ""
		return m, nil
	}
	switch msg.String() {
	case "enter":
		s, ok := m.selected()
		if !ok {
			m.phase = sessionPhaseBrowse
			return m, nil
		}
		if m.sessions == nil {
			m.statusErr = "session list unavailable"
			m.phase = sessionPhaseBrowse
			return m, nil
		}
		title := strings.TrimSpace(m.renameBuf)
		got, err := m.sessions.Rename(s.ID, title)
		if err != nil {
			m.statusErr = err.Error()
			m.phase = sessionPhaseBrowse
			m.renameBuf = ""
			return m, nil
		}
		// Update list in place so view reflects rename without full PR refresh churn.
		keepID := got.ID
		final := strings.TrimSpace(got.Title)
		m.phase = sessionPhaseBrowse
		m.renameBuf = ""
		m.statusErr = ""
		m.reload()
		m.cursorOn(keepID)
		return m, func() tea.Msg {
			return sessionRenamedMsg{id: keepID, title: final}
		}
	case "backspace":
		if m.renameBuf != "" {
			// rune-safe trim
			r := []rune(m.renameBuf)
			m.renameBuf = string(r[:len(r)-1])
		}
		return m, nil
	default:
		if len(msg.Text) > 0 {
			m.renameBuf += msg.Text
		}
		return m, nil
	}
}

func (m *sessionModal) updateConfirmDelete(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "n" {
		m.phase = sessionPhaseBrowse
		m.deleteID = ""
		m.deleteForce = false
		m.statusErr = ""
		return m, nil
	}
	switch msg.String() {
	case "f":
		m.deleteForce = true
		return m, nil
	case "y", "enter":
		return m.doDelete()
	default:
		return m, nil
	}
}

func (m *sessionModal) doDelete() (modal, tea.Cmd) {
	id := m.deleteID
	force := m.deleteForce
	if id == "" || m.sessions == nil {
		m.phase = sessionPhaseBrowse
		m.deleteID = ""
		m.deleteForce = false
		return m, nil
	}
	// Active (current) or open sessions require an explicit force arm.
	needsForce := id == m.current
	if !needsForce {
		if s, ok, _ := m.sessions.Get(id); ok && s.Open {
			needsForce = true
		}
	}
	if needsForce && !force {
		// Stay in confirm so the user can press f then y.
		return m, nil
	}
	if err := m.sessions.Delete(id, force || needsForce); err != nil {
		if strings.Contains(err.Error(), "force required") {
			// Manager still considers it open; keep confirm open for force.
			return m, nil
		}
		m.statusErr = err.Error()
		m.phase = sessionPhaseBrowse
		m.deleteID = ""
		m.deleteForce = false
		return m, nil
	}
	m.phase = sessionPhaseBrowse
	m.deleteID = ""
	m.deleteForce = false
	m.statusErr = ""
	m.reload()
	list := m.filtered()
	if m.cursor >= len(list) {
		m.cursor = max(0, len(list)-1)
	}
	return m, nil
}

func (m *sessionModal) cursorOn(id string) {
	list := m.filtered()
	for i, s := range list {
		if s.ID == id {
			m.cursor = i
			return
		}
	}
}

func (m *sessionModal) view(width int, th theme.Theme) string {
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))

	if m.phase == sessionPhaseRename {
		return m.viewRename(width, th, inner)
	}
	if m.phase == sessionPhaseConfirmDelete {
		return m.viewConfirmDelete(width, th, inner)
	}

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
		if m.statusErr != "" {
			body = body + "\n" + wrapToWidth(st.Error.Render(m.statusErr), inner)
		}
	}
	title := "Resume session"
	if m.allProjects {
		title = "Resume session (all projects)"
	}
	hints := []string{"type to filter", "↑/↓ move", "enter resume", "ctrl+r rename", "ctrl+x delete", "esc close"}
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

func (m *sessionModal) viewRename(width int, th theme.Theme, inner int) string {
	st := th.S()
	s, ok := m.selected()
	label := "session"
	if ok {
		label = shortSessionID(s.ID)
	}
	lines := []string{
		st.Muted.Render("Rename " + label),
		st.Input.Render(m.renameBuf) + st.InputCursor.Render(th.Icons.InputCursor),
	}
	body := wrapToWidth(strings.Join(lines, "\n"), inner)
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Rename session",
		Hint:  dotJoin(th, "type title", "enter save", "esc cancel"),
		Width: width,
	}, body)
}

func (m *sessionModal) viewConfirmDelete(width int, th theme.Theme, inner int) string {
	st := th.S()
	title := m.deleteID
	openHint := ""
	if s, ok, _ := m.lookup(m.deleteID); ok {
		title = sessionPickerLabel(s)
		needsForce := s.Open || s.ID == m.current
		if needsForce {
			if m.deleteForce {
				openHint = st.Warning.Render("force armed — enter/y deletes open session")
			} else {
				openHint = st.Warning.Render("open/active — press f to arm force, then y")
			}
		}
	}
	lines := []string{
		st.Danger.Render("Delete session?"),
		st.Text.Render(sanitizeDisplayData(title)),
		st.Muted.Render(shortSessionID(m.deleteID)),
	}
	if openHint != "" {
		lines = append(lines, openHint)
	}
	body := wrapToWidth(strings.Join(lines, "\n"), inner)
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Delete session",
		Hint:  dotJoin(th, "y/enter confirm", "f force", "n/esc cancel"),
		Width: width,
		Tone:  ui.ToneDanger,
	}, body)
}

func (m *sessionModal) lookup(id string) (host.Session, bool, error) {
	for _, s := range m.all {
		if s.ID == id {
			return s, true, nil
		}
	}
	if m.sessions != nil {
		return m.sessions.Get(id)
	}
	return host.Session{}, false, fmt.Errorf("unavailable")
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
