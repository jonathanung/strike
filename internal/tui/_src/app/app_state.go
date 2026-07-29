package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// clearUsage drops last-request token figures and catalog limits so a model
// switch never shows stale occupancy against a new window size. Session totals
// for /cost are kept — they span the whole session log.
func (m *Model) clearUsage() {
	m.usageInput = protocol.TokenCount{}
	m.usageOutput = protocol.TokenCount{}
	m.usageCacheRead = protocol.TokenCount{}
	m.usageCacheCreation = protocol.TokenCount{}
	m.usageUsed = protocol.TokenCount{}
	m.usageSource = ""
	m.modelInputCost = 0
	m.modelOutputCost = 0
	m.modelHasCost = false
	m.contextLimit = 0
	m.contextLimitKnown = false
	m.outputLimit = 0
	m.outputLimitKnown = false
	m.modelAttachment = false
	m.modelAttachmentKnown = false
}

// recordUsage updates last-request figures and session totals.
func (m *Model) recordUsage(ev protocol.UsageReported) {
	m.usageInput = ev.Input
	m.usageOutput = ev.Output
	m.usageCacheRead = ev.CacheRead
	m.usageCacheCreation = ev.CacheCreation
	m.usageUsed = ev.Used
	m.usageSource = ev.Source
	m.usageSession.add(ev)
}

// fetchContextLimitsCmd looks up context window, output limit, and pricing for
// the current provider/model via the host catalog (may hit network/cache).
func (m Model) fetchContextLimitsCmd() tea.Cmd {
	catalog := m.services.Catalog
	provider, model := m.providerName, m.modelName
	if catalog == nil || provider == "" {
		return nil
	}
	return func() tea.Msg {
		ctx := context.Background()
		ct, cok, _ := catalog.ContextWindow(ctx, provider, model)
		ot, ook, _ := catalog.OutputLimit(ctx, provider, model)
		msg := contextLimitsMsg{
			provider:      provider,
			model:         model,
			contextTokens: ct,
			contextOK:     cok,
			outputTokens:  ot,
			outputOK:      ook,
		}
		if infos, err := catalog.Models(ctx, provider); err == nil {
			for _, info := range infos {
				if info.ID == model {
					msg.attachment = info.Attachment
					msg.attachmentOK = true
					msg.inputCost = info.InputCost
					msg.outputCost = info.OutputCost
					msg.hasCost = info.HasCost
					break
				}
			}
		}
		return msg
	}
}

// contextStateSnapshot copies model-owned context fields for right-pane windows.
func (m Model) contextStateSnapshot() contextStateMsg {
	return contextStateMsg{
		WorkDir:           m.workDir,
		SessionID:         m.sessionID,
		SessionTitle:      m.titleTopic,
		Provider:          m.providerName,
		Model:             m.modelName,
		Agent:             m.agentName,
		AgentState:        m.agentState().Label(),
		Input:             m.usageInput,
		Output:            m.usageOutput,
		Used:              m.usageUsed,
		Source:            m.usageSource,
		ContextLimit:      m.contextLimit,
		ContextLimitKnown: m.contextLimitKnown,
		OutputLimit:       m.outputLimit,
		OutputLimitKnown:  m.outputLimitKnown,
	}
}

// broadcastContextState pushes the current snapshot to every right-pane window.
func (m *Model) broadcastContextState() tea.Cmd {
	var cmd tea.Cmd
	m.windows, cmd = m.windows.broadcast(m.contextStateSnapshot())
	return tea.Batch(cmd, m.broadcastVisualizerState())
}

// broadcastVisualizerState pushes selected-node stats to the visualizer window.
func (m *Model) broadcastVisualizerState() tea.Cmd {
	var cmd tea.Cmd
	m.windows, cmd = m.windows.broadcast(m.visualizerStateSnapshot())
	return cmd
}

