package tui

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

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
func (m *Model) handleLeaderKey(msg tea.KeyPressMsg) (handled bool, cmd tea.Cmd) {
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
func (m *Model) handleSessionNavKeys(msg tea.KeyPressMsg) (handled bool, cmd tea.Cmd) {
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
	case m.matchesInterrupt(msg):
		// Idle child view: esc returns to parent (interrupt already handled
		// mid-turn before session nav is reached).
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
		cmd := m.closeSessionView()
		m.reflow()
		m.refreshViewport()
		return cmd
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
	name   string
}

func (m *Model) listChildren(parentID string) []navChild {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return nil
	}
	// Runtime activity rows preserve spawn order and live status. Filter by
	// parent so nested grandchildren are not listed under the root.
	if len(m.children) > 0 {
		out := make([]navChild, 0, len(m.children))
		for _, ch := range m.children {
			if ch.sessionID == "" || ch.sessionID == "child" {
				continue
			}
			chParent := ch.parentID
			if chParent == "" {
				chParent = m.sessionID
			}
			if chParent != parentID {
				continue
			}
			out = append(out, navChild{
				id:     ch.sessionID,
				agent:  ch.agent,
				prompt: ch.prompt,
				status: ch.status,
				title:  ch.title,
				name:   ch.name,
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
		name := ""
		agent := ""
		prompt := ""
		for _, ch := range m.children {
			if ch.sessionID == s.ID {
				status = ch.status
				name = ch.name
				agent = ch.agent
				prompt = ch.prompt
				break
			}
		}
		out = append(out, navChild{
			id:     s.ID,
			title:  s.Title,
			name:   name,
			agent:  agent,
			prompt: prompt,
			status: status,
		})
	}
	return out
}

func (m *Model) openSessionView(id string) tea.Cmd {
	id = strings.TrimSpace(id)
	if id == "" || id == m.sessionID {
		cmd := m.closeSessionView()
		m.reflow()
		m.refreshViewport()
		return cmd
	}
	if m.services.Sessions == nil {
		m.setNotice("session navigation unavailable", true)
		return nil
	}
	live := m.childIsRunning(id)
	var (
		cells           []cell
		tools           map[string]*toolCell
		title, parentID string
		err             error
	)
	if live {
		cells, tools, title, parentID, err = loadSessionTranscriptLive(m.services.Sessions, id)
	} else {
		cells, tools, title, parentID, err = loadSessionTranscript(m.services.Sessions, id)
	}
	if err != nil {
		m.setNotice("subagent transcript: "+err.Error(), true)
		return nil
	}
	// Enrich title from runtime childActivity when meta title is empty.
	if title == "" {
		for _, ch := range m.children {
			if ch.sessionID == id {
				title = childViewTitle(ch.agent, ch.prompt, ch.sessionID, ch.title, ch.name)
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
	agentsCmd := m.broadcastAgentsState()
	// Live refresh while the child is still running.
	if live {
		gen := m.viewGen
		return tea.Batch(agentsCmd, tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
			return childTranscriptRefreshMsg{id: id, gen: gen}
		}))
	}
	return agentsCmd
}

func (m *Model) closeSessionView() tea.Cmd {
	m.viewingID = ""
	m.viewParentID = ""
	m.viewTitle = ""
	m.viewCells = nil
	m.viewToolByID = nil
	m.viewGen++
	m.selectedCell = -1
	m.selectedFileRef = -1
	return m.broadcastAgentsState()
}

func (m *Model) childIsRunning(id string) bool {
	for _, ch := range m.children {
		if ch.sessionID != id {
			continue
		}
		// Treat any non-terminal status as live (running/working/starting/…).
		if ch.status != "" && !childStatusTerminal(ch.status) {
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
	live := m.childIsRunning(id)
	var (
		cells           []cell
		tools           map[string]*toolCell
		title, parentID string
		err             error
	)
	if live {
		cells, tools, title, parentID, err = loadSessionTranscriptLive(m.services.Sessions, id)
	} else {
		cells, tools, title, parentID, err = loadSessionTranscript(m.services.Sessions, id)
	}
	if err != nil {
		// Keep prior cells on transient read/decode errors; keep polling while live.
		if live {
			gen := m.viewGen
			return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
				return childTranscriptRefreshMsg{id: id, gen: gen}
			})
		}
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
	if live {
		gen := m.viewGen
		return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg {
			return childTranscriptRefreshMsg{id: id, gen: gen}
		})
	}
	return nil
}

// childViewTitle builds a brief subagent label: durable title, else stable
// spawn name, else "{agent} {shortId}", else one of those parts, else "subagent".
func childViewTitle(agent, prompt, sessionID, title, name string) string {
	if t := strings.TrimSpace(title); t != "" {
		return sanitizeTitleTopic(t)
	}
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	agent = strings.TrimSpace(agent)
	short := shortSessionID(sessionID)
	switch {
	case agent != "" && short != "":
		return agent + " " + short
	case agent != "":
		return agent
	case short != "":
		return short
	default:
		// prompt is a last-resort fallback for legacy rows without id/agent.
		if p := strings.TrimSpace(prompt); p != "" {
			return sanitizeTitleTopic(p)
		}
		return "subagent"
	}
}

func loadSessionTranscript(sessions host.Sessions, id string) (cells []cell, tools map[string]*toolCell, title, parentID string, err error) {
	return loadSessionTranscriptOpts(sessions, id, false)
}

// loadSessionTranscriptLive rebuilds a still-running child transcript without
// force-completing the trailing assistant/explore stream (issue #692).
func loadSessionTranscriptLive(sessions host.Sessions, id string) (cells []cell, tools map[string]*toolCell, title, parentID string, err error) {
	return loadSessionTranscriptOpts(sessions, id, true)
}

func loadSessionTranscriptOpts(sessions host.Sessions, id string, live bool) (cells []cell, tools map[string]*toolCell, title, parentID string, err error) {
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
	if live {
		cells, tools = cellsFromEventsLive(events)
	} else {
		cells, tools = cellsFromEvents(events)
	}
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
				if topic := sanitizeTitleTopic(userMessageDisplayText(u.Text, u.Images)); topic != "" {
					title = topic
					break
				}
			}
		}
	}
	return cells, tools, title, parentID, nil
}

// sessionLogSchemaVersion is the session JSONL header schema this TUI understands
// (must stay in sync with internal/session.LogSchemaVersion).
const sessionLogSchemaVersion = 1

func decodeSessionJSONL(data []byte) ([]protocol.Event, error) {
	// Collect lines first so a trailing partial write (live child still
	// appending) can be skipped without failing the whole transcript.
	sc := bufio.NewScanner(bytes.NewReader(data))
	// Multimodal user.message lines can carry multi-MiB base64 images.
	sc.Buffer(make([]byte, 0, 64*1024), 32<<20)
	var lines [][]byte
	for sc.Scan() {
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		// Scanner reuses its buffer; copy each line.
		lines = append(lines, append([]byte(nil), raw...))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	var events []protocol.Event
	for i, raw := range lines {
		// Optional first-line session.header (#803); not a protocol event.
		if isSessionLogHeaderLine(raw) {
			if ver, ok := sessionHeaderSchemaVersion(raw); ok && ver > sessionLogSchemaVersion {
				return nil, fmt.Errorf("session log schema version %d is newer than supported %d; upgrade strike", ver, sessionLogSchemaVersion)
			}
			continue
		}
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			// Last line may be mid-append while the child is still writing.
			if i == len(lines)-1 {
				break
			}
			return nil, err
		}
		ev, err := env.Decode()
		if err != nil {
			if i == len(lines)-1 {
				break
			}
			return nil, err
		}
		events = append(events, ev)
	}
	return events, nil
}

func isSessionLogHeaderLine(raw []byte) bool {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.Type == "session.header"
}

func sessionHeaderSchemaVersion(raw []byte) (int, bool) {
	var probe struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return 0, false
	}
	return probe.SchemaVersion, true
}

