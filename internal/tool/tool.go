// Package tool defines the tool contract and the built-in tool set
// (read/glob/grep/edit/write/apply_patch/bash/task/webfetch/todowrite/todoread/
// memory_write/memory_read/notebook_edit/sleep/skill/question/enter_plan_mode/
// exit_plan_mode/toolsearch).
// Used by internal/engine (dispatch), internal/permission (AskRequest, for the
// Context.Ask signature), and cmd/strike (registry construction); internal/tui
// never imports it — tool calls reach the frontend only as
// protocol.ToolCallBegin/End events.
package tool

import (
	"context"
	"encoding/json"
)

// Result separates what the model sees (Output) from what the UI renders
// (Title for the one-line summary, Metadata for rich views like diffs).
type Result struct {
	Title    string
	Output   string
	Metadata json.RawMessage
}

// AskRequest is a permission ask raised by a tool mid-execution.
type AskRequest struct {
	Permission string   // e.g. "edit", "bash"
	Patterns   []string // what specifically, e.g. a relative path or command
	// Always holds the patterns an "always" grant should persist for the
	// session (often broader than Patterns, e.g. "git *" for "git status").
	Always   []string
	Metadata json.RawMessage
}

// TaskRequest is a foreground child/subagent spawn request.
type TaskRequest struct {
	Prompt string
	Agent  string
}

// TaskResult is the terminal outcome of a foreground child session.
// Status is one of "completed", "failed", or "canceled".
type TaskResult struct {
	Output string
	Status string
}

// QuestionOption is one selectable choice on a QuestionItem.
type QuestionOption struct {
	Label       string
	Description string
}

// QuestionItem is one prompt in a QuestionRequest batch.
type QuestionItem struct {
	ID       string
	Header   string
	Question string
	Options  []QuestionOption
}

// QuestionRequest is a user-question batch raised by the question tool.
type QuestionRequest struct {
	Questions []QuestionItem
}

// QuestionResponse carries one answer string per question (same order).
type QuestionResponse struct {
	Answers []string
}

// Context carries per-call facilities into a tool. Ask blocks until the
// permission is granted; it returns an error if rejected or denied.
// SpawnTask, when non-nil, runs a blocking foreground child session.
// AskUser, when non-nil, blocks until the user answers a question batch.
// SwitchAgent, when non-nil, queues an agent switch applied when the turn ends.
type Context struct {
	WorkDir     string
	Ask         func(ctx context.Context, req AskRequest) error
	SpawnTask   func(ctx context.Context, req TaskRequest) (TaskResult, error)
	AskUser     func(ctx context.Context, req QuestionRequest) (QuestionResponse, error)
	SwitchAgent func(name string) error
	// Files optionally tracks read snapshots for stale-edit detection after
	// external changes (FilesChanged / /vim). Nil disables the checks.
	Files *FileState
}

type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage // JSON Schema for the tool's parameters
	Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error)
}
