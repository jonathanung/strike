package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// rootPane holds frozen UI state for one parent session so concurrent roots can
// keep running while the left pane shows another.
type rootPane struct {
	sessionID  string
	workDir    string
	titleTopic string

	cells    []cell
	toolByID map[string]*toolCell
	children []childActivity

	providerName  string
	modelName     string
	agentName     string
	phaseName     string
	phaseWorkflow string
	effort        protocol.Effort
	autonomy      protocol.Autonomy
	fastEnabled   bool

	turnRunning        bool
	awaitingPermission bool
	sessionErrored     bool
	turnStartedAt      time.Time
	toolCallsThisTurn  int

	usageInput, usageOutput, usageUsed protocol.TokenCount
	usageSource                        string
	contextLimit                       int
	contextLimitKnown                  bool
	outputLimit                        int
	outputLimitKnown                   bool

	viewingID    string
	viewParentID string
	viewTitle    string
	viewCells    []cell
	viewToolByID map[string]*toolCell
}

// stashActiveRoot copies live Model fields into the roots map under sessionID.
func (m *Model) stashActiveRoot() {
	if m.sessionID == "" {
		return
	}
	if m.roots == nil {
		m.roots = map[string]*rootPane{}
	}
	toolByID := make(map[string]*toolCell, len(m.toolByID))
	for k, v := range m.toolByID {
		toolByID[k] = v
	}
	viewTools := make(map[string]*toolCell, len(m.viewToolByID))
	for k, v := range m.viewToolByID {
		viewTools[k] = v
	}
	children := append([]childActivity(nil), m.children...)
	m.roots[m.sessionID] = &rootPane{
		sessionID:          m.sessionID,
		workDir:            m.workDir,
		titleTopic:         m.titleTopic,
		cells:              append([]cell(nil), m.cells...),
		toolByID:           toolByID,
		children:           children,
		providerName:       m.providerName,
		modelName:          m.modelName,
		agentName:          m.agentName,
		phaseName:          m.phaseName,
		phaseWorkflow:      m.phaseWorkflow,
		effort:             m.effort,
		autonomy:           m.autonomy,
		fastEnabled:        m.fastEnabled,
		turnRunning:        m.turnRunning,
		awaitingPermission: m.awaitingPermission,
		sessionErrored:     m.sessionErrored,
		turnStartedAt:      m.turnStartedAt,
		toolCallsThisTurn:  m.toolCallsThisTurn,
		usageInput:         m.usageInput,
		usageOutput:        m.usageOutput,
		usageUsed:          m.usageUsed,
		usageSource:        m.usageSource,
		contextLimit:       m.contextLimit,
		contextLimitKnown:  m.contextLimitKnown,
		outputLimit:        m.outputLimit,
		outputLimitKnown:   m.outputLimitKnown,
		viewingID:          m.viewingID,
		viewParentID:       m.viewParentID,
		viewTitle:          m.viewTitle,
		viewCells:          append([]cell(nil), m.viewCells...),
		viewToolByID:       viewTools,
	}
}

// loadRootPane applies a stashed pane onto the live Model fields.
func (m *Model) loadRootPane(p *rootPane) {
	if p == nil {
		return
	}
	m.sessionID = p.sessionID
	m.workDir = p.workDir
	m.titleTopic = p.titleTopic
	m.cells = append([]cell(nil), p.cells...)
	m.toolByID = map[string]*toolCell{}
	for k, v := range p.toolByID {
		m.toolByID[k] = v
	}
	m.children = append([]childActivity(nil), p.children...)
	m.providerName = p.providerName
	m.modelName = p.modelName
	m.agentName = p.agentName
	m.phaseName = p.phaseName
	m.phaseWorkflow = p.phaseWorkflow
	m.effort = p.effort
	m.autonomy = p.autonomy
	m.fastEnabled = p.fastEnabled
	m.turnRunning = p.turnRunning
	m.awaitingPermission = p.awaitingPermission
	m.sessionErrored = p.sessionErrored
	m.turnStartedAt = p.turnStartedAt
	m.toolCallsThisTurn = p.toolCallsThisTurn
	m.usageInput = p.usageInput
	m.usageOutput = p.usageOutput
	m.usageUsed = p.usageUsed
	m.usageSource = p.usageSource
	m.contextLimit = p.contextLimit
	m.contextLimitKnown = p.contextLimitKnown
	m.outputLimit = p.outputLimit
	m.outputLimitKnown = p.outputLimitKnown
	m.viewingID = p.viewingID
	m.viewParentID = p.viewParentID
	m.viewTitle = p.viewTitle
	m.viewCells = append([]cell(nil), p.viewCells...)
	m.viewToolByID = map[string]*toolCell{}
	for k, v := range p.viewToolByID {
		m.viewToolByID[k] = v
	}
	m.selectedCell = -1
	m.selectedFileRef = -1
	m.modal = nil
	m.viewGen++
}

