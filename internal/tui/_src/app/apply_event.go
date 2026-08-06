package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func (m *Model) applyEvent(ev protocol.Event) tea.Cmd {
	// Fold every event that reaches the UI into the structured run timeline
	// before child filtering so parent-visible child lifecycle still lands.
	m.observeTimeline(ev, time.Now())
	// Defense-in-depth: child-session events should only surface permissions,
	// questions, and child lifecycle (activity pane). Primary filtering is in
	// the engine; ChildStarted/Completed must reach the TUI for subagent UI.
	if corr, ok := eventCorrelation(ev); ok && (corr.ParentSessionID != "" || corr.Depth > 0) {
		switch ev.(type) {
		case protocol.PermissionAsked, protocol.PermissionResolved,
			protocol.QuestionAsked, protocol.QuestionResolved,
			protocol.ChildStarted, protocol.ChildCompleted,
			// Parent re-emits child peer mail + nested roster with child
			// correlation; keep them for team UI (issue #614).
			protocol.AgentMessage, protocol.TeamRoster,
			// Queue lifecycle for children: show constrained pool, not idle.
			protocol.SchedulerQueued, protocol.SchedulerAdmitted, protocol.SchedulerCanceled:
		default:
			return nil
		}
	}
	// Status coloring tracks protocol facts before view-side side effects so
	// agentState never depends on modal type checks.
	m.applyAgentStateEvent(ev)
	var cmd tea.Cmd
	switch ev := ev.(type) {
	case protocol.UserMessage:
		m.completeAssistantCells()
		// Model-facing [child.completed] notices stay off the transcript UI;
		// subagentResultCell from ChildCompleted is the parent-facing row.
		if isChildCompletedNotice(ev.Text) {
			break
		}
		display := userMessageDisplayText(ev.Text, ev.Images)
		m.cells = append(m.cells, &userCell{text: display})
		// Fallback for logs without session.titled (pre-auto-title sessions).
		// Use display text so @file attachment bodies do not pollute the title.
		if m.titleTopic == "" {
			if topic := sanitizeTitleTopic(display); topic != "" {
				m.titleTopic = topic
				cmd = m.broadcastContextState()
			}
		}
	case protocol.SessionTitled:
		if topic := sanitizeTitleTopic(ev.Title); topic != "" {
			m.titleTopic = topic
			cmd = m.broadcastContextState()
		}
	case protocol.TurnStarted:
		m.turnStartedAt = time.Now()
		m.toolCallsThisTurn = 0
		m.refreshOpenPalette()
		cmd = tea.Batch(m.broadcastContextState(), m.spinTickCmd())
	case protocol.TextDelta:
		if last, ok := lastCell[*assistantCell](m.cells); ok {
			last.text += ev.Text
		} else {
			m.cells = append(m.cells, &assistantCell{text: ev.Text})
		}
	case protocol.ReasoningDelta:
		if ev.Text == "" {
			break
		}
		if last, ok := lastCell[*reasoningCell](m.cells); ok {
			last.text += ev.Text
		} else {
			m.cells = append(m.cells, &reasoningCell{text: ev.Text})
		}
	case protocol.ToolCallBegin:
		if last, ok := lastCell[*assistantCell](m.cells); ok {
			last.complete = true
			last.mdCacheOK = false
		}
		m.toolCallsThisTurn++
		// Coalesce consecutive sleep ticks into one in-place row (no spam).
		if ev.Name == "sleep" {
			m.cells = beginSleepToolCell(m.cells, m.toolByID, ev.CallID, ev.Name, ev.Args)
		} else {
			tc := &toolCell{callID: ev.CallID, name: ev.Name, args: ev.Args}
			m.toolByID[ev.CallID] = tc
			if isExploreTool(ev.Name) {
				if exp, ok := lastCell[*exploreCell](m.cells); ok && exp.accepting {
					exp.calls = append(exp.calls, tc)
				} else if prev, ok := lastCell[*toolCell](m.cells); ok && isExploreTool(prev.name) {
					// First explore tool stays a normal cell; a second consecutive
					// one promotes the pair into an exploring group.
					m.cells[len(m.cells)-1] = &exploreCell{
						calls:     []*toolCell{prev, tc},
						accepting: true,
					}
				} else {
					m.cells = append(m.cells, tc)
				}
			} else {
				if exp, ok := lastCell[*exploreCell](m.cells); ok {
					exp.accepting = false
				}
				m.cells = append(m.cells, tc)
			}
		}
		// Push tool strip mid-turn (not only on TurnCompleted) so activity/
		// visualizer show shell and other tools as they start (#625).
		cmd = m.broadcastVisualizerState()
	case protocol.ToolCallOutput:
		if tc, ok := m.toolByID[ev.CallID]; ok && !tc.done {
			tc.output += ev.Data
		}
	case protocol.ToolCallEnd:
		if tc, ok := m.toolByID[ev.CallID]; ok {
			applyToolCallEnd(tc, ev.Title, ev.Output, ev.Metadata, ev.IsError)
			if isProjectDataTool(tc.name) {
				m.windows = refreshProjectDataWindows(m.windows)
			}
			if isWorkspaceFSTool(tc.name) {
				m.windows = refreshFilesWindows(m.windows)
				m.windows = refreshDiagnosticsWindows(m.windows)
			}
		}
		cmd = m.broadcastVisualizerState()
	case protocol.PermissionAsked:
		pm := newPermissionModal(ev, m.ops, m.th)
		showCmd := m.presentBlockingModal(pm)
		m.refreshAwaitingPermission()
		cmd = m.broadcastContextState()
		if showCmd != nil {
			cmd = tea.Batch(cmd, showCmd)
		}
		// Static message only — never include paths, args, or secrets.
		cmd = tea.Batch(cmd, m.desktopNotifyCmd("strike: permission required", true))
	case protocol.PermissionResolved:
		if promote := m.resolveBlockingRequest(ev.RequestID); promote != nil {
			cmd = promote
		}
		m.refreshAwaitingPermission()
		cmd = tea.Batch(cmd, m.broadcastContextState())
	case protocol.QuestionAsked:
		qm := newQuestionModal(ev, m.ops, m.th)
		showCmd := m.presentBlockingModal(qm)
		m.refreshAwaitingPermission()
		cmd = m.broadcastContextState()
		if showCmd != nil {
			cmd = tea.Batch(cmd, showCmd)
		}
		cmd = tea.Batch(cmd, m.desktopNotifyCmd("strike: question required", true))
	case protocol.QuestionResolved:
		if promote := m.resolveBlockingRequest(ev.RequestID); promote != nil {
			cmd = promote
		}
		m.refreshAwaitingPermission()
		cmd = tea.Batch(cmd, m.broadcastContextState())
	case protocol.TurnCompleted:
		m.completeAssistantCells()
		if exp, ok := lastCell[*exploreCell](m.cells); ok {
			exp.accepting = false
		}
		notify := m.desktopNotifyCmd("strike: turn complete", false)
		m.turnStartedAt = time.Time{}
		m.toolCallsThisTurn = 0
		m.refreshOpenPalette()
		if ev.StopReason == "interrupted" {
			m.setNotice("interrupted", false)
		}
		// turnRunning is already false via applyAgentStateEvent; drain next prompt.
		cmd = tea.Batch(m.broadcastContextState(), notify, m.tryDrainInputQueue())
	case protocol.HarnessProgress:
		// Surface harness progress as an info cell in the transcript.
		if m.turnRunning {
			payload := string(ev.Payload)
			if payload == "" || payload == "null" {
				payload = ev.Name
			} else {
				payload = ev.Name + ": " + payload
			}
			m.cells = append(m.cells, &infoCell{text: payload})
		}
	case protocol.ModelSelected:
		if m.noticeCause == noticeNeedsModel {
			m.clearNotice()
		}
		m.providerName, m.modelName = ev.Provider, ev.Model
		m.syncFTUEState()
		m.clearUsage()
		m.refreshOpenPalette()
		cmd = tea.Batch(m.fetchContextLimitsCmd(), m.broadcastContextState(), m.authExpiryNoticeCmd())
	case protocol.AgentSelected:
		m.agentName = ev.Name
		cmd = m.broadcastContextState()
	case protocol.PhaseChanged:
		m.phaseName = ev.Phase
		m.phaseWorkflow = ev.Workflow
		m.phaseGate = ev.Gate
		m.phaseSource = ev.Source
		m.phaseFingerprint = ev.Fingerprint
		m.phaseStatus = ev.Status
		if ev.Phase == "" && ev.Workflow == "" {
			// Cleared — drop identity so chrome does not stale-show grants.
			m.phaseGate = ""
			m.phaseSource = ""
			m.phaseFingerprint = ""
			m.phaseStatus = ""
		}
		cmd = m.broadcastContextState()
	case protocol.EffortSelected:
		m.effort = ev.Level
		m.setNotice("effort: "+detailJoin(m.th, string(ev.Level), ev.Level.Describe()), false)
	case protocol.AutonomySelected:
		m.autonomy = ev.Mode.Normalize()
		m.setNotice("autonomy: "+detailJoin(m.th, string(m.autonomy), m.autonomy.Describe()), false)
	case protocol.PermissionModeSelected:
		m.permMode = ev.Mode.Normalize()
		m.setNotice("mode: "+detailJoin(m.th, string(m.permMode), m.permMode.Describe()), false)
		cmd = m.broadcastContextState()
	case protocol.FastSelected:
		m.fastEnabled = ev.Enabled
		m.setNotice(m.fastNotice(ev.Enabled), false)
	case protocol.FilesInvalidated:
		m.windows = refreshFilesWindows(m.windows)
		m.windows = refreshDiagnosticsWindows(m.windows)
		if len(ev.Paths) == 0 {
			break
		}
		label := strings.Join(ev.Paths, ", ")
		m.setNotice("files changed — agent will re-read: "+label, false)
	case protocol.UsageReported:
		m.recordUsage(ev)
		cmd = m.broadcastContextState()
	case protocol.EffectivePrompt:
		if m.pendingContextDoctor {
			m.pendingContextDoctor = false
			m.modal = newDoctorModal(ev, m.contextLimit, m.contextLimitKnown)
			m.reflow()
		} else {
			m.cells = append(m.cells, &infoCell{text: formatEffectivePrompt(ev)})
		}
	case protocol.CompactionCompleted:
		strategy := ev.Strategy
		if strategy == "" {
			strategy = protocol.CompactionStrategyTrim
		}
		msg := fmt.Sprintf("history compacted (%s/%s): removed %d, kept %d", ev.Reason, strategy, ev.Removed, ev.Kept)
		if m.turnRunning {
			m.cells = append(m.cells, &errorCell{text: msg})
		} else {
			m.setNotice(msg, false)
		}
		cmd = m.broadcastContextState()
	case protocol.SessionRewound:
		m.cells, m.toolByID = dropLastUserTurnCells(m.cells, m.toolByID)
		m.selectedCell = -1
		m.selectedFileRef = -1
		m.setNotice(formatSessionRewound(ev), false)
		cmd = m.broadcastContextState()
	case protocol.EngineError:
		// Mid-turn failures belong in the transcript; idle-state errors
		// (no model selected, bad /provider, …) show in the notice line.
		// Do not mark assistants complete here: non-terminal errors (e.g.
		// "turn already running") must not freeze a live stream.
		if m.turnRunning {
			m.cells = append(m.cells, &errorCell{text: ev.Message})
		} else {
			if ev.Message == "no model selected — use /provider <anthropic|openai|xai|google|kimi|deepseek|echo> [model]" {
				m.setNeedsModelNotice(ev.Message, true)
			} else {
				m.setNotice(ev.Message, true)
			}
		}
		cmd = m.broadcastContextState()
	case protocol.ChildStarted:
		m.onChildStarted(ev)
		cmd = m.broadcastAgentsState()
	case protocol.ChildCompleted:
		m.onChildCompleted(ev)
		cmd = m.broadcastAgentsState()
		// Meaningful lifecycle transition only (not sleep/poll ticks).
		status := string(ev.Status)
		if status == "" {
			status = string(protocol.ChildStatusCompleted)
		}
		attention := status == string(protocol.ChildStatusFailed) ||
			status == string(protocol.ChildStatusCanceled)
		cmd = tea.Batch(cmd, m.desktopNotifyCmd("strike: subagent "+status, attention))
		if m.viewingChild() && (ev.SessionID == m.viewingID || ev.SessionID == "") {
			if refresh := m.refreshViewingTranscript(); refresh != nil {
				cmd = tea.Batch(cmd, refresh)
			}
		}
	case protocol.TeamRoster:
		m.onTeamRoster(ev)
		cmd = m.broadcastAgentsState()
	case protocol.AgentMessage:
		m.onAgentMessage(ev)
	case protocol.SchedulerQueued:
		m.onSchedulerQueued(ev)
		cmd = m.broadcastAgentsState()
	case protocol.SchedulerAdmitted:
		m.onSchedulerAdmitted(ev)
		cmd = m.broadcastAgentsState()
	case protocol.SchedulerCanceled:
		m.onSchedulerCanceled(ev)
		cmd = m.broadcastAgentsState()
	}
	return cmd
}

