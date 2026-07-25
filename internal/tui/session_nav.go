package tui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// Leader key (ctrl+x) then a chord navigates subagent transcripts, matching
// opencode's session_child_first / session_parent / sibling cycle.
const leaderTimeout = 2 * time.Second

type leaderExpiredMsg struct {
	gen int
}

type childTranscriptRefreshMsg struct {
	id  string
	gen int
}

// viewingChild reports whether the left pane shows a subagent transcript.
func (m Model) viewingChild() bool {
	return m.viewingID != "" && m.viewingID != m.sessionID
}

// displayCells is the transcript currently shown in the left pane.
func (m Model) displayCells() []cell {
	if m.viewingChild() {
		return m.viewCells
	}
	return m.cells
}

// displayToolByID is the tool index for the displayed transcript.
func (m Model) displayToolByID() map[string]*toolCell {
	if m.viewingChild() {
		return m.viewToolByID
	}
	return m.toolByID
}

func (m *Model) armLeader() tea.Cmd {
	m.leaderArmed = true
	m.leaderGen++
	gen := m.leaderGen
	return tea.Tick(leaderTimeout, func(time.Time) tea.Msg {
		return leaderExpiredMsg{gen: gen}
	})
}

func (m *Model) clearLeader() {
	m.leaderArmed = false
}

// handleLeaderKey consumes a key while the leader is armed. handled is true
// when the chord was recognized or the leader state was cleared.
func (m *Model) handleLeaderKey(msg tea.KeyMsg) (handled bool, cmd tea.Cmd) {
	if !m.leaderArmed {
		return false, nil
	}
	m.clearLeader()
	switch {
	case key.Matches(msg, m.keyMap.SessionChildFirst), msg.String() == "j":
		cmd = m.navChildFirst()
		return true, cmd
	case key.Matches(msg, m.keyMap.SessionParent), msg.String() == "k":
		cmd = m.navParent()
		return true, cmd
	case key.Matches(msg, m.keyMap.SessionChildPrev), msg.String() == "h":
		cmd = m.navSibling(-1)
		return true, cmd
	case key.Matches(msg, m.keyMap.SessionChildNext), msg.String() == "l":
		cmd = m.navSibling(1)
		return true, cmd
	case key.Matches(msg, m.keyMap.Leader):
		// Double leader cancels.
		return true, nil
	default:
		// Unknown chord: drop leader, do not re-dispatch.
		return true, nil
	}
}

// handleSessionNavKeys handles bare up/down/left/right while viewing a child
// (opencode-style in-session navigation). At root only leader chords navigate.
func (m *Model) handleSessionNavKeys(msg tea.KeyMsg) (handled bool, cmd tea.Cmd) {
	if m.focus != focusLeft || m.modal != nil || m.completion != nil {
		return false, nil
	}
	if !m.viewingChild() {
		return false, nil
	}
	// Don't steal keys when the user is typing in the composer.
	if strings.TrimSpace(m.composer.Value()) != "" {
		return false, nil
	}
	switch {
	case key.Matches(msg, m.keyMap.SessionParent), msg.String() == "k", msg.String() == "up":
		return true, m.navParent()
	case key.Matches(msg, m.keyMap.SessionChildFirst), msg.String() == "j", msg.String() == "down":
		return true, m.navChildFirst()
	case key.Matches(msg, m.keyMap.SessionChildPrev), msg.String() == "h", msg.String() == "left":
		return true, m.navSibling(-1)
	case key.Matches(msg, m.keyMap.SessionChildNext), msg.String() == "l", msg.String() == "right":
		return true, m.navSibling(1)
	case key.Matches(msg, m.keyMap.Interrupt):
		// esc returns to parent when not interrupting a live root turn.
		if !m.turnRunning {
			return true, m.navParent()
		}
	}
	return false, nil
}

func (m *Model) currentViewID() string {
	if m.viewingID != "" {
		return m.viewingID
	}
	return m.sessionID
}

func (m *Model) navChildFirst() tea.Cmd {
	parent := m.currentViewID()
	kids := m.listChildren(parent)
	if len(kids) == 0 {
		m.setNotice("no subagent sessions", false)
		return nil
	}
	return m.openSessionView(kids[0].id)
}

func (m *Model) navParent() tea.Cmd {
	if !m.viewingChild() {
		m.setNotice("already at root session", false)
		return nil
	}
	parent := m.viewParentID
	if parent == "" {
		parent = m.sessionID
	}
	if parent == m.sessionID {
		m.closeSessionView()
		m.reflow()
		m.refreshViewport()
		return nil
	}
	return m.openSessionView(parent)
}

func (m *Model) navSibling(delta int) tea.Cmd {
	if !m.viewingChild() {
		// From root, sibling cycle is a no-op; enter first child instead.
		if delta > 0 {
			return m.navChildFirst()
		}
		return nil
	}
	parent := m.viewParentID
	if parent == "" {
		parent = m.sessionID
	}
	kids := m.listChildren(parent)
	if len(kids) == 0 {
		return nil
	}
	idx := 0
	for i, k := range kids {
		if k.id == m.viewingID {
			idx = i
			break
		}
	}
	next := (idx + delta) % len(kids)
	if next < 0 {
		next += len(kids)
	}
	return m.openSessionView(kids[next].id)
}

