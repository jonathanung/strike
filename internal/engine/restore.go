package engine

import (
	"strings"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
)

// Restored is model-usable runtime state reduced from a session event log.
// It is the durable counterpart of live engine fields needed for --continue.
type Restored struct {
	Messages []provider.Message
	Provider string
	Model    string
	Agent    string
	Effort   protocol.Effort
	Priority bool
	Titled   bool
	// Autonomy is the last AutonomySelected mode; empty means unset (caller
	// keeps the engine default of supervised).
	Autonomy protocol.Autonomy
	// Phase* capture the last PhaseChanged with a non-empty Phase. Empty
	// PhaseName means no workflow phase was active at end of log.
	PhaseWorkflow string
	PhaseName     string
	PhaseIndex    int
	// AlwaysGrants are session DecisionAlways rules still in force after the
	// last agent switch (SetAgentRules clears grants).
	AlwaysGrants permission.Ruleset
}

// reqAccum collects one provider-request's stream into an assistant message
// plus tool results (mirrors runTurn post-stream history writes).
type reqAccum struct {
	id      string
	text    string
	calls   []provider.ToolCall
	results map[string]toolEnd
}

type toolEnd struct {
	output  string
	isError bool
}

// pendingAsk remembers PermissionAsked fields until PermissionResolved.
type pendingAsk struct {
	permission string
	patterns   []string
	always     []string
}

// Restore reduces root-session protocol events into engine history and
// selections. Child-lineage events (ParentSessionID or Depth > 0) are skipped.
// CompactionCompleted reapplies the recorded remove/keep boundary. Incomplete
// tool batches receive synthetic error results so the next model request stays
// structurally valid and does not re-run completed side effects.
func Restore(events []protocol.Event) Restored {
	var r Restored
	var msgs []provider.Message
	var cur *reqAccum
	asks := map[string]pendingAsk{}
	var grants permission.Ruleset

	flush := func() {
		if cur == nil {
			return
		}
		if cur.text != "" || len(cur.calls) > 0 {
			msgs = append(msgs, provider.Message{
				Role:      provider.RoleAssistant,
				Text:      cur.text,
				ToolCalls: cur.calls,
			})
			for _, c := range cur.calls {
				end, ok := cur.results[c.ID]
				if !ok {
					end = toolEnd{output: unstartedToolOutput, isError: true}
				}
				msgs = append(msgs, provider.Message{
					Role:       provider.RoleTool,
					ToolResult: &provider.ToolResult{CallID: c.ID, Output: end.output, IsError: end.isError},
				})
			}
		}
		cur = nil
	}

	ensure := func(reqID string) {
		if cur != nil && cur.id == reqID {
			return
		}
		// Empty ids share one anonymous bucket until a real id or flush boundary.
		if cur != nil && cur.id == "" && reqID == "" {
			return
		}
		flush()
		cur = &reqAccum{id: reqID, results: make(map[string]toolEnd)}
	}

	for _, ev := range events {
		if !restoreRootEvent(ev) {
			continue
		}
		switch e := ev.(type) {
		case protocol.UserMessage:
			flush()
			msgs = append(msgs, provider.Message{
				Role:   provider.RoleUser,
				Text:   e.Text,
				Images: protocolImagesToProvider(e.Images),
			})
		case protocol.TextDelta:
			ensure(e.ProviderRequestID)
			cur.text += e.Text
		case protocol.ToolCallBegin:
			ensure(e.ProviderRequestID)
			cur.calls = append(cur.calls, provider.ToolCall{
				ID:   e.CallID,
				Name: e.Name,
				Args: e.Args,
			})
		case protocol.ToolCallEnd:
			ensure(e.ProviderRequestID)
			cur.results[e.CallID] = toolEnd{output: e.Output, isError: e.IsError}
		case protocol.TurnCompleted, protocol.EngineError:
			flush()
		case protocol.CompactionCompleted:
			flush()
			msgs = applyRecordedCompaction(msgs, e.Removed, e.Kept, e.Summary)
		case protocol.SessionRewound:
			flush()
			msgs, _ = dropLastUserTurn(msgs)
		case protocol.ModelSelected:
			r.Provider = e.Provider
			r.Model = e.Model
		case protocol.AgentSelected:
			r.Agent = e.Name
			// Matches permission.Service.SetAgentRules: agent switch clears
			// session always-grants so prior role grants cannot widen.
			grants = nil
		case protocol.EffortSelected:
			r.Effort = e.Level
		case protocol.FastSelected:
			r.Priority = e.Enabled
		case protocol.AutonomySelected:
			r.Autonomy = e.Mode.Normalize()
		case protocol.PhaseChanged:
			r.PhaseWorkflow = e.Workflow
			r.PhaseName = e.Phase
			r.PhaseIndex = e.Index
			if e.Phase == "" {
				r.PhaseWorkflow = ""
				r.PhaseIndex = 0
			}
		case protocol.SessionTitled:
			if e.Title != "" {
				r.Titled = true
			}
		case protocol.PermissionAsked:
			asks[e.RequestID] = pendingAsk{
				permission: e.Permission,
				patterns:   append([]string(nil), e.Patterns...),
				always:     append([]string(nil), e.Always...),
			}
		case protocol.PermissionResolved:
			ask, ok := asks[e.RequestID]
			delete(asks, e.RequestID)
			if !ok || e.Decision != protocol.DecisionAlways {
				continue
			}
			patterns := ask.always
			if len(patterns) == 0 {
				patterns = ask.patterns
			}
			if len(patterns) == 0 {
				patterns = []string{"*"}
			}
			for _, pat := range patterns {
				grants = append(grants, permission.Rule{
					Permission: ask.permission,
					Pattern:    pat,
					Action:     permission.Allow,
				})
			}
		}
	}
	flush()
	r.Messages = msgs
	r.AlwaysGrants = grants
	return r
}