// seedFromReplay rebuilds transcript cells and durable UI selection state from
// a prior session log without live side effects: no modals, notices, working
// state, or permission attention. Incomplete ChildStarted rows are marked
// canceled so resume cannot leave a forever-running activity poll.
func seedFromReplay(m *Model, events []protocol.Event) {
	if m == nil || len(events) == 0 {
		return
	}
	// Rebuild structured timeline from the durable log (synthetic 1ms steps
	// when envelope times are unavailable — live Observe uses wall clock).
	m.resetRunTimeline()
	base := time.Unix(0, 0).UTC()
	for i, ev := range events {
		m.observeTimeline(ev, base.Add(time.Duration(i)*time.Millisecond))
	}
	m.cells, m.toolByID = cellsFromEvents(events)
	// Rebuild /undo preview stack from durable TurnCompleted / SessionRewound
	// so resume still shows path preview. Checkpoint bytes do not survive
	// process restart (#573) — mark previews so the modal does not imply
	// disk restore will work.
	m.undoStack = undoStackFromEvents(events)
	for i := range m.undoStack {
		m.undoStack[i].checkpointsGone = true
	}
	// Incomplete assistant/tool streams stay visible but are marked complete
	// so resume never looks mid-stream.
	for _, c := range m.cells {
		switch cell := c.(type) {
		case *assistantCell:
			cell.complete = true
			cell.mdCacheOK = false
		case *toolCell:
			if !cell.done {
				cell.done = true
				cell.isError = true
				if cell.output == "" {
					cell.output = "interrupted"
				}
			}
		case *exploreCell:
			cell.accepting = false
			for _, tc := range cell.calls {
				if tc != nil && !tc.done {
					tc.done = true
					tc.isError = true
					if tc.output == "" {
						tc.output = "interrupted"
					}
				}
			}
		}
	}
	m.turnRunning = false
	m.awaitingPermission = false
	// Resume never leaves sticky queue wait — the process is gone.
	m.queueRequestID = ""
	m.queuePools = nil
	m.queueLabel = ""
	m.turnStartedAt = time.Time{}
	m.toolCallsThisTurn = 0
	m.clearModalStack()
	m.children = childrenFromEvents(events)
	m.teamMessages = teamMessagesFromEvents(events)
	for _, ev := range events {
		if corr, ok := eventCorrelation(ev); ok && (corr.ParentSessionID != "" || corr.Depth > 0) {
			// Team roster/messages may carry child correlation when re-emitted
			// from nested engines; still applied above via dedicated helpers.
			switch ev.(type) {
			case protocol.TeamRoster, protocol.AgentMessage, protocol.AgentContractTimeout:
			default:
				continue
			}
		}
		switch e := ev.(type) {
		case protocol.UserMessage:
			if m.titleTopic == "" {
				if topic := sanitizeTitleTopic(userMessageDisplayText(e.Text, e.Images)); topic != "" {
					m.titleTopic = topic
				}
			}
		case protocol.SessionTitled:
			if topic := sanitizeTitleTopic(e.Title); topic != "" {
				m.titleTopic = topic
			}
		case protocol.ModelSelected:
			m.providerName, m.modelName = e.Provider, e.Model
		case protocol.AgentSelected:
			m.agentName = e.Name
		case protocol.PhaseChanged:
			m.phaseName = e.Phase
			m.phaseWorkflow = e.Workflow
			m.phaseGate = e.Gate
			m.phaseSource = e.Source
			m.phaseFingerprint = e.Fingerprint
			m.phaseStatus = e.Status
			if e.Phase == "" && e.Workflow == "" {
				m.phaseGate = ""
				m.phaseSource = ""
				m.phaseFingerprint = ""
				m.phaseStatus = ""
			}
		case protocol.EffortSelected:
			m.effort = e.Level
		case protocol.AutonomySelected:
			m.autonomy = e.Mode.Normalize()
		case protocol.FastSelected:
			m.fastEnabled = e.Enabled
		case protocol.UsageReported:
			m.recordUsage(e)
		}
	}
}

