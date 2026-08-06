package tui

import (
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// maxTeamMessages caps the recent peer-message ring shown in the activity
// pane. Under broadcast storms older rows drop so render stays bounded.
const maxTeamMessages = 24

// maxTeamMessageBodyRunes truncates stored body text for UI labels/detail.
const maxTeamMessageBodyRunes = 280

// Bounds for multi-agent observability fields on childActivity (#922).
const (
	maxChildFilesTouched    = 12
	maxChildPathOverlaps    = 8
	maxChildObsFieldRunes   = 280
	maxChildObsWarningRunes = 160
)

// teamMessage is one peer/team mailbox delivery surfaced for the lead UI.
type teamMessage struct {
	id      string
	from    string
	to      string
	body    string
	summary string
	teamID  string
	taskID  string
	urgency string
	kind    string
	at      time.Time
}

func childStatusTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case string(protocol.ChildStatusCompleted), string(protocol.ChildStatusFailed),
		string(protocol.ChildStatusCanceled), "cancelled":
		return true
	default:
		return false
	}
}

// rosterStatusToChild maps team.roster / task_status vocabulary onto the
// childActivity status strings used by agents/activity panes.
func rosterStatusToChild(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "unknown":
		return ""
	case "starting", "working", "running", "needs_attention":
		return "running"
	case "completed":
		return string(protocol.ChildStatusCompleted)
	case "failed":
		return string(protocol.ChildStatusFailed)
	case "canceled", "cancelled":
		return string(protocol.ChildStatusCanceled)
	default:
		return strings.TrimSpace(state)
	}
}

// rosterDetailLabel is the agents-tree detail chip for a roster/task state.
func rosterDetailLabel(state, fallback string) string {
	s := strings.ToLower(strings.TrimSpace(state))
	switch s {
	case "starting":
		return "starting"
	case "working", "running":
		return "working"
	case "needs_attention":
		return "needs you"
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "canceled", "cancelled":
		return "canceled"
	case "unknown", "":
		if fallback != "" {
			return fallback
		}
		return "unknown"
	default:
		return s
	}
}

func trimTeamMessageBody(body string) string {
	body = strings.TrimSpace(body)
	// Collapse whitespace so activity rows stay single-line friendly.
	body = strings.Join(strings.Fields(body), " ")
	return truncateRunes(body, maxTeamMessageBodyRunes)
}

// applyTeamRosterMembers merges a team.roster snapshot into children.
// Lead entries are skipped (root is shown separately). index maps session id
// → slice index and is updated when new members are appended.
func applyTeamRosterMembers(children *[]childActivity, index map[string]int, members []protocol.TeamRosterMember, leadID string) {
	if children == nil {
		return
	}
	leadID = strings.TrimSpace(leadID)
	for _, mem := range members {
		id := strings.TrimSpace(mem.SessionID)
		if id == "" || id == "child" {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(mem.Role))
		if role == "lead" || (leadID != "" && id == leadID) {
			continue
		}
		status := rosterStatusToChild(mem.State)
		detail := rosterDetailLabel(mem.State, status)
		parentID := strings.TrimSpace(mem.ParentSessionID)
		if parentID == "" {
			parentID = leadID
		}
		if i, ok := index[id]; ok && i >= 0 && i < len(*children) {
			ch := &(*children)[i]
			if mem.Name != "" {
				ch.name = mem.Name
			}
			if mem.Agent != "" {
				ch.agent = mem.Agent
			}
			if parentID != "" && ch.parentID == "" {
				ch.parentID = parentID
			}
			// Never revive a terminal child from a late/stale roster snapshot.
			if status != "" && (!childStatusTerminal(ch.status) || childStatusTerminal(status)) {
				ch.status = status
			}
			if detail != "" {
				ch.rosterState = detail
			}
			// Roster queue fields refresh when present; empty clears only when
			// the snapshot explicitly omits them after a prior clear path.
			if len(mem.QueuePools) > 0 || mem.QueueLabel != "" {
				ch.queuePools = append([]string(nil), mem.QueuePools...)
				ch.queueLabel = mem.QueueLabel
			} else if len(ch.queuePools) > 0 && status != "" && childStatusTerminal(status) {
				ch.queuePools = nil
				ch.queueLabel = ""
				ch.queueRequestID = ""
			}
			if ch.startedAt.IsZero() && mem.StartedAt != "" {
				if t, err := time.Parse(time.RFC3339, mem.StartedAt); err == nil {
					ch.startedAt = t
				}
			}
			mergeChildObservabilityFromRoster(ch, mem)
			continue
		}
		ch := childActivity{
			sessionID:   id,
			parentID:    parentID,
			agent:       mem.Agent,
			name:        mem.Name,
			status:      status,
			rosterState: detail,
			queuePools:  append([]string(nil), mem.QueuePools...),
			queueLabel:  mem.QueueLabel,
		}
		if ch.status == "" {
			ch.status = "running"
		}
		if mem.StartedAt != "" {
			if t, err := time.Parse(time.RFC3339, mem.StartedAt); err == nil {
				ch.startedAt = t
			}
		}
		mergeChildObservabilityFromRoster(&ch, mem)
		if index != nil {
			index[id] = len(*children)
		}
		*children = append(*children, ch)
	}
}