// ensureRootPane returns the pane for id, creating an empty one when missing.
func (m *Model) ensureRootPane(id string) *rootPane {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if id == m.sessionID {
		m.stashActiveRoot()
	}
	if m.roots == nil {
		m.roots = map[string]*rootPane{}
	}
	if p, ok := m.roots[id]; ok && p != nil {
		return p
	}
	p := &rootPane{
		sessionID: id,
		toolByID:  map[string]*toolCell{},
		autonomy:  protocol.AutonomySupervised,
	}
	if m.services.Roots != nil {
		p.workDir = m.services.Roots.WorkDir(id)
	}
	m.roots[id] = p
	return p
}

// rootForEvent maps an engine event onto its parent root session id.
func (m *Model) rootForEvent(ev protocol.Event) string {
	corr, ok := eventCorrelation(ev)
	if !ok {
		return m.sessionID
	}
	if corr.SessionID != "" {
		if corr.SessionID == m.sessionID {
			return m.sessionID
		}
		if m.roots != nil {
			if _, ok := m.roots[corr.SessionID]; ok {
				return corr.SessionID
			}
		}
		if m.services.Roots != nil {
			for _, id := range m.services.Roots.LiveIDs() {
				if id == corr.SessionID {
					return id
				}
			}
		}
	}
	// Child lineage: walk parent chain via live panes / activity rows.
	parent := corr.ParentSessionID
	if parent == "" && corr.SessionID != "" {
		// ChildCompleted sometimes only has SessionID; look up parent from activity.
		parent = m.parentOfChild(corr.SessionID)
	}
	if r := m.findLiveRootAncestor(parent); r != "" {
		return r
	}
	if r := m.findLiveRootAncestor(corr.SessionID); r != "" {
		return r
	}
	return m.sessionID
}

func (m *Model) parentOfChild(childID string) string {
	childID = strings.TrimSpace(childID)
	if childID == "" {
		return ""
	}
	for _, ch := range m.children {
		if ch.sessionID == childID {
			return ch.parentID
		}
	}
	if m.roots != nil {
		for _, p := range m.roots {
			for _, ch := range p.children {
				if ch.sessionID == childID {
					if ch.parentID != "" {
						return ch.parentID
					}
					return p.sessionID
				}
			}
		}
	}
	return ""
}

func (m *Model) findLiveRootAncestor(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		if id == m.sessionID {
			return id
		}
		if m.roots != nil {
			if _, ok := m.roots[id]; ok {
				return id
			}
		}
		if m.services.Roots != nil {
			for _, live := range m.services.Roots.LiveIDs() {
				if live == id {
					return id
				}
			}
		}
		id = m.parentOfChild(id)
	}
	return ""
}

// applyEventToRoot routes a background-root event into its stashed pane.
// Permission/question asks on a background root auto-activate it so the modal
// can resolve against the correct engine ops channel.
func (m *Model) applyEventToRoot(rootID string, ev protocol.Event) tea.Cmd {
	rootID = strings.TrimSpace(rootID)
	if rootID == "" || rootID == m.sessionID {
		return m.applyEvent(ev)
	}

	switch ev.(type) {
	case protocol.PermissionAsked, protocol.QuestionAsked:
		// Bring the root forward so the modal replies on the right ops stream.
		if cmd := m.activateRoot(rootID); cmd != nil {
			// activateRoot already refreshes; apply on the now-active model.
			return tea.Batch(cmd, m.applyEvent(ev))
		}
		return m.applyEvent(ev)
	}

	p := m.ensureRootPane(rootID)
	if p == nil {
		return nil
	}
	applyEventToPane(p, ev)
	// Keep live activity snapshot for the agents tree.
	return m.broadcastAgentsState()
}