// childrenFromEvents rebuilds activity-pane child rows. A ChildStarted without
// ChildCompleted is treated as canceled — the child process is gone on resume.
// Later team.roster snapshots enrich names/states for teammates.
func childrenFromEvents(events []protocol.Event) []childActivity {
	var out []childActivity
	index := map[string]int{}
	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.ChildStarted:
			id := e.SessionID
			if id == "" {
				id = "child"
			}
			if i, ok := index[id]; ok {
				out[i].agent = e.Agent
				out[i].prompt = e.Prompt
				out[i].name = e.Name
				out[i].status = "running"
				if e.ParentSessionID != "" {
					out[i].parentID = e.ParentSessionID
				}
				continue
			}
			index[id] = len(out)
			out = append(out, childActivity{
				sessionID: id,
				parentID:  e.ParentSessionID,
				agent:     e.Agent,
				prompt:    e.Prompt,
				name:      e.Name,
				status:    "running",
			})
		case protocol.ChildCompleted:
			id := e.SessionID
			status := string(e.Status)
			if status == "" {
				status = string(protocol.ChildStatusCompleted)
			}
			if id != "" {
				if i, ok := index[id]; ok {
					out[i].status = status
					if e.Name != "" {
						out[i].name = e.Name
					}
					if e.ParentSessionID != "" && out[i].parentID == "" {
						out[i].parentID = e.ParentSessionID
					}
					if e.Verification != nil {
						_ = applyChildVerification(&out, index, id, e.Verification)
					}
					continue
				}
			} else if len(out) > 0 {
				out[len(out)-1].status = status
				if e.Name != "" {
					out[len(out)-1].name = e.Name
				}
				continue
			}
			if id == "" {
				id = "child"
			}
			index[id] = len(out)
			out = append(out, childActivity{
				sessionID: id,
				parentID:  e.ParentSessionID,
				name:      e.Name,
				status:    status,
			})
			if e.Verification != nil {
				_ = applyChildVerification(&out, index, id, e.Verification)
			}
		case protocol.ChildEscalated:
			applyChildEscalatedToChildren(&out, index, e)
		case protocol.PathOverlap:
			applyPathOverlapToChildren(&out, index, e)
		case protocol.TeamRoster:
			leadID := strings.TrimSpace(e.LeadID)
			if leadID == "" {
				leadID = strings.TrimSpace(e.SessionID)
			}
			applyTeamRosterMembers(&out, index, e.Members, leadID)
		case protocol.VerificationCompleted:
			if strings.EqualFold(strings.TrimSpace(e.Scope), protocol.VerificationScopeChild) ||
				(e.ParentSessionID != "" || e.Depth > 0) {
				rep := e.Report
				_ = applyChildVerification(&out, index, e.SessionID, &rep)
			}
		case protocol.SchedulerQueued:
			applySchedulerQueuedToChildren(&out, e)
		case protocol.SchedulerAdmitted:
			applySchedulerClearToChildren(&out, e.RequestID, e.SessionID)
		case protocol.SchedulerCanceled:
			applySchedulerClearToChildren(&out, e.RequestID, e.SessionID)
		}
	}
	for i := range out {
		if out[i].status == "running" {
			out[i].status = string(protocol.ChildStatusCanceled)
			// Child process is gone on resume — drop stale queue chips.
			out[i].queueRequestID = ""
			out[i].queuePools = nil
			out[i].queueLabel = ""
		}
	}
	if len(out) > maxChildActivity {
		out = out[len(out)-maxChildActivity:]
	}
	return out
}