type navChild struct {
	id     string
	agent  string
	prompt string
	status string
	title  string
}

func (m *Model) listChildren(parentID string) []navChild {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return nil
	}
	// Runtime children (root only) preserve spawn order and live status.
	if parentID == m.sessionID && len(m.children) > 0 {
		out := make([]navChild, 0, len(m.children))
		for _, ch := range m.children {
			if ch.sessionID == "" || ch.sessionID == "child" {
				continue
			}
			out = append(out, navChild{
				id:     ch.sessionID,
				agent:  ch.agent,
				prompt: ch.prompt,
				status: ch.status,
			})
		}
		if len(out) > 0 {
			return out
		}
	}
	if m.services.Sessions == nil {
		return nil
	}
	list, err := m.services.Sessions.Children(parentID)
	if err != nil || len(list) == 0 {
		return nil
	}
	out := make([]navChild, 0, len(list))
	for _, s := range list {
		status := ""
		for _, ch := range m.children {
			if ch.sessionID == s.ID {
				status = ch.status
				break
			}
		}
		out = append(out, navChild{
			id:     s.ID,
			title:  s.Title,
			status: status,
		})
	}
	return out
}

func (m *Model) openSessionView(id string) tea.Cmd {
	id = strings.TrimSpace(id)
	if id == "" || id == m.sessionID {
		m.closeSessionView()
		m.reflow()
		m.refreshViewport()
		return nil
	}
	if m.services.Sessions == nil {
		m.setNotice("session navigation unavailable", true)
		return nil
	}
	cells, tools, title, parentID, err := loadSessionTranscript(m.services.Sessions, id)
	if err != nil {
		m.setNotice("subagent transcript: "+err.Error(), true)
		return nil
	}
	// Enrich title from runtime childActivity when meta title is empty.
	if title == "" {
		for _, ch := range m.children {
			if ch.sessionID == id {
				title = childViewTitle(ch.agent, ch.prompt)
				break
			}
		}
	}
	if title == "" {
		title = "subagent"
	}
	m.viewingID = id
	m.viewParentID = parentID
	if m.viewParentID == "" {
		m.viewParentID = m.sessionID
	}
	m.viewTitle = title
	m.viewCells = cells
	m.viewToolByID = tools
	m.selectedCell = -1
	m.selectedFileRef = -1
	m.viewGen++
	m.clearNotice()
	m.reflow()
	m.refreshViewport()
	m.viewport.GotoBottom()
	// Live refresh while the child is still running.
	if m.childIsRunning(id) {
		gen := m.viewGen
		return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
			return childTranscriptRefreshMsg{id: id, gen: gen}
		})
	}
	return nil
}

func (m *Model) closeSessionView() {
	m.viewingID = ""
	m.viewParentID = ""
	m.viewTitle = ""
	m.viewCells = nil
	m.viewToolByID = nil
	m.viewGen++
	m.selectedCell = -1
	m.selectedFileRef = -1
}

func (m *Model) childIsRunning(id string) bool {
	for _, ch := range m.children {
		if ch.sessionID == id && ch.status == "running" {
			return true
		}
	}
	return false
}

func (m *Model) refreshViewingTranscript() tea.Cmd {
	if !m.viewingChild() || m.services.Sessions == nil {
		return nil
	}
	id := m.viewingID
	cells, tools, title, parentID, err := loadSessionTranscript(m.services.Sessions, id)
	if err != nil {
		return nil
	}
	if title != "" {
		m.viewTitle = title
	}
	if parentID != "" {
		m.viewParentID = parentID
	}
	m.viewCells = cells
	m.viewToolByID = tools
	m.refreshViewport()
	if m.childIsRunning(id) {
		gen := m.viewGen
		return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
			return childTranscriptRefreshMsg{id: id, gen: gen}
		})
	}
	return nil
}

func childViewTitle(agent, prompt string) string {
	agent = strings.TrimSpace(agent)
	prompt = strings.TrimSpace(prompt)
	switch {
	case agent != "" && prompt != "":
		return agent + ": " + prompt
	case agent != "":
		return agent
	case prompt != "":
		return prompt
	default:
		return "subagent"
	}
}

func loadSessionTranscript(sessions host.Sessions, id string) (cells []cell, tools map[string]*toolCell, title, parentID string, err error) {
	data, err := sessions.ReplayJSONL(id)
	if err != nil {
		return nil, nil, "", "", err
	}
	events, err := decodeSessionJSONL(data)
	if err != nil {
		return nil, nil, "", "", err
	}
	if info, ok, gerr := sessions.Get(id); gerr == nil && ok {
		parentID = info.ParentID
		title = strings.TrimSpace(info.Title)
	}
	cells, tools = cellsFromEvents(events)
	if title == "" {
		for _, ev := range events {
			if t, ok := ev.(protocol.SessionTitled); ok {
				if topic := strings.TrimSpace(t.Title); topic != "" {
					title = topic
					break
				}
			}
		}
	}
	if title == "" {
		for _, ev := range events {
			if u, ok := ev.(protocol.UserMessage); ok {
				if topic := sanitizeTitleTopic(u.Text); topic != "" {
					title = topic
					break
				}
			}
		}
	}
	return cells, tools, title, parentID, nil
}