// visualizerStateSnapshot builds live stats for vizFocusID / viewing / session.
func (m Model) visualizerStateSnapshot() visualizerStateMsg {
	id := strings.TrimSpace(m.vizFocusID)
	if id == "" {
		id = strings.TrimSpace(m.viewingID)
	}
	if id == "" {
		id = strings.TrimSpace(m.sessionID)
	}
	if id == "" {
		return visualizerStateMsg{}
	}

	msg := visualizerStateMsg{SessionID: id}

	// Child node?
	if ch, ok := m.findChildActivity(id); ok {
		msg.Kind = "child"
		msg.Label = childViewTitle(ch.agent, ch.prompt, ch.sessionID, ch.title)
		if msg.Label == "" {
			msg.Label = shortSessionID(id)
		}
		msg.StatusLabel = ch.status
		if msg.StatusLabel == "" {
			msg.StatusLabel = "unknown"
		}
		msg.State = childAgentState(ch.status)
		// Child token/cost stay unknown unless we later track per-child usage.
		// Never fabricate zeros from absence.
		return msg
	}

	// Root (active or stashed).
	msg.Kind = "root"
	msg.Label = m.rootTitleLabel(id)
	msg.State = m.rootAgentState(id)
	msg.StatusLabel = msg.State.Label()

	if id == m.sessionID {
		msg.Input = m.usageInput
		msg.Output = m.usageOutput
		msg.Used = m.usageUsed
		msg.Source = m.usageSource
		msg.ContextLimit = m.contextLimit
		msg.ContextLimitKnown = m.contextLimitKnown
		msg.Activity = usageActivitySamples(m.usageSession)
		msg.Tools = recentVisualizerTools(m.cells, 6)
		if usd, ok, partial := estimateUSD(m.usageSession, m.modelInputCost, m.modelOutputCost, m.modelHasCost); ok {
			msg.CostUSD, msg.CostOK, msg.CostPartial = usd, true, partial
		}
		return msg
	}

	if m.roots != nil {
		if p, ok := m.roots[id]; ok && p != nil {
			msg.Input = p.usageInput
			msg.Output = p.usageOutput
			msg.Used = p.usageUsed
			msg.Source = p.usageSource
			msg.ContextLimit = p.contextLimit
			msg.ContextLimitKnown = p.contextLimitKnown
			msg.Activity = usageActivitySamples(p.usageSession)
			msg.Tools = recentVisualizerTools(p.cells, 6)
			// Background roots share the active catalog rates when provider/model match.
			if p.providerName == m.providerName && p.modelName == m.modelName {
				if usd, ok, partial := estimateUSD(p.usageSession, m.modelInputCost, m.modelOutputCost, m.modelHasCost); ok {
					msg.CostUSD, msg.CostOK, msg.CostPartial = usd, true, partial
				}
			}
		}
	}
	return msg
}

// findChildActivity looks up a subagent row across the active root and stashes.
func (m Model) findChildActivity(id string) (childActivity, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return childActivity{}, false
	}
	for _, ch := range m.children {
		if ch.sessionID == id {
			return ch, true
		}
	}
	if m.roots != nil {
		for _, p := range m.roots {
			if p == nil {
				continue
			}
			for _, ch := range p.children {
				if ch.sessionID == id {
					return ch, true
				}
			}
		}
	}
	return childActivity{}, false
}

// usageActivitySamples extracts known turn magnitudes for the sparkline.
// Turns with no known token parts are skipped — never plotted as zero.
func usageActivitySamples(t usageTotals) []float64 {
	if len(t.Turns) == 0 {
		return nil
	}
	out := make([]float64, 0, len(t.Turns))
	for _, turn := range t.Turns {
		var v float64
		known := false
		if turn.Used.Known {
			v = float64(turn.Used.N)
			known = true
		} else {
			if turn.Input.Known {
				v += float64(turn.Input.N)
				known = true
			}
			if turn.Output.Known {
				v += float64(turn.Output.N)
				known = true
			}
		}
		if known {
			out = append(out, v)
		}
	}
	return out
}

// recentVisualizerTools collects the newest tool cells (parent transcript).
func recentVisualizerTools(cells []cell, limit int) []visualizerTool {
	if limit <= 0 || len(cells) == 0 {
		return nil
	}
	var tools []visualizerTool
	for i := len(cells) - 1; i >= 0 && len(tools) < limit; i-- {
		switch c := cells[i].(type) {
		case *toolCell:
			name := c.name
			if c.title != "" {
				name = c.title
			}
			tools = append(tools, visualizerTool{Name: name, Done: c.done, IsError: c.isError})
		case *exploreCell:
			for j := len(c.calls) - 1; j >= 0 && len(tools) < limit; j-- {
				tc := c.calls[j]
				if tc == nil {
					continue
				}
				name := tc.name
				if tc.title != "" {
					name = tc.title
				}
				tools = append(tools, visualizerTool{Name: name, Done: tc.done, IsError: tc.isError})
			}
		}
	}
	return tools
}

// agentsStateSnapshot pushes multi-root tree data into the agents window.
// Hidden ids (agentsHidden) are omitted from the tree only — LiveIDs and
// durable session storage are unchanged.
func (m Model) agentsStateSnapshot() agentsStateMsg {
	// Ensure active root is visible to the tree builder.
	roots := m.liveRootIDs()
	snaps := make([]agentsRootSnap, 0, len(roots))
	for _, id := range roots {
		if m.isAgentHidden(id) {
			continue
		}
		var kids []childActivity
		if id == m.sessionID {
			kids = append([]childActivity(nil), m.children...)
		} else if m.roots != nil {
			if p, ok := m.roots[id]; ok && p != nil {
				kids = append([]childActivity(nil), p.children...)
			}
		}
		kids = m.filterHiddenChildren(kids)
		snaps = append(snaps, agentsRootSnap{
			ID:       id,
			Title:    m.rootTitleLabel(id),
			State:    m.rootAgentState(id),
			Children: kids,
		})
	}
	viewing := m.viewingID
	if viewing == "" {
		viewing = m.sessionID
	}
	return agentsStateMsg{
		activeID:  m.sessionID,
		viewingID: viewing,
		roots:     snaps,
	}
}