// undoStackFromEvents rebuilds the /undo preview stack from a session log.
// Mirrors live TurnCompleted push / SessionRewound pop. Child-lineage events
// are ignored (same filter as live applyEvent correlation checks).
func undoStackFromEvents(events []protocol.Event) []undoPreview {
	var stack []undoPreview
	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.TurnCompleted:
			if e.ParentSessionID != "" || e.Depth > 0 {
				continue
			}
			stack = append(stack, undoPreview{
				files:     append([]protocol.TurnFileChange(nil), e.Files...),
				skipped:   e.CheckpointSkipped,
				uncovered: append([]string(nil), e.Uncovered...),
			})
		case protocol.SessionRewound:
			if e.ParentSessionID != "" || e.Depth > 0 {
				continue
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return stack
}

// cellsFromEvents rebuilds transcript cells from a finished session event log
// without side effects (no modals, notices, or agent-state updates). Trailing
// assistants are marked complete (resume/history).
func cellsFromEvents(events []protocol.Event) ([]cell, map[string]*toolCell) {
	return cellsFromEventsOpts(events, false)
}

// cellsFromEventsLive rebuilds a still-running child transcript. Trailing
// assistant/explore streams stay incomplete so glamour is not run on partial
// markdown and live tool tails keep updating (issue #692).
func cellsFromEventsLive(events []protocol.Event) ([]cell, map[string]*toolCell) {
	return cellsFromEventsOpts(events, true)
}

func cellsFromEventsOpts(events []protocol.Event, live bool) ([]cell, map[string]*toolCell) {
	var cells []cell
	toolByID := map[string]*toolCell{}
	childAgent := map[string]string{}
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
			// Notices are model-facing only; UI uses ChildCompleted cells.
			if isChildCompletedNotice(ev.Text) {
				break
			}
			cells = append(cells, &userCell{text: userMessageDisplayText(ev.Text, ev.Images)})
		case protocol.TextDelta:
			if last, ok := lastCell[*assistantCell](cells); ok {
				last.text += ev.Text
			} else {
				cells = append(cells, &assistantCell{text: ev.Text})
			}
		case protocol.ReasoningDelta:
			if ev.Text == "" {
				break
			}
			if last, ok := lastCell[*reasoningCell](cells); ok {
				last.text += ev.Text
			} else {
				cells = append(cells, &reasoningCell{text: ev.Text})
			}
		case protocol.ToolCallBegin:
			if last, ok := lastCell[*assistantCell](cells); ok {
				last.complete = true
				last.mdCacheOK = false
			}
			if ev.Name == "sleep" {
				cells = beginSleepToolCell(cells, toolByID, ev.CallID, ev.Name, ev.Args)
				break
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
				applyToolCallEnd(tc, ev.Title, ev.Output, ev.Metadata, ev.IsError, ev.ErrorCode)
			}
		case protocol.ChildStarted:
			id := strings.TrimSpace(ev.SessionID)
			if id == "" {
				id = "child"
			}
			if a := strings.TrimSpace(ev.Agent); a != "" {
				childAgent[id] = a
			}
		case protocol.ChildCompleted:
			applyChildCompletedToTaskCells(toolByID, ev)
			id := strings.TrimSpace(ev.SessionID)
			agent := ""
			if id != "" {
				agent = childAgent[id]
			}
			cells = appendSubagentResultCell(cells, ev, agent, 0)
		case protocol.TurnCompleted:
			complete()
			if exp, ok := lastCell[*exploreCell](cells); ok {
				exp.accepting = false
			}
		case protocol.SessionRewound:
			cells, toolByID = dropLastUserTurnCells(cells, toolByID)
		case protocol.EngineError:
			cells = append(cells, &errorCell{text: ev.Message})
		}
	}
	if live {
		// Keep trailing stream incomplete while the child is still writing.
		if exp, ok := lastCell[*exploreCell](cells); ok && exp.allDone() {
			exp.accepting = false
		}
	} else {
		complete()
		if exp, ok := lastCell[*exploreCell](cells); ok {
			exp.accepting = false
		}
	}
	return cells, toolByID
}