func decodeSessionJSONL(data []byte) ([]protocol.Event, error) {
	var events []protocol.Event
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	line := 0
	for sc.Scan() {
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		line++
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, err
		}
		ev, err := env.Decode()
		if err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, sc.Err()
}

// cellsFromEvents rebuilds transcript cells from a session event log without
// side effects (no modals, notices, or agent-state updates).
func cellsFromEvents(events []protocol.Event) ([]cell, map[string]*toolCell) {
	var cells []cell
	toolByID := map[string]*toolCell{}
	complete := func() {
		for _, c := range cells {
			if a, ok := c.(*assistantCell); ok {
				a.complete = true
				a.mdCacheOK = false
			}
		}
	}
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.UserMessage:
			complete()
			cells = append(cells, &userCell{text: ev.Text})
		case protocol.TextDelta:
			if last, ok := lastCell[*assistantCell](cells); ok {
				last.text += ev.Text
			} else {
				cells = append(cells, &assistantCell{text: ev.Text})
			}
		case protocol.ToolCallBegin:
			if last, ok := lastCell[*assistantCell](cells); ok {
				last.complete = true
				last.mdCacheOK = false
			}
			tc := &toolCell{callID: ev.CallID, name: ev.Name, args: ev.Args}
			toolByID[ev.CallID] = tc
			if isExploreTool(ev.Name) {
				if exp, ok := lastCell[*exploreCell](cells); ok && exp.accepting {
					exp.calls = append(exp.calls, tc)
					break
				}
				if prev, ok := lastCell[*toolCell](cells); ok && isExploreTool(prev.name) {
					cells[len(cells)-1] = &exploreCell{
						calls:     []*toolCell{prev, tc},
						accepting: true,
					}
					break
				}
				cells = append(cells, tc)
				break
			}
			if exp, ok := lastCell[*exploreCell](cells); ok {
				exp.accepting = false
			}
			cells = append(cells, tc)
		case protocol.ToolCallOutput:
			if tc, ok := toolByID[ev.CallID]; ok && !tc.done {
				tc.output += ev.Data
			}
		case protocol.ToolCallEnd:
			if tc, ok := toolByID[ev.CallID]; ok {
				tc.title, tc.output, tc.metadata, tc.done, tc.isError = ev.Title, ev.Output, ev.Metadata, true, ev.IsError
			}
		case protocol.TurnCompleted:
			complete()
			if exp, ok := lastCell[*exploreCell](cells); ok {
				exp.accepting = false
			}
		case protocol.EngineError:
			cells = append(cells, &errorCell{text: ev.Message})
		}
	}
	complete()
	if exp, ok := lastCell[*exploreCell](cells); ok {
		exp.accepting = false
	}
	return cells, toolByID
}

// sessionTreeNodes builds a ui.Tree of the root session and its children for
// the activity pane.
func (m Model) sessionTreeNodes() []ui.TreeNode {
	rootID := m.sessionID
	if rootID == "" {
		rootID = "session"
	}
	rootLabel := "session"
	if topic := strings.TrimSpace(m.titleTopic); topic != "" {
		rootLabel = sanitizeTitleTopic(topic)
	}
	root := ui.TreeNode{
		ID:      rootID,
		Label:   rootLabel,
		Current: !m.viewingChild(),
		Tone:    ui.ToneAccent,
	}
	kids := m.listChildren(m.sessionID)
	if len(kids) == 0 {
		root.Leaf = true
		return []ui.TreeNode{root}
	}
	root.Expanded = true
	root.Children = make([]ui.TreeNode, 0, len(kids))
	for _, ch := range kids {
		label := ch.title
		if label == "" {
			label = childViewTitle(ch.agent, ch.prompt)
		}
		if label == "" {
			label = shortSessionID(ch.id)
		}
		tone := ui.ToneDefault
		detail := ch.status
		switch ch.status {
		case "running":
			tone = ui.ToneAccentAlt
		case string(protocol.ChildStatusCompleted):
			tone = ui.ToneSuccess
		case string(protocol.ChildStatusFailed):
			tone = ui.ToneError
		case string(protocol.ChildStatusCanceled):
			tone = ui.ToneMuted
		}
		root.Children = append(root.Children, ui.TreeNode{
			ID:      ch.id,
			Label:   label,
			Detail:  detail,
			Leaf:    true,
			Current: m.viewingID == ch.id,
			Tone:    tone,
		})
	}
	return []ui.TreeNode{root}
}