// onSchedulerQueued marks root or child as waiting on named pools.
func (m *Model) onSchedulerQueued(ev protocol.SchedulerQueued) {
	if applySchedulerQueuedToChildren(&m.children, ev) {
		return
	}
	// Root / this session.
	if ev.SessionID == "" || ev.SessionID == m.sessionID {
		m.queueRequestID = ev.RequestID
		m.queuePools = append([]string(nil), ev.Pools...)
		m.queueLabel = ev.Label
	}
}

func (m *Model) onSchedulerAdmitted(ev protocol.SchedulerAdmitted) {
	if applySchedulerClearToChildren(&m.children, ev.RequestID, ev.SessionID) {
		return
	}
	if ev.SessionID == "" || ev.SessionID == m.sessionID {
		if m.queueRequestID == "" || m.queueRequestID == ev.RequestID {
			m.queueRequestID = ""
			m.queuePools = nil
			m.queueLabel = ""
		}
	}
}

func (m *Model) onSchedulerCanceled(ev protocol.SchedulerCanceled) {
	if applySchedulerClearToChildren(&m.children, ev.RequestID, ev.SessionID) {
		return
	}
	if ev.SessionID == "" || ev.SessionID == m.sessionID {
		if m.queueRequestID == "" || m.queueRequestID == ev.RequestID {
			m.queueRequestID = ""
			m.queuePools = nil
			m.queueLabel = ""
		}
	}
}