func (m Model) isAgentHidden(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || m.agentsHidden == nil {
		return false
	}
	return m.agentsHidden[id]
}

func (m Model) filterHiddenChildren(kids []childActivity) []childActivity {
	if len(kids) == 0 || m.agentsHidden == nil {
		return kids
	}
	out := make([]childActivity, 0, len(kids))
	for _, ch := range kids {
		if m.agentsHidden[ch.sessionID] {
			continue
		}
		out = append(out, ch)
	}
	return out
}

// unhideAgent clears an ephemeral Agents-pane hide so the session reappears.
func (m *Model) unhideAgent(id string) {
	id = strings.TrimSpace(id)
	if id == "" || m.agentsHidden == nil {
		return
	}
	delete(m.agentsHidden, id)
}

// revealBusyHiddenAgents clears hide flags for roots/children that need the
// user or are running so a dismissed row cannot silently bury an active task.
func (m *Model) revealBusyHiddenAgents() {
	if m.agentsHidden == nil || len(m.agentsHidden) == 0 {
		return
	}
	for id := range m.agentsHidden {
		if m.agentBusyForReveal(id) {
			delete(m.agentsHidden, id)
		}
	}
}

func (m Model) agentBusyForReveal(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, live := range m.liveRootIDs() {
		if live != id {
			continue
		}
		st := m.rootAgentState(id)
		return st == theme.AgentStateWorking || st == theme.AgentStateAttention
	}
	if ch, ok := m.lookupChildActivity(id); ok {
		return ch.status == "running"
	}
	return false
}

func (m Model) lookupChildActivity(id string) (childActivity, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return childActivity{}, false
	}
	for _, ch := range m.children {
		if ch.sessionID == id {
			return ch, true
		}
	}
	if m.roots != nil {
		for _, p := range m.roots {
			if p == nil {
				continue
			}
			for _, ch := range p.children {
				if ch.sessionID == id {
					return ch, true
				}
			}
		}
	}
	return childActivity{}, false
}

// canHideAgentFromPane reports whether id may leave the Agents pane. Running
// and active roots are blocked so hide never interrupts or buries live work.
func (m Model) canHideAgentFromPane(id string) (ok bool, notice string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, "nothing selected"
	}
	if id == m.sessionID {
		return false, "cannot hide the active session"
	}
	for _, live := range m.liveRootIDs() {
		if live != id {
			continue
		}
		st := m.rootAgentState(id)
		if st == theme.AgentStateWorking || st == theme.AgentStateAttention {
			return false, "cannot hide a running agent"
		}
		return true, ""
	}
	if ch, found := m.lookupChildActivity(id); found {
		if ch.status == "running" {
			return false, "cannot hide a running agent"
		}
		return true, ""
	}
	return false, "session not in agents pane"
}

// lookupSessionTitle returns the durable host title for id, or "".
func (m Model) lookupSessionTitle(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || m.services.Sessions == nil {
		return ""
	}
	s, ok, err := m.services.Sessions.Get(id)
	if err != nil || !ok {
		return ""
	}
	return strings.TrimSpace(s.Title)
}

// applySessionRename updates live labels after a durable host rename.
func (m *Model) applySessionRename(id, title string) tea.Cmd {
	id = strings.TrimSpace(id)
	title = strings.TrimSpace(title)
	if id == "" {
		return nil
	}
	if id == m.sessionID {
		m.titleTopic = sanitizeTitleTopic(title)
	}
	if m.roots != nil {
		if p, ok := m.roots[id]; ok && p != nil {
			p.titleTopic = sanitizeTitleTopic(title)
		}
	}
	for i := range m.children {
		if m.children[i].sessionID == id {
			m.children[i].title = title
		}
	}
	if m.roots != nil {
		for _, p := range m.roots {
			if p == nil {
				continue
			}
			for i := range p.children {
				if p.children[i].sessionID == id {
					p.children[i].title = title
				}
			}
		}
	}
	if m.viewingID == id {
		if title != "" {
			m.viewTitle = sanitizeTitleTopic(title)
		} else {
			m.viewTitle = childViewTitle("", "", id, "")
		}
	}
	if title != "" {
		m.setNotice("renamed to "+sanitizeTitleTopic(title), false)
	} else {
		m.setNotice("title cleared", false)
	}
	var titleCmd tea.Cmd
	if id == m.sessionID {
		titleCmd = tea.SetWindowTitle(windowTitle(*m))
	}
	return tea.Batch(titleCmd, m.broadcastAgentsState(), m.broadcastContextState())
}