// restoreRootEvent reports whether ev belongs to the root conversation
// (not a child/subagent lineage). Events without correlation are treated as root.
func restoreRootEvent(ev protocol.Event) bool {
	corr, ok := restoreCorrelation(ev)
	if !ok {
		return true
	}
	return corr.ParentSessionID == "" && corr.Depth == 0
}

func restoreCorrelation(ev protocol.Event) (protocol.Correlation, bool) {
	switch e := ev.(type) {
	case protocol.UserMessage:
		return e.Correlation, true
	case protocol.TurnStarted:
		return e.Correlation, true
	case protocol.TurnCompleted:
		return e.Correlation, true
	case protocol.TextDelta:
		return e.Correlation, true
	case protocol.ToolCallBegin:
		return e.Correlation, true
	case protocol.ToolCallEnd:
		return e.Correlation, true
	case protocol.ToolCallOutput:
		return e.Correlation, true
	case protocol.EngineError:
		return e.Correlation, true
	case protocol.ModelSelected:
		return e.Correlation, true
	case protocol.AgentSelected:
		return e.Correlation, true
	case protocol.EffortSelected:
		return e.Correlation, true
	case protocol.FastSelected:
		return e.Correlation, true
	case protocol.AutonomySelected:
		return e.Correlation, true
	case protocol.PhaseChanged:
		return e.Correlation, true
	case protocol.SessionTitled:
		return e.Correlation, true
	case protocol.CompactionStarted:
		return e.Correlation, true
	case protocol.CompactionCompleted:
		return e.Correlation, true
	case protocol.ChildStarted:
		return e.Correlation, true
	case protocol.ChildCompleted:
		return e.Correlation, true
	case protocol.UsageReported:
		return e.Correlation, true
	case protocol.PermissionAsked:
		return e.Correlation, true
	case protocol.PermissionResolved:
		return e.Correlation, true
	case protocol.QuestionAsked:
		return e.Correlation, true
	case protocol.QuestionResolved:
		return e.Correlation, true
	case protocol.FilesInvalidated:
		return e.Correlation, true
	case protocol.SessionMeta:
		return e.Correlation, true
	case protocol.SessionRewound:
		return e.Correlation, true
	default:
		return protocol.Correlation{}, false
	}
}

// applyRecordedCompaction drops the prefix Removed messages and prepends the
// compact marker (or model-authored summary when recorded), matching
// applyCompaction's on-disk record.
func applyRecordedCompaction(msgs []provider.Message, removed, kept int, summary string) []provider.Message {
	if removed <= 0 || kept < 1 {
		return msgs
	}
	tailLen := kept - 1
	if tailLen < 0 {
		tailLen = 0
	}
	var tail []provider.Message
	switch {
	case removed <= len(msgs) && len(msgs)-removed == tailLen:
		tail = msgs[removed:]
	case len(msgs) >= tailLen:
		// Reconstruction drift: keep the trailing tailLen messages.
		tail = msgs[len(msgs)-tailLen:]
	default:
		return msgs
	}
	marker := compactMarker(removed)
	if s := strings.TrimSpace(summary); s != "" {
		marker = summaryCompactMarker(removed, s)
	}
	out := make([]provider.Message, 0, 1+len(tail))
	out = append(out, provider.Message{Role: provider.RoleUser, Text: marker})
	out = append(out, tail...)
	return out
}
