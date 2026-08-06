package acp

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// promptText flattens ACP content blocks into a single user-input string.
// Baseline: text + resource_link. Embedded resource text is inlined when present.
func promptText(blocks []ContentBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		switch blk.Type {
		case "text":
			if blk.Text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(blk.Text)
		case "resource_link":
			label := blk.Name
			if label == "" {
				label = blk.URI
			}
			if label == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			if blk.URI != "" && blk.Name != "" && blk.URI != blk.Name {
				fmt.Fprintf(&b, "[%s](%s)", blk.Name, blk.URI)
			} else {
				b.WriteString(label)
			}
		case "resource":
			if blk.Resource == nil {
				continue
			}
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			if blk.Resource.Text != "" {
				if blk.Resource.URI != "" {
					fmt.Fprintf(&b, "```%s\n%s\n```", blk.Resource.URI, blk.Resource.Text)
				} else {
					b.WriteString(blk.Resource.Text)
				}
			} else if blk.Resource.URI != "" {
				b.WriteString(blk.Resource.URI)
			}
		case "image":
			// Image capability not advertised; mention presence so the model
			// knows context was dropped rather than silently ignoring it.
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString("[image attachment omitted: agent does not advertise image prompt capability]")
		case "audio":
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString("[audio attachment omitted: agent does not advertise audio prompt capability]")
		}
	}
	return b.String()
}

// mapStopReason converts a strike TurnCompleted stop reason to an ACP StopReason.
func mapStopReason(strike string) string {
	switch strings.ToLower(strings.TrimSpace(strike)) {
	case "", "end_turn", "stop", "complete", "completed":
		return StopEndTurn
	case "interrupted", "canceled", "cancelled", "cancel":
		return StopCancelled
	case "max_tokens":
		return StopMaxTokens
	case "max_turn_requests":
		return StopMaxTurnRequests
	case "refusal", "refused":
		return StopRefusal
	case "error":
		// Engine errors are already streamed; end the turn cleanly for the client.
		return StopEndTurn
	default:
		return StopEndTurn
	}
}

// toolKind maps a strike tool name onto an ACP ToolKind.
func toolKind(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case n == "read" || n == "notebook_read" || strings.HasPrefix(n, "read_"):
		return "read"
	case n == "edit" || n == "write" || n == "apply_patch" || n == "notebook_edit":
		return "edit"
	case n == "bash" || n == "shell" || n == "execute":
		return "execute"
	case n == "glob" || n == "grep" || n == "search" || n == "toolsearch":
		return "search"
	case n == "webfetch" || n == "fetch":
		return "fetch"
	case n == "todowrite" || n == "todoread" || n == "enter_plan_mode" || n == "exit_plan_mode":
		return "think"
	default:
		return "other"
	}
}

// toolTitle builds a short human-readable tool call title.
func toolTitle(name string, args json.RawMessage) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "tool"
	}
	if len(args) == 0 {
		return name
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return name
	}
	for _, key := range []string{"path", "file", "file_path", "filepath", "command", "pattern", "query", "url"} {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				if key == "path" || key == "file" || key == "file_path" || key == "filepath" {
					s = filepath.Base(s)
				}
				if len(s) > 60 {
					s = s[:57] + "..."
				}
				return name + " " + s
			}
		}
	}
	return name
}

// toolLocations extracts follow-along file paths from tool args when present.
func toolLocations(args json.RawMessage) []map[string]any {
	if len(args) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return nil
	}
	for _, key := range []string{"path", "file", "file_path", "filepath"} {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return []map[string]any{{"path": s}}
			}
		}
	}
	return nil
}

// eventUpdates maps a strike protocol event to zero or more ACP session/update
// payloads (the "update" object only — caller wraps with sessionId).
// PermissionAsked is handled separately (agent → client request).
func eventUpdates(ev protocol.Event) []map[string]any {
	switch e := ev.(type) {
	case protocol.TextDelta:
		if e.Text == "" {
			return nil
		}
		return []map[string]any{contentChunk("agent_message_chunk", e.Text)}
	case protocol.ReasoningDelta:
		if e.Text == "" {
			return nil
		}
		return []map[string]any{contentChunk("agent_thought_chunk", e.Text)}
	case protocol.UserMessage:
		if e.Text == "" {
			return nil
		}
		return []map[string]any{contentChunk("user_message_chunk", e.Text)}
	case protocol.ToolCallBegin:
		id := e.CallID
		if id == "" {
			return nil
		}
		u := map[string]any{
			"sessionUpdate": "tool_call",
			"toolCallId":    id,
			"title":         toolTitle(e.Name, e.Args),
			"kind":          toolKind(e.Name),
			"status":        "pending",
		}
		if len(e.Args) > 0 {
			var raw any
			if err := json.Unmarshal(e.Args, &raw); err == nil {
				u["rawInput"] = raw
			}
		}
		if locs := toolLocations(e.Args); len(locs) > 0 {
			u["locations"] = locs
		}
		return []map[string]any{u}
	case protocol.ToolCallOutput:
		if e.CallID == "" || e.Data == "" {
			return nil
		}
		return []map[string]any{{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    e.CallID,
			"status":        "in_progress",
			"content": []map[string]any{
				{
					"type": "content",
					"content": map[string]any{
						"type": "text",
						"text": e.Data,
					},
				},
			},
		}}
	case protocol.ToolCallEnd:
		if e.CallID == "" {
			return nil
		}
		status := "completed"
		if e.IsError {
			status = "failed"
		}
		u := map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    e.CallID,
			"status":        status,
		}
		if e.Title != "" {
			u["title"] = e.Title
		}
		if e.Output != "" {
			u["content"] = []map[string]any{
				{
					"type": "content",
					"content": map[string]any{
						"type": "text",
						"text": e.Output,
					},
				},
			}
			u["rawOutput"] = map[string]any{"output": e.Output, "isError": e.IsError}
		}
		return []map[string]any{u}
	case protocol.UsageReported:
		// Optional usage_update when we have a known context occupancy.
		if !e.Used.Known {
			return nil
		}
		u := map[string]any{
			"sessionUpdate": "usage_update",
			"used":          e.Used.N,
			"size":          e.Used.N, // full window size not always on this event
		}
		return []map[string]any{u}
	case protocol.EngineError:
		if strings.TrimSpace(e.Message) == "" {
			return nil
		}
		return []map[string]any{contentChunk("agent_message_chunk", "error: "+e.Message)}
	default:
		return nil
	}
}

func contentChunk(kind, text string) map[string]any {
	return map[string]any{
		"sessionUpdate": kind,
		"content": map[string]any{
			"type": "text",
			"text": text,
		},
	}
}

// defaultPermissionOptions are the choices offered on session/request_permission.
func defaultPermissionOptions() []PermissionOpt {
	return []PermissionOpt{
		{OptionID: "allow-once", Name: "Allow once", Kind: "allow_once"},
		{OptionID: "allow-always", Name: "Allow always for session", Kind: "allow_always"},
		{OptionID: "reject-once", Name: "Reject", Kind: "reject_once"},
	}
}

// decisionFromOption maps an ACP permission optionId to a strike Decision.
func decisionFromOption(optionID string) protocol.Decision {
	switch strings.ToLower(strings.TrimSpace(optionID)) {
	case "allow-once", "allow_once", "once":
		return protocol.DecisionOnce
	case "allow-always", "allow_always", "always":
		return protocol.DecisionAlways
	case "reject-once", "reject_once", "reject", "reject-always", "reject_always":
		return protocol.DecisionReject
	default:
		return protocol.DecisionReject
	}
}