// mergeChildObservabilityFromRoster copies live observability fields from a
// roster member. Wire-present strings/lists replace prior values; budget is
// cloned when non-nil and cleared when the snapshot omits tracking. Does not
// invent zeros or verified-success flags.
func mergeChildObservabilityFromRoster(ch *childActivity, mem protocol.TeamRosterMember) {
	if ch == nil {
		return
	}
	ch.objective = truncateRunes(strings.TrimSpace(mem.Objective), maxChildObsFieldRunes)
	ch.lastAction = truncateRunes(strings.TrimSpace(mem.LastAction), maxChildObsFieldRunes)
	ch.blockReason = truncateRunes(strings.TrimSpace(mem.BlockReason), maxChildObsFieldRunes)
	ch.filesTouched = boundStringList(mem.FilesTouched, maxChildFilesTouched)
	ch.budget = cloneAgentBudgetView(mem.Budget)
}

// cloneAgentBudgetView deep-copies a protocol budget snapshot (nil-safe).
func cloneAgentBudgetView(b *protocol.AgentBudgetView) *protocol.AgentBudgetView {
	if b == nil {
		return nil
	}
	cp := *b
	if b.WallClockRemainingS != nil {
		v := *b.WallClockRemainingS
		cp.WallClockRemainingS = &v
	}
	if b.TokensRemaining != nil {
		v := *b.TokensRemaining
		cp.TokensRemaining = &v
	}
	if b.ToolCallsRemaining != nil {
		v := *b.ToolCallsRemaining
		cp.ToolCallsRemaining = &v
	}
	if b.DangerousRemaining != nil {
		v := *b.DangerousRemaining
		cp.DangerousRemaining = &v
	}
	if b.CostUSDRemaining != nil {
		v := *b.CostUSDRemaining
		cp.CostUSDRemaining = &v
	}
	return &cp
}