func applySchedulerQueuedToChildren(children *[]childActivity, ev protocol.SchedulerQueued) bool {
	if children == nil || ev.SessionID == "" {
		return false
	}
	for i := range *children {
		if (*children)[i].sessionID != ev.SessionID {
			continue
		}
		(*children)[i].queueRequestID = ev.RequestID
		(*children)[i].queuePools = append([]string(nil), ev.Pools...)
		(*children)[i].queueLabel = ev.Label
		// Keep status live while queued so agents filter does not drop to ready.
		if (*children)[i].status == "" || (*children)[i].status == "running" {
			(*children)[i].status = "running"
		}
		return true
	}
	return false
}

func applySchedulerClearToChildren(children *[]childActivity, requestID, sessionID string) bool {
	if children == nil || sessionID == "" {
		return false
	}
	for i := range *children {
		if (*children)[i].sessionID != sessionID {
			continue
		}
		ch := &(*children)[i]
		if ch.queueRequestID == "" || ch.queueRequestID == requestID {
			ch.queueRequestID = ""
			ch.queuePools = nil
			ch.queueLabel = ""
		}
		return true
	}
	return false
}

const maxChildActivity = 12

func (m *Model) onChildStarted(ev protocol.ChildStarted) {
	id := ev.SessionID
	if id == "" {
		id = "child"
	}
	parentID := ev.ParentSessionID
	now := time.Now()
	for i := range m.children {
		if m.children[i].sessionID == id {
			m.children[i].agent = ev.Agent
			m.children[i].prompt = ev.Prompt
			m.children[i].name = ev.Name
			m.children[i].status = "running"
			if parentID != "" {
				m.children[i].parentID = parentID
			}
			if m.children[i].startedAt.IsZero() {
				m.children[i].startedAt = now
			}
			m.children[i].endedAt = time.Time{}
			if m.children[i].title == "" {
				m.children[i].title = m.lookupSessionTitle(id)
			}
			return
		}
	}
	m.children = append(m.children, childActivity{
		sessionID: id,
		parentID:  parentID,
		agent:     ev.Agent,
		prompt:    ev.Prompt,
		name:      ev.Name,
		title:     m.lookupSessionTitle(id),
		status:    "running",
		startedAt: now,
	})
	m.trimChildren()
}