// openRenameModal opens the rename dialog for id (prefilled with current title).
func (m *Model) openRenameModal(id string) tea.Cmd {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if m.services.Sessions == nil {
		m.setNotice("session rename unavailable", true)
		return nil
	}
	current := m.lookupSessionTitle(id)
	if current == "" {
		if id == m.sessionID {
			current = strings.TrimSpace(m.titleTopic)
		} else if m.roots != nil {
			if p, ok := m.roots[id]; ok && p != nil {
				current = strings.TrimSpace(p.titleTopic)
			}
		}
	}
	if current == "" {
		if ch, ok := m.findChildActivity(id); ok {
			current = childViewTitle(ch.agent, ch.prompt, ch.sessionID, ch.title)
		}
	}
	m.modal = newRenameModal(m.services.Sessions, id, current, m.th)
	m.reflow()
	return nil
}

// handleAgentsHide dismisses id from the Agents pane only. Never deletes JSONL
// or sends Interrupt.
func (m *Model) handleAgentsHide(id string) tea.Cmd {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if ok, reason := m.canHideAgentFromPane(id); !ok {
		m.setNotice(reason, true)
		return nil
	}
	// Close a child transcript view of the hidden id so the left pane does not
	// keep showing a row that just left the tree.
	if m.viewingID == id && m.viewingChild() {
		_ = m.closeSessionView()
	}
	if m.agentsHidden == nil {
		m.agentsHidden = map[string]bool{}
	}
	m.agentsHidden[id] = true
	m.setNotice("hidden from agents pane (session kept)", false)
	return m.broadcastAgentsState()
}

// handleAgentsOpen activates a root or opens a child transcript from the tree.
func (m *Model) handleAgentsOpen(msg agentsOpenMsg) tea.Cmd {
	id := strings.TrimSpace(msg.sessionID)
	if id == "" {
		return nil
	}
	// Root selection.
	for _, live := range m.liveRootIDs() {
		if live == id {
			if msg.interrupt {
				return m.interruptRoot(id)
			}
			cmd := m.activateRoot(id)
			// Close child view when focusing the root itself.
			if m.viewingChild() {
				_ = m.closeSessionView()
			}
			return cmd
		}
	}
	// Child (or nested) transcript.
	if msg.interrupt {
		return m.interruptRoot(id)
	}
	// Ensure parent root is active when opening a child of another root.
	if parent := m.parentOfChild(id); parent != "" && parent != m.sessionID {
		if root := m.findLiveRootAncestor(parent); root != "" && root != m.sessionID {
			if cmd := m.activateRoot(root); cmd != nil {
				return tea.Batch(cmd, m.openSessionView(id))
			}
		}
	}
	return m.openSessionView(id)
}

// broadcastAgentsState pushes current subagent rows to every right-pane window.
func (m *Model) broadcastAgentsState() tea.Cmd {
	m.revealBusyHiddenAgents()
	var cmd tea.Cmd
	m.windows, cmd = m.windows.broadcast(m.agentsStateSnapshot())
	return tea.Batch(cmd, m.broadcastVisualizerState())
}

// hasContextMeter reports whether the header should show a compact usage chip.
func (m Model) hasContextMeter() bool {
	return m.usageUsed.Known || m.usageInput.Known || m.usageOutput.Known || m.contextLimitKnown
}

func (m Model) currentPaletteAvailability() paletteAvailability {
	return paletteAvailability{
		HasProvider: m.providerName != "",
		TurnRunning: m.turnRunning,
	}
}

func (m *Model) refreshOpenPalette() {
	if palette, ok := m.modal.(*paletteModal); ok {
		palette.refresh(buildPaletteEntries(m.commands, m.agents, m.currentPaletteAvailability()))
	}
}

func (m Model) currentPaletteEntry(action paletteAction) (paletteEntry, bool) {
	for _, entry := range buildPaletteEntries(m.commands, m.agents, m.currentPaletteAvailability()) {
		if entry.Action == action {
			return entry, true
		}
	}
	return paletteEntry{}, false
}

// handleCommand processes slash commands locally; they never reach the model.

func (m *Model) setNotice(text string, isErr bool) {
	m.notice, m.noticeErr = text, isErr
	m.noticeCause = noticeGeneral
}

func (m *Model) setNeedsModelNotice(text string, isErr bool) {
	m.notice, m.noticeErr = text, isErr
	m.noticeCause = noticeNeedsModel
}

func (m *Model) clearNotice() {
	m.notice, m.noticeErr = "", false
	m.noticeCause = noticeGeneral
}