// boundStringList copies and caps a string slice for render-storm safety.
func boundStringList(in []string, maxN int) []string {
	if len(in) == 0 {
		return nil
	}
	if maxN <= 0 {
		return nil
	}
	if len(in) > maxN {
		in = in[len(in)-maxN:]
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, truncateRunes(s, maxChildObsFieldRunes))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// applyChildEscalatedToChildren updates escalation + budget on the matching
// child row. Returns true when a row was updated or created.
func applyChildEscalatedToChildren(children *[]childActivity, index map[string]int, ev protocol.ChildEscalated) bool {
	if children == nil {
		return false
	}
	id := strings.TrimSpace(ev.SessionID)
	if id == "" {
		return false
	}
	kind := strings.TrimSpace(ev.Kind)
	reason := truncateRunes(strings.TrimSpace(ev.Reason), maxChildObsFieldRunes)
	action := strings.TrimSpace(ev.Action)
	parentID := strings.TrimSpace(ev.ParentSessionID)

	if i, ok := index[id]; ok && i >= 0 && i < len(*children) {
		ch := &(*children)[i]
		if ev.Name != "" {
			ch.name = ev.Name
		}
		if parentID != "" && ch.parentID == "" {
			ch.parentID = parentID
		}
		ch.escalateKind = kind
		ch.escalateReason = reason
		ch.escalateAction = action
		if reason != "" && ch.blockReason == "" {
			ch.blockReason = reason
		}
		if ev.Budget != nil {
			ch.budget = cloneAgentBudgetView(ev.Budget)
		}
		return true
	}
	// Escalation without a prior start still surfaces a row so viz can show it.
	ch := childActivity{
		sessionID:      id,
		parentID:       parentID,
		name:           ev.Name,
		status:         "running",
		escalateKind:   kind,
		escalateReason: reason,
		escalateAction: action,
		blockReason:    reason,
		budget:         cloneAgentBudgetView(ev.Budget),
		startedAt:      time.Now(),
	}
	if index != nil {
		index[id] = len(*children)
	}
	*children = append(*children, ch)
	return true
}

// applyPathOverlapToChildren retains a bounded path-overlap warning on the
// claiming session's child row. Returns true when applied.
func applyPathOverlapToChildren(children *[]childActivity, index map[string]int, ev protocol.PathOverlap) bool {
	if children == nil {
		return false
	}
	id := strings.TrimSpace(ev.SessionID)
	if id == "" {
		return false
	}
	entry := childPathOverlap{
		path:    truncateRunes(strings.TrimSpace(ev.Path), maxChildObsFieldRunes),
		policy:  strings.TrimSpace(ev.Policy),
		blocked: ev.Blocked,
		warning: truncateRunes(strings.TrimSpace(ev.Warning), maxChildObsWarningRunes),
	}
	if entry.path == "" && entry.warning == "" {
		return false
	}
	parentID := strings.TrimSpace(ev.ParentSessionID)

	if i, ok := index[id]; ok && i >= 0 && i < len(*children) {
		ch := &(*children)[i]
		if parentID != "" && ch.parentID == "" {
			ch.parentID = parentID
		}
		ch.pathOverlaps = appendPathOverlap(ch.pathOverlaps, entry)
		return true
	}
	ch := childActivity{
		sessionID:    id,
		parentID:     parentID,
		status:       "running",
		pathOverlaps: []childPathOverlap{entry},
		startedAt:    time.Now(),
	}
	if index != nil {
		index[id] = len(*children)
	}
	*children = append(*children, ch)
	return true
}

func appendPathOverlap(list []childPathOverlap, entry childPathOverlap) []childPathOverlap {
	// Dedup identical path+policy+blocked rows; keep newest warning text.
	for i := range list {
		if list[i].path == entry.path && list[i].policy == entry.policy && list[i].blocked == entry.blocked {
			list[i] = entry
			return list
		}
	}
	list = append(list, entry)
	if len(list) > maxChildPathOverlaps {
		list = append([]childPathOverlap(nil), list[len(list)-maxChildPathOverlaps:]...)
	}
	return list
}

// applyChildVerification stores a verification summary on the matching child.
// rep nil is a no-op (unknown stays unknown).
func applyChildVerification(children *[]childActivity, index map[string]int, sessionID string, rep *protocol.VerificationReport) bool {
	if children == nil || rep == nil {
		return false
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return false
	}
	sum := &childVerificationSummary{
		claimed:  rep.Claimed,
		verified: rep.Verified,
		passed:   rep.Passed,
		summary:  truncateRunes(strings.TrimSpace(rep.Summary), maxChildObsFieldRunes),
	}
	if i, ok := index[id]; ok && i >= 0 && i < len(*children) {
		(*children)[i].verification = sum
		return true
	}
	return false
}

// childIndex builds sessionID → slice index for children.
func childIndex(children []childActivity) map[string]int {
	index := make(map[string]int, len(children))
	for i, ch := range children {
		if ch.sessionID != "" {
			index[ch.sessionID] = i
		}
	}
	return index
}

func (m *Model) onTeamRoster(ev protocol.TeamRoster) {
	leadID := strings.TrimSpace(ev.LeadID)
	if leadID == "" {
		leadID = strings.TrimSpace(ev.SessionID)
	}
	index := childIndex(m.children)
	applyTeamRosterMembers(&m.children, index, ev.Members, leadID)
	m.trimChildren()
}

func (m *Model) onChildEscalated(ev protocol.ChildEscalated) {
	index := childIndex(m.children)
	if applyChildEscalatedToChildren(&m.children, index, ev) {
		m.trimChildren()
	}
}

func (m *Model) onPathOverlap(ev protocol.PathOverlap) {
	index := childIndex(m.children)
	if applyPathOverlapToChildren(&m.children, index, ev) {
		m.trimChildren()
	}
}

func applyTeamRosterToPane(p *rootPane, ev protocol.TeamRoster) {
	if p == nil {
		return
	}
	leadID := strings.TrimSpace(ev.LeadID)
	if leadID == "" {
		leadID = strings.TrimSpace(ev.SessionID)
	}
	if leadID == "" {
		leadID = p.sessionID
	}
	index := childIndex(p.children)
	applyTeamRosterMembers(&p.children, index, ev.Members, leadID)
	trimPaneChildren(p)
}

func applyChildEscalatedToPane(p *rootPane, ev protocol.ChildEscalated) {
	if p == nil {
		return
	}
	index := childIndex(p.children)
	if applyChildEscalatedToChildren(&p.children, index, ev) {
		trimPaneChildren(p)
	}
}

func applyPathOverlapToPane(p *rootPane, ev protocol.PathOverlap) {
	if p == nil {
		return
	}
	index := childIndex(p.children)
	if applyPathOverlapToChildren(&p.children, index, ev) {
		trimPaneChildren(p)
	}
}

func trimPaneChildren(p *rootPane) {
	if p == nil || len(p.children) <= maxChildActivity {
		return
	}
	// Drop oldest non-running first (same spirit as Model.trimChildren).
	for len(p.children) > maxChildActivity {
		drop := -1
		for i, ch := range p.children {
			if ch.status != "running" {
				drop = i
				break
			}
		}
		if drop < 0 {
			drop = 0
		}
		p.children = append(p.children[:drop], p.children[drop+1:]...)
	}
}

func (m *Model) onAgentMessage(ev protocol.AgentMessage) {
	m.teamMessages = appendTeamMessage(m.teamMessages, teamMessageFromEvent(ev))
}

func applyAgentMessageToPane(p *rootPane, ev protocol.AgentMessage) {
	if p == nil {
		return
	}
	p.teamMessages = appendTeamMessage(p.teamMessages, teamMessageFromEvent(ev))
}

func teamMessageFromEvent(ev protocol.AgentMessage) teamMessage {
	id := strings.TrimSpace(ev.MessageID)
	if id == "" {
		// Stable-enough fallback for dedup within a short window.
		id = strings.TrimSpace(ev.From) + "→" + strings.TrimSpace(ev.To) + ":" + trimTeamMessageBody(ev.Body)
	}
	return teamMessage{
		id:      id,
		from:    strings.TrimSpace(ev.From),
		to:      strings.TrimSpace(ev.To),
		body:    trimTeamMessageBody(ev.Body),
		summary: strings.TrimSpace(ev.Summary),
		teamID:  strings.TrimSpace(ev.TeamID),
		taskID:  strings.TrimSpace(ev.TaskID),
		urgency: strings.ToLower(strings.TrimSpace(ev.Urgency)),
		kind:    strings.ToLower(strings.TrimSpace(ev.Kind)),
		at:      time.Now(),
	}
}

func appendTeamMessage(msgs []teamMessage, msg teamMessage) []teamMessage {
	if msg.body == "" && msg.summary == "" && msg.from == "" && msg.to == "" {
		return msgs
	}
	if msg.id != "" {
		for _, existing := range msgs {
			if existing.id == msg.id {
				return msgs
			}
		}
	}
	msgs = append(msgs, msg)
	if len(msgs) > maxTeamMessages {
		msgs = append([]teamMessage(nil), msgs[len(msgs)-maxTeamMessages:]...)
	}
	return msgs
}

// teamMessagesFromEvents rebuilds the recent-message ring from a session log.
// Only the trailing maxTeamMessages deliveries are kept (newest last).
func teamMessagesFromEvents(events []protocol.Event) []teamMessage {
	var out []teamMessage
	for _, ev := range events {
		am, ok := ev.(protocol.AgentMessage)
		if !ok {
			continue
		}
		// Skip child-session correlation noise when ParentSessionID is set on
		// non-bubbled lines; parent re-emits keep recipient correlation but
		// still carry body — include all AgentMessage rows in the log.
		out = appendTeamMessage(out, teamMessageFromEvent(am))
	}
	return out
}

// resolveTeamMsgNav picks a teammate session id to open from a message row.
// Prefers a non-lead child sender, then recipient, then any known child id.
func (m Model) resolveTeamMsgNav(from, to string) string {
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	lead := strings.TrimSpace(m.sessionID)
	try := func(id string) string {
		id = strings.TrimSpace(id)
		if id == "" || id == lead {
			return ""
		}
		if _, ok := m.findChildActivity(id); ok {
			return id
		}
		// Also allow opening via host sessions even if the row aged out of
		// the bounded children list.
		if m.services.Sessions != nil {
			if _, ok, err := m.services.Sessions.Get(id); err == nil && ok {
				return id
			}
		}
		return ""
	}
	if id := try(from); id != "" {
		return id
	}
	if id := try(to); id != "" {
		return id
	}
	return ""
}

func teamMsgActivityLabel(msg teamMessage, resolveName func(id string) string) string {
	resolve := func(id string) string {
		if resolveName != nil {
			if s := resolveName(id); s != "" {
				return s
			}
		}
		return shortSessionID(id)
	}
	from := resolve(msg.from)
	if from == "" {
		from = "?"
	}
	to := resolve(msg.to)
	if to == "" {
		to = "?"
	}
	label := "msg " + from + "→" + to
	switch msg.urgency {
	case "blocker":
		label = "!! " + label
	case "high":
		label = "! " + label
	}
	if k := strings.TrimSpace(msg.kind); k != "" && k != "message" {
		label = label + " [" + k + "]"
	}
	if tid := strings.TrimSpace(msg.taskID); tid != "" {
		label = label + " #" + shortSessionID(tid)
	}
	snippet := strings.TrimSpace(msg.summary)
	if snippet == "" {
		snippet = msg.body
	}
	snippet = truncateRunes(snippet, 48)
	if snippet != "" {
		label = label + " " + snippet
	}
	return label
}

func (m Model) teamMemberLabel(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if id == m.sessionID {
		if t := strings.TrimSpace(m.titleTopic); t != "" {
			return t
		}
		return "lead"
	}
	if ch, ok := m.findChildActivity(id); ok {
		return childViewTitle(ch.agent, ch.prompt, ch.sessionID, ch.title, ch.name)
	}
	return ""
}