// dropLastUserTurnCells removes transcript cells from the last user message
// through the end (matching engine dropLastUserTurn for the UI).
func dropLastUserTurnCells(cells []cell, toolByID map[string]*toolCell) ([]cell, map[string]*toolCell) {
	if toolByID == nil {
		toolByID = map[string]*toolCell{}
	}
	lastUser := -1
	for i := len(cells) - 1; i >= 0; i-- {
		if _, ok := cells[i].(*userCell); ok {
			lastUser = i
			break
		}
	}
	if lastUser < 0 {
		return cells, toolByID
	}
	for _, c := range cells[lastUser:] {
		switch cell := c.(type) {
		case *toolCell:
			delete(toolByID, cell.callID)
		case *exploreCell:
			for _, tc := range cell.calls {
				if tc != nil {
					delete(toolByID, tc.callID)
				}
			}
		}
	}
	return cells[:lastUser], toolByID
}

// sessionTreeNodes builds a ui.Tree of live parent sessions and nested task
// children for the activity pane (multi-root when Roots is wired).
func (m Model) sessionTreeNodes() []ui.TreeNode {
	ids := m.liveRootIDs()
	if len(ids) == 0 {
		rootID := m.sessionID
		if rootID == "" {
			rootID = "session"
		}
		ids = []string{rootID}
	}
	out := make([]ui.TreeNode, 0, len(ids))
	for _, rootID := range ids {
		rootLabel := m.rootTitleLabel(rootID)
		if rootLabel == "" {
			rootLabel = "session"
		}
		current := rootID == m.sessionID && !m.viewingChild()
		root := ui.TreeNode{
			ID:      rootID,
			Label:   rootLabel,
			Current: current,
			Tone:    agentStateTone(m.rootAgentState(rootID)),
			Detail:  agentsRootDetail(m.rootAgentState(rootID)),
		}
		if m.services.Sessions != nil && rootID != "" {
			if s, ok, err := m.services.Sessions.Get(rootID); err == nil && ok {
				root.Suffix = sessionPRBadge(m.th, s)
			}
		}
		var kids []navChild
		if rootID == m.sessionID {
			kids = m.listChildren(rootID)
		} else if m.roots != nil {
			if p, ok := m.roots[rootID]; ok && p != nil {
				// Rebuild nav children from stashed activity for background roots.
				for _, ch := range p.children {
					if ch.sessionID == "" || ch.sessionID == "child" {
						continue
					}
					chParent := ch.parentID
					if chParent == "" {
						chParent = rootID
					}
					if chParent != rootID {
						continue
					}
					kids = append(kids, navChild{
						id:     ch.sessionID,
						agent:  ch.agent,
						prompt: ch.prompt,
						status: ch.status,
						title:  ch.title,
						name:   ch.name,
					})
				}
			}
		}
		if len(kids) == 0 {
			root.Leaf = true
		} else {
			root.Expanded = true
			// Newest child first in the activity tree.
			root.Children = m.navChildrenToTree(reverseNavChildren(kids))
		}
		out = append(out, root)
	}
	return out
}

func (m Model) navChildrenToTree(kids []navChild) []ui.TreeNode {
	out := make([]ui.TreeNode, 0, len(kids))
	for _, ch := range kids {
		label := childViewTitle(ch.agent, ch.prompt, ch.id, ch.title, ch.name)
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
		node := ui.TreeNode{
			ID:      ch.id,
			Label:   label,
			Detail:  detail,
			Current: m.viewingID == ch.id,
			Tone:    tone,
		}
		grand := reverseNavChildren(m.listChildren(ch.id))
		if len(grand) == 0 {
			node.Leaf = true
		} else {
			node.Expanded = true
			node.Children = m.navChildrenToTree(grand)
		}
		out = append(out, node)
	}
	return out
}
