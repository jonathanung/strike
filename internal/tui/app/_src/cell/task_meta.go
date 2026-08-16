package tui

import (
	"encoding/json"
	"strings"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// isChildCompletedNotice reports an engine-injected [child.completed] user
// message (model-visible notice, not a human prompt).
func isChildCompletedNotice(text string) bool {
	return strings.Contains(text, "[child.completed")
}

// taskMetadata is the JSON payload on task tool ToolCallEnd / cell metadata.
type taskMetadata struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
}

// beginSleepToolCell registers a sleep tool call, coalescing consecutive sleep
// ticks into a single transcript row that updates in place.
func beginSleepToolCell(cells []cell, toolByID map[string]*toolCell, callID, name string, args json.RawMessage) []cell {
	tc := &toolCell{callID: callID, name: name, args: args}
	toolByID[callID] = tc
	if prev, ok := lastCell[*toolCell](cells); ok && prev.name == "sleep" {
		prev.callID = callID
		prev.args = args
		prev.done = false
		prev.isError = false
		prev.title = ""
		prev.output = ""
		prev.metadata = nil
		toolByID[callID] = prev
		return cells
	}
	return append(cells, tc)
}

func parseTaskMetadata(raw json.RawMessage) (taskMetadata, bool) {
	if len(raw) == 0 {
		return taskMetadata{}, false
	}
	var m taskMetadata
	if err := json.Unmarshal(raw, &m); err != nil {
		return taskMetadata{}, false
	}
	if strings.TrimSpace(m.SessionID) == "" && strings.TrimSpace(m.Status) == "" {
		return taskMetadata{}, false
	}
	return m, true
}

// taskSpawnPending reports a non-blocking task spawn that is still running
// (ToolCallEnd arrived with status "started" — not a terminal success).
func taskSpawnPending(meta json.RawMessage, isError bool) bool {
	if isError {
		return false
	}
	m, ok := parseTaskMetadata(meta)
	return ok && m.Status == "started"
}

func applyToolCallEnd(tc *toolCell, title, output string, meta json.RawMessage, isError bool, errorCode ...string) {
	if tc == nil {
		return
	}
	tc.title = title
	tc.output = output
	tc.metadata = meta
	tc.isError = isError
	if len(errorCode) > 0 {
		tc.errorCode = errorCode[0]
	} else {
		tc.errorCode = ""
	}
	// Async task: keep the cell in-progress until ChildCompleted.
	tc.done = !taskSpawnPending(meta, isError)
}

func taskMetadataJSON(sessionID, status string) json.RawMessage {
	b, err := json.Marshal(taskMetadata{SessionID: sessionID, Status: status})
	if err != nil {
		return nil
	}
	return b
}

// applyChildCompletedToTaskCells updates parent transcript task rows when a
// child finishes so the tool cell shows terminal status instead of spawn-only.
func applyChildCompletedToTaskCells(toolByID map[string]*toolCell, ev protocol.ChildCompleted) {
	if toolByID == nil {
		return
	}
	id := strings.TrimSpace(ev.SessionID)
	if id == "" {
		return
	}
	status := string(ev.Status)
	if status == "" {
		status = string(protocol.ChildStatusCompleted)
	}
	summary := strings.TrimSpace(ev.Summary)
	for _, tc := range toolByID {
		if tc == nil || tc.name != "task" {
			continue
		}
		meta, ok := parseTaskMetadata(tc.metadata)
		if !ok || meta.SessionID != id {
			continue
		}
		tc.done = true
		tc.isError = status == string(protocol.ChildStatusFailed) ||
			status == string(protocol.ChildStatusCanceled)
		tc.metadata = taskMetadataJSON(id, status)
		// One-line tool status only; full multi-line result lives on the
		// dedicated subagentResultCell so the transcript stays collapsed.
		if summary != "" {
			tc.output = firstLine(summary)
		} else if tc.output == "" {
			tc.output = "task " + status
		}
	}
}
