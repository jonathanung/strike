// Package tool defines the tool contract and the built-in tool set
// (read/glob/grep/edit/write/apply_patch/bash/task/webfetch/todowrite/todoread/
// memory_write/memory_read/issue_write/issue_read/notebook_edit/sleep/skill/question/enter_plan_mode/
// exit_plan_mode/phase_done/toolsearch).
// Used by internal/engine (dispatch), internal/permission (AskRequest, for the
// Context.Ask signature), and cmd/strike (registry construction); internal/tui
// never imports it — tool calls reach the frontend only as
// protocol.ToolCallBegin/Output/End events.
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

// TaskRequest is a child/subagent spawn request.
type TaskRequest struct {
	Prompt string
	Agent  string
}

// TaskResult is the outcome of spawning a child session.
// Status is one of "started", "completed", "failed", or "canceled".
// Non-blocking spawns return "started" with SessionID set; terminal statuses
// are retained for callers that still wait on completion.
type TaskResult struct {
	Output    string
	Status    string
	SessionID string
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

// SessionPR is a pull request linked to the active session (from gh output).
// State is open, merged, or closed when known (empty when unset).
type SessionPR struct {
	URL    string
	Number int
	State  string
}

// Context carries per-call facilities into a tool. Ask blocks until the
// permission is granted; it returns an error if rejected or denied.
// SpawnTask, when non-nil, starts a child session (non-blocking for the parent).
// AskUser, when non-nil, blocks until the user answers a question batch.
// SwitchAgent, when non-nil, queues an agent switch applied when the turn ends.
// EnterPlanPhase starts the built-in plan→implement workflow at plan.
// AdvancePhase runs the active phase exit gate and loads the next phase.
// ReportOutput, when non-nil, streams partial stdout/stderr to the UI while
// Execute is still running (e.g. live bash output).
// Process, when set, receives subprocess lifecycle from RunProcess (engine
// maps these to protocol process.* events for hooks and session logs).
// RecordSessionPR, when non-nil, persists a PR URL/number on the session
// (used when bash captures gh pr create/view output).
type Context struct {
	WorkDir     string
	Ask         func(ctx context.Context, req AskRequest) error
	SpawnTask   func(ctx context.Context, req TaskRequest) (TaskResult, error)
	AskUser     func(ctx context.Context, req QuestionRequest) (QuestionResponse, error)
	SwitchAgent func(name string) error
	// EnterPlanPhase starts the default plan-implement workflow at the plan phase.
	EnterPlanPhase func() error
	// AdvancePhase clears the current phase exit gate and advances (or ends).
	AdvancePhase func(ctx context.Context) error
	// ReportOutput streams retained output chunks (already size-capped by the
	// tool) for live UI. Nil disables streaming; tools must still return the
	// full Result.Output at the end.
	ReportOutput func(data string)
	// Process observes RunProcess lifecycle when non-nil.
	Process ProcessObserver
	// RecordSessionPR stores PR linkage on the session when non-nil.
	RecordSessionPR func(pr SessionPR) error
	// Files optionally tracks read snapshots for stale-edit detection after
	// external changes (FilesChanged / /vim). Nil disables the checks.
	Files *FileState
	// Checkpoint, when non-nil, records pre-mutation file state for undo
	// restore (first touch per turn). Absolute path. Nil disables checkpoints.
	Checkpoint func(absPath string)
	// ChildWake is closed when a background child completes. Sleep selects on
	// it so poll-loops return promptly. Nil disables early wake.
	ChildWake <-chan struct{}
	// HasChildNotice reports a queued child.completed ready for the model.
	// Sleep checks this before waiting so a completion that arrived just
	// before Execute is not missed. Nil means never pending.
	HasChildNotice func() bool
}

// SnapshotPath records the pre-mutation state of absPath when Checkpoint is set.
// Safe on a nil receiver or nil Checkpoint.
func (tc *Context) SnapshotPath(absPath string) {
	if tc == nil || tc.Checkpoint == nil || absPath == "" {
		return
	}
	tc.Checkpoint(absPath)
}

type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage // JSON Schema for the tool's parameters
	Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error)
}
