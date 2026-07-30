package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// agentRoster returns the implicit team snapshot for agent_roster.
// Available on lead and children (shared Team pointer). Live children owned
// by this engine get task_status-style state; peers fall back to team state
// mapped onto the same vocabulary where possible.
func (e *Engine) agentRoster(ctx context.Context, _ tool.AgentRosterRequest) (tool.AgentRosterResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.AgentRosterResult{}, err
	}
	if e == nil || e.team == nil {
		return tool.AgentRosterResult{}, fmt.Errorf("agent_roster is not available (no team)")
	}
	members := e.team.Roster()
	if len(members) == 0 {
		// Dissolved team — treat as N/A rather than empty solo.
		return tool.AgentRosterResult{}, fmt.Errorf("agent_roster is not available (no team)")
	}

	selfID := strings.TrimSpace(e.opts.SessionID)
	leadID := e.team.LeadID()
	out := tool.AgentRosterResult{
		LeadID:  leadID,
		Members: make([]tool.AgentRosterMember, 0, len(members)),
	}

	e.childMu.Lock()
	children := e.children
	history := e.childHistory
	e.childMu.Unlock()

	for _, m := range members {
		entry := tool.AgentRosterMember{
			SessionID:       m.SessionID,
			Name:            m.Name,
			Agent:           m.Persona,
			ParentSessionID: m.ParentSessionID,
			Depth:           m.Depth,
			TerminalSummary: m.Summary,
			IsSelf:          m.SessionID == selfID,
		}
		if m.SessionID == leadID {
			entry.Role = "lead"
		} else {
			entry.Role = "member"
		}
		if !m.StartedAt.IsZero() {
			entry.StartedAt = m.StartedAt.UTC().Format(time.RFC3339)
		}

		// Prefer live task_status vocabulary for children this engine owns.
		if h := children[m.SessionID]; h != nil {
			snap := h.statusSnapshot(false)
			entry.State = snap.State
			if entry.StartedAt == "" && !h.startedAt.IsZero() {
				entry.StartedAt = h.startedAt.UTC().Format(time.RFC3339)
			}
			if entry.Agent == "" {
				entry.Agent = h.agent
			}
		} else if rec := history[m.SessionID]; rec != nil {
			snap := rec.statusSnapshot(false)
			entry.State = snap.State
			if entry.TerminalSummary == "" {
				entry.TerminalSummary = snap.TerminalSummary
			}
			if entry.StartedAt == "" && !rec.startedAt.IsZero() {
				entry.StartedAt = rec.startedAt.UTC().Format(time.RFC3339)
			}
			if entry.Agent == "" {
				entry.Agent = rec.agent
			}
		} else {
			entry.State = teamStateToTaskVocab(m.State)
		}
		out.Members = append(out.Members, entry)
	}
	return out, nil
}

// teamStateToTaskVocab maps coarse TeamMemberState onto task_status vocabulary.
func teamStateToTaskVocab(s protocol.TeamMemberState) string {
	switch s {
	case protocol.TeamMemberRunning:
		// task_status uses starting|working|needs_attention for live work.
		return "working"
	case protocol.TeamMemberCompleted:
		return "completed"
	case protocol.TeamMemberFailed:
		return "failed"
	case protocol.TeamMemberCanceled:
		return "canceled"
	case "":
		return "unknown"
	default:
		return string(s)
	}
}

// emitTeamRoster publishes a protocol.TeamRoster snapshot for UI/session logs.
func (e *Engine) emitTeamRoster() {
	if e == nil || e.team == nil {
		return
	}
	res, err := e.agentRoster(context.Background(), tool.AgentRosterRequest{})
	if err != nil {
		// Dissolved team: empty snapshot keeps lead id for consumers.
		leadID := e.team.LeadID()
		e.emit(protocol.TeamRoster{
			Correlation: protocol.Correlation{SessionID: leadID},
			LeadID:      leadID,
		})
		return
	}
	members := make([]protocol.TeamRosterMember, 0, len(res.Members))
	for _, m := range res.Members {
		members = append(members, protocol.TeamRosterMember{
			SessionID:       m.SessionID,
			Name:            m.Name,
			Agent:           m.Agent,
			State:           m.State,
			ParentSessionID: m.ParentSessionID,
			Depth:           m.Depth,
			StartedAt:       m.StartedAt,
			TerminalSummary: m.TerminalSummary,
			Role:            m.Role,
		})
	}
	e.emit(protocol.TeamRoster{
		Correlation: protocol.Correlation{SessionID: res.LeadID},
		LeadID:      res.LeadID,
		Members:     members,
	})
}
