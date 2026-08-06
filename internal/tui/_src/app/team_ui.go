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
		if index != nil {
			index[id] = len(*children)
		}
		*children = append(*children, ch)
	}
}

func (m *Model) onTeamRoster(ev protocol.TeamRoster) {
	leadID := strings.TrimSpace(ev.LeadID)
	if leadID == "" {
		leadID = strings.TrimSpace(ev.SessionID)
	}
	index := make(map[string]int, len(m.children))
	for i, ch := range m.children {
		if ch.sessionID != "" {
			index[ch.sessionID] = i
		}
	}
	applyTeamRosterMembers(&m.children, index, ev.Members, leadID)
	m.trimChildren()
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
	index := make(map[string]int, len(p.children))
	for i, ch := range p.children {
		if ch.sessionID != "" {
			index[ch.sessionID] = i
		}
	}
	applyTeamRosterMembers(&p.children, index, ev.Members, leadID)
	if len(p.children) > maxChildActivity {
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
