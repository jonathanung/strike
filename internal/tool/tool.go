// Package tool defines the tool contract and the built-in tool set
// (read/glob/grep/edit/write/bash/task). Used by internal/engine (dispatch),
// internal/permission (AskRequest, for the Context.Ask signature), and
// cmd/strike (registry construction); internal/tui never imports it — tool
// calls reach the frontend only as protocol.ToolCallBegin/End events.
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

// Context carries per-call facilities into a tool. Ask blocks until the
// permission is granted; it returns an error if rejected or denied.
// SpawnTask, when non-nil, runs a blocking foreground child session.
type Context struct {
	WorkDir   string
	Ask       func(ctx context.Context, req AskRequest) error
	SpawnTask func(ctx context.Context, req TaskRequest) (TaskResult, error)
}

type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage // JSON Schema for the tool's parameters
	Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error)
}