// applyEventToPane mutates a stashed pane the same way applyEvent mutates Model
// for transcript/status fields (no modals).
func applyEventToPane(p *rootPane, ev protocol.Event) {
	if p == nil {
		return
	}
	if p.toolByID == nil {
		p.toolByID = map[string]*toolCell{}
	}
	switch e := ev.(type) {
	case protocol.TurnStarted:
		p.turnRunning = true
		p.sessionErrored = false
		p.turnStartedAt = time.Now()
		p.toolCallsThisTurn = 0
	case protocol.PermissionAsked, protocol.QuestionAsked:
		p.awaitingPermission = true
	case protocol.PermissionResolved, protocol.QuestionResolved:
		p.awaitingPermission = false
	case protocol.TurnCompleted:
		p.turnRunning = false
		p.awaitingPermission = false
		if e.StopReason == "error" {
			p.sessionErrored = true
		}
	case protocol.EngineError:
		if !p.turnRunning {
			p.sessionErrored = true
		}
	case protocol.UserMessage:
		p.sessionErrored = false
		p.cells = append(p.cells, &userCell{text: e.Text})
		if p.titleTopic == "" {
			if topic := sanitizeTitleTopic(e.Text); topic != "" {
				p.titleTopic = topic
			}
		}
	case protocol.SessionTitled:
		if topic := sanitizeTitleTopic(e.Title); topic != "" {
			p.titleTopic = topic
		}
	case protocol.TextDelta:
		if last, ok := lastCell[*assistantCell](p.cells); ok {
			last.text += e.Text
		} else {
			p.cells = append(p.cells, &assistantCell{text: e.Text})
		}
	case protocol.ReasoningDelta:
		if e.Text == "" {
			break
		}
		if last, ok := lastCell[*reasoningCell](p.cells); ok {
			last.text += e.Text
		} else {
			p.cells = append(p.cells, &reasoningCell{text: e.Text})
		}
	case protocol.ToolCallBegin:
		if last, ok := lastCell[*assistantCell](p.cells); ok {
			last.complete = true
			last.mdCacheOK = false
		}
		tc := &toolCell{callID: e.CallID, name: e.Name, args: e.Args}
		p.toolByID[e.CallID] = tc
		p.toolCallsThisTurn++
		p.cells = append(p.cells, tc)
	case protocol.ToolCallOutput:
		if tc, ok := p.toolByID[e.CallID]; ok && !tc.done {
			tc.output += e.Data
		}
	case protocol.ToolCallEnd:
		if tc, ok := p.toolByID[e.CallID]; ok {
			applyToolCallEnd(tc, e.Title, e.Output, e.Metadata, e.IsError)
		}
	case protocol.ModelSelected:
		p.providerName, p.modelName = e.Provider, e.Model
	case protocol.AgentSelected:
		p.agentName = e.Name
	case protocol.PhaseChanged:
		p.phaseName = e.Phase
		p.phaseWorkflow = e.Workflow
	case protocol.EffortSelected:
		p.effort = e.Level
	case protocol.AutonomySelected:
		p.autonomy = e.Mode.Normalize()
	case protocol.FastSelected:
		p.fastEnabled = e.Enabled
	case protocol.UsageReported:
		p.usageInput = e.Input
		p.usageOutput = e.Output
		p.usageUsed = e.Used
		p.usageSource = e.Source
	case protocol.ChildStarted:
		id := e.SessionID
		if id == "" {
			id = "child"
		}
		updated := false
		for i := range p.children {
			if p.children[i].sessionID == id {
				p.children[i].agent = e.Agent
				p.children[i].prompt = e.Prompt
				p.children[i].status = "running"
				p.children[i].startedAt = time.Now()
				p.children[i].endedAt = time.Time{}
				if e.ParentSessionID != "" {
					p.children[i].parentID = e.ParentSessionID
				}
				updated = true
				break
			}
		}
		if !updated {
			p.children = append(p.children, childActivity{
				sessionID: id,
				parentID:  e.ParentSessionID,
				agent:     e.Agent,
				prompt:    e.Prompt,
				status:    "running",
				startedAt: time.Now(),
			})
		}
		if len(p.children) > maxChildActivity {
			p.children = p.children[len(p.children)-maxChildActivity:]
		}
	case protocol.ChildCompleted:
		id := e.SessionID
		status := string(e.Status)
		if status == "" {
			status = string(protocol.ChildStatusCompleted)
		}
		for i := range p.children {
			if p.children[i].sessionID == id || (id == "" && i == len(p.children)-1) {
				p.children[i].status = status
				p.children[i].endedAt = time.Now()
				if e.ParentSessionID != "" && p.children[i].parentID == "" {
					p.children[i].parentID = e.ParentSessionID
				}
				break
			}
		}
		applyChildCompletedToTaskCells(p.toolByID, e)
	}
}