func (m *Model) onChildCompleted(ev protocol.ChildCompleted) {
	id := ev.SessionID
	status := string(ev.Status)
	if status == "" {
		status = string(protocol.ChildStatusCompleted)
	}
	now := time.Now()
	applyChildCompletedToTaskCells(m.toolByID, ev)
	matched := false
	for i := range m.children {
		if m.children[i].sessionID == id || (id == "" && i == len(m.children)-1) {
			m.children[i].status = status
			if ev.Name != "" {
				m.children[i].name = ev.Name
			}
			if ev.ParentSessionID != "" && m.children[i].parentID == "" {
				m.children[i].parentID = ev.ParentSessionID
			}
			m.children[i].endedAt = now
			matched = true
			break
		}
	}
	if !matched {
		// Completed without a matching start still surfaces briefly.
		if id == "" {
			id = "child"
		}
		m.children = append(m.children, childActivity{
			sessionID: id,
			parentID:  ev.ParentSessionID,
			name:      ev.Name,
			status:    status,
			startedAt: now,
			endedAt:   now,
		})
		m.trimChildren()
	}
	agent, elapsed := lookupChildMeta(m.children, ev.SessionID)
	m.cells = appendSubagentResultCell(m.cells, ev, agent, elapsed)
	// plan_delegate may have applied section CAS on finish — refresh plan progress
	// without touching agents/activity focus state.
	m.windows = refreshProjectDataWindows(m.windows)
}

func (m *Model) trimChildren() {
	if len(m.children) <= maxChildActivity {
		return
	}
	// Drop oldest non-running first; if still over, drop oldest overall.
	for len(m.children) > maxChildActivity {
		drop := -1
		for i, ch := range m.children {
			if ch.status != "running" {
				drop = i
				break
			}
		}
		if drop < 0 {
			drop = 0
		}
		m.children = append(m.children[:drop], m.children[drop+1:]...)
	}
}

// eventCorrelation extracts lineage fields when the event embeds Correlation.
func eventCorrelation(ev protocol.Event) (protocol.Correlation, bool) {
	switch e := ev.(type) {
	case protocol.UserMessage:
		return e.Correlation, true
	case protocol.SessionTitled:
		return e.Correlation, true
	case protocol.TurnStarted:
		return e.Correlation, true
	case protocol.TextDelta:
		return e.Correlation, true
	case protocol.ReasoningDelta:
		return e.Correlation, true
	case protocol.ToolCallBegin:
		return e.Correlation, true
	case protocol.ToolCallEnd:
		return e.Correlation, true
	case protocol.ToolCallOutput:
		return e.Correlation, true
	case protocol.ProcessStarted:
		return e.Correlation, true
	case protocol.ProcessOutput:
		return e.Correlation, true
	case protocol.ProcessExited:
		return e.Correlation, true
	case protocol.PermissionAsked:
		return e.Correlation, true
	case protocol.PermissionResolved:
		return e.Correlation, true
	case protocol.QuestionAsked:
		return e.Correlation, true
	case protocol.QuestionResolved:
		return e.Correlation, true
	case protocol.TurnCompleted:
		return e.Correlation, true
	case protocol.HarnessProgress:
		return e.Correlation, true
	case protocol.ModelSelected:
		return e.Correlation, true
	case protocol.AgentSelected:
		return e.Correlation, true
	case protocol.PhaseChanged:
		return e.Correlation, true
	case protocol.EffortSelected:
		return e.Correlation, true
	case protocol.AutonomySelected:
		return e.Correlation, true
	case protocol.FastSelected:
		return e.Correlation, true
	case protocol.UsageReported:
		return e.Correlation, true
	case protocol.CompactionStarted:
		return e.Correlation, true
	case protocol.CompactionCompleted:
		return e.Correlation, true
	case protocol.EffectivePrompt:
		return e.Correlation, true
	case protocol.EngineError:
		return e.Correlation, true
	case protocol.ChildStarted:
		return e.Correlation, true
	case protocol.ChildCompleted:
		return e.Correlation, true
	case protocol.WaitStarted:
		return e.Correlation, true
	case protocol.WaitResolved:
		return e.Correlation, true
	case protocol.AgentMessage:
		return e.Correlation, true
	case protocol.TeamRoster:
		return e.Correlation, true
	case protocol.SchedulerQueued:
		return e.Correlation, true
	case protocol.SchedulerAdmitted:
		return e.Correlation, true
	case protocol.SchedulerCanceled:
		return e.Correlation, true
	case protocol.ProviderRetrying:
		return e.Correlation, true
	case protocol.PermissionModeSelected:
		return e.Correlation, true
	case protocol.FilesInvalidated:
		return e.Correlation, true
	case protocol.SessionMeta:
		return e.Correlation, true
	case protocol.SessionRewound:
		return e.Correlation, true
	case protocol.HookMatched:
		return e.Correlation, true
	default:
		return protocol.Correlation{}, false
	}
}