// activateRoot switches the live UI + host ops target to id.
func (m *Model) activateRoot(id string) tea.Cmd {
	id = strings.TrimSpace(id)
	if id == "" || id == m.sessionID {
		return nil
	}
	if m.services.Roots == nil {
		return func() tea.Msg { return sessionResumeMsg{id: id} }
	}
	if err := m.services.Roots.Activate(id); err != nil {
		m.setNotice("activate: "+err.Error(), true)
		return nil
	}
	m.stashActiveRoot()
	p := m.ensureRootPane(id)
	// If pane is empty (just opened live), seed from durable log.
	if p != nil && len(p.cells) == 0 && m.services.Sessions != nil {
		if data, err := m.services.Sessions.ReplayJSONL(id); err == nil {
			if events, err := decodeSessionJSONL(data); err == nil {
				seedPaneFromReplay(p, events)
			}
		}
		if info, ok, err := m.services.Sessions.Get(id); err == nil && ok {
			if t := strings.TrimSpace(info.Title); t != "" {
				p.titleTopic = t
			}
		}
		if wd := m.services.Roots.WorkDir(id); wd != "" {
			p.workDir = wd
		}
	}
	m.loadRootPane(p)
	m.clearNotice()
	m.reflow()
	m.refreshViewport()
	m.viewport.GotoBottom()
	return tea.Batch(m.broadcastContextState(), m.broadcastAgentsState())
}

// spawnRoot creates a new concurrent parent and focuses it.
func (m *Model) spawnRoot() tea.Cmd {
	if m.services.Roots == nil {
		m.setNotice("multi-root sessions unavailable", true)
		return nil
	}
	id, err := m.services.Roots.Spawn()
	if err != nil {
		m.setNotice("new agent: "+err.Error(), true)
		return nil
	}
	m.stashActiveRoot()
	p := &rootPane{
		sessionID: id,
		workDir:   m.services.Roots.WorkDir(id),
		toolByID:  map[string]*toolCell{},
		autonomy:  protocol.AutonomySupervised,
	}
	if m.roots == nil {
		m.roots = map[string]*rootPane{}
	}
	m.roots[id] = p
	m.loadRootPane(p)
	m.setNotice("new agent "+shortSessionID(id), false)
	m.reflow()
	m.refreshViewport()
	return tea.Batch(m.broadcastContextState(), m.broadcastAgentsState())
}

// openRootInProcess opens or activates a durable root without process restart.
func (m *Model) openRootInProcess(id string) tea.Cmd {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if id == m.sessionID {
		return m.closeSessionView()
	}
	if m.services.Roots == nil {
		return func() tea.Msg { return sessionResumeMsg{id: id} }
	}
	// Already live?
	for _, live := range m.services.Roots.LiveIDs() {
		if live == id {
			return m.activateRoot(id)
		}
	}
	if err := m.services.Roots.Open(id); err != nil {
		// Fall back to composition-root resume loop.
		return func() tea.Msg { return sessionResumeMsg{id: id} }
	}
	m.stashActiveRoot()
	p := m.ensureRootPane(id)
	if p != nil && m.services.Sessions != nil {
		if data, err := m.services.Sessions.ReplayJSONL(id); err == nil {
			if events, err := decodeSessionJSONL(data); err == nil {
				seedPaneFromReplay(p, events)
			}
		}
		if info, ok, err := m.services.Sessions.Get(id); err == nil && ok {
			if t := strings.TrimSpace(info.Title); t != "" {
				p.titleTopic = t
			}
		}
		if wd := m.services.Roots.WorkDir(id); wd != "" {
			p.workDir = wd
		}
	}
	m.loadRootPane(p)
	m.clearNotice()
	m.reflow()
	m.refreshViewport()
	m.viewport.GotoBottom()
	return tea.Batch(m.broadcastContextState(), m.broadcastAgentsState())
}