// formatEffectivePrompt renders a compact layer map for the transcript.
func formatEffectivePrompt(ev protocol.EffectivePrompt) string {
	var b strings.Builder
	scope := "current composition"
	if ev.FromLastStream {
		scope = "last request"
	}
	fmt.Fprintf(&b, "effective prompt (%s) - system %d chars - history %d msgs",
		scope, ev.SystemChars, ev.MessageCount)
	if a := ev.Attribution; a.Source != "" || a.Total.Known {
		src := a.Source
		if src == "" {
			src = protocol.UsageSourceEstimated
		}
		fmt.Fprintf(&b, "\n  request ~tok (%s): system %s / tools %s / messages %s / tool_results %s / total %s",
			src,
			formatAttrTok(a.System),
			formatAttrTok(a.Tools),
			formatAttrTok(a.Messages),
			formatAttrTok(a.ToolResults),
			formatAttrTok(a.Total),
		)
	}
	if len(ev.Layers) == 0 {
		b.WriteString("\n  (no layers)")
		return b.String()
	}
	for i, layer := range ev.Layers {
		kind := sanitizeDisplayData(layer.Kind)
		source := sanitizeDisplayData(layer.Source)
		mode := sanitizeDisplayData(layer.Mode)
		fmt.Fprintf(&b, "\n  %d. %s [%s] %s - %d chars",
			i+1, kind, mode, source, layer.Chars)
	}
	return b.String()
}

func formatAttrTok(tc protocol.TokenCount) string {
	if !tc.Known {
		return "?"
	}
	return fmt.Sprintf("~%d", tc.N)
}

func lastCell[T cell](cells []cell) (T, bool) {
	var zero T
	if len(cells) == 0 {
		return zero, false
	}
	last, ok := cells[len(cells)-1].(T)
	return last, ok
}

// completeAssistantCells marks every assistant transcript cell complete so
// markdown rendering runs for finished replies (including those no longer trailing).
func (m *Model) completeAssistantCells() {
	completeAssistantCellsIn(m.cells)
}

// completeAssistantCellsIn marks assistant cells complete and drops markdown
// cache so the next render runs glamour (active model and background root panes).
func completeAssistantCellsIn(cells []cell) {
	for _, c := range cells {
		if a, ok := c.(*assistantCell); ok {
			a.complete = true
			a.mdCacheOK = false
		}
	}
}