func seedPaneFromReplay(p *rootPane, events []protocol.Event) {
	if p == nil || len(events) == 0 {
		return
	}
	// Build a throwaway Model to reuse seedFromReplay, then copy fields.
	tmp := &Model{
		sessionID: p.sessionID,
		toolByID:  map[string]*toolCell{},
		autonomy:  protocol.AutonomySupervised,
	}
	seedFromReplay(tmp, events)
	p.cells = tmp.cells
	p.toolByID = tmp.toolByID
	p.children = tmp.children
	p.titleTopic = tmp.titleTopic
	p.providerName = tmp.providerName
	p.modelName = tmp.modelName
	p.agentName = tmp.agentName
	p.phaseName = tmp.phaseName
	p.phaseWorkflow = tmp.phaseWorkflow
	p.effort = tmp.effort
	p.autonomy = tmp.autonomy
	p.fastEnabled = tmp.fastEnabled
	p.usageInput = tmp.usageInput
	p.usageOutput = tmp.usageOutput
	p.usageUsed = tmp.usageUsed
	p.usageSource = tmp.usageSource
	p.turnRunning = false
	p.awaitingPermission = false
}

// interruptRoot sends Interrupt to a live root (empty = active).
func (m *Model) interruptRoot(id string) tea.Cmd {
	id = strings.TrimSpace(id)
	if m.services.Roots == nil {
		if id != "" && id != m.sessionID {
			m.setNotice("cannot interrupt inactive session", true)
			return nil
		}
		ops := m.ops
		return func() tea.Msg {
			ops <- protocol.Interrupt{}
			return nil
		}
	}
	if err := m.services.Roots.Interrupt(id); err != nil {
		m.setNotice("interrupt: "+err.Error(), true)
	}
	return nil
}

// rootAgentState derives theme state for a root id (live pane or active).
func (m Model) rootAgentState(id string) theme.AgentState {
	id = strings.TrimSpace(id)
	if id == "" || id == m.sessionID {
		return m.agentState()
	}
	if m.roots != nil {
		if p, ok := m.roots[id]; ok && p != nil {
			if p.awaitingPermission {
				return theme.AgentStateAttention
			}
			if p.turnRunning {
				return theme.AgentStateWorking
			}
			if p.sessionErrored {
				return theme.AgentStateError
			}
		}
	}
	return theme.AgentStateReady
}

// liveRootIDs returns concurrent root ids when Roots is available, else the
// single active session.
func (m Model) liveRootIDs() []string {
	if m.services.Roots != nil {
		ids := m.services.Roots.LiveIDs()
		if len(ids) > 0 {
			return ids
		}
	}
	if m.sessionID != "" {
		return []string{m.sessionID}
	}
	return nil
}

// rootTitleLabel is the tree label for a parent session.
func (m Model) rootTitleLabel(id string) string {
	id = strings.TrimSpace(id)
	if id == m.sessionID {
		if topic := strings.TrimSpace(m.titleTopic); topic != "" {
			return sanitizeTitleTopic(topic)
		}
	}
	if m.roots != nil {
		if p, ok := m.roots[id]; ok && p != nil {
			if topic := strings.TrimSpace(p.titleTopic); topic != "" {
				return sanitizeTitleTopic(topic)
			}
		}
	}
	if m.services.Sessions != nil {
		if s, ok, err := m.services.Sessions.Get(id); err == nil && ok {
			if t := strings.TrimSpace(s.Title); t != "" {
				return t
			}
		}
	}
	if short := shortSessionID(id); short != "" {
		return short
	}
	return "session"
}
