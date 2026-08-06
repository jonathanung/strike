// Package tool defines the tool contract and the built-in tool set
// (read/glob/grep/edit/write/apply_patch/bash/task/task_status/task_read/
// task_message/task_interrupt/agent_roster/agent_message/agent_broadcast/
// team_task/webfetch/todowrite/todoread/
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

	"github.com/jonathanung/strike-cli/internal/sandbox"
	"github.com/jonathanung/strike-cli/internal/scheduler"
)

// Result separates what the model sees (Output) from what the UI renders
// (Title for the one-line summary, Metadata for rich views like diffs).
type Result struct {
	Title    string
	Output   string
	Metadata json.RawMessage
}

// UserRejectedError is returned when the user declines an interactive
// approval that is not a permission ask (e.g. exit_plan_mode "No"). The
// engine settles it as a tool-result error and interrupts the turn.
type UserRejectedError struct {
	Message string
}

func (e *UserRejectedError) Error() string {
	if e != nil && e.Message != "" {
		return e.Message
	}
	return "The user rejected this action."
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
	// Name is an optional stable teammate alias unique within the session team
	// (e.g. "explorer"). Empty leaves the child addressable by session id only.
	Name string
	// Model is an optional model id for the child (bare id on the parent
	// provider, or "provider/model"). Empty inherits the parent's model
	// (subject to agent pins).
	Model string
	// Effort is an optional reasoning-effort level for the child
	// (off|low|medium|high|xhigh|max). Empty inherits the parent's dial
	// (subject to agent effort pins). When set, it wins over agent pins.
	Effort string
}

// TaskResult is the outcome of spawning a child session.
// Status is one of "started", "completed", "failed", or "canceled".
// Non-blocking spawns return "started" with SessionID set; terminal statuses
// are retained for callers that still wait on completion.
type TaskResult struct {
	Output    string
	Status    string
	SessionID string
	// Name is the stable alias assigned at spawn when requested (may be empty).
	Name string
}

// Task control request/result types for parent inspection of owned children.

// TaskStatusRequest queries live or terminal state of one child session.
type TaskStatusRequest struct {
	SessionID     string
	IncludeRecent bool
}

// TaskStatusResult is a model-facing snapshot of a child session.
// State is one of starting|working|needs_attention|completed|failed|canceled|unknown.
// QueuePools/QueueLabel are set while the child is waiting on scheduler admission
// so callers can distinguish queue wait from idle without exact queue positions.
type TaskStatusResult struct {
	State           string
	Elapsed         string
	CurrentTool     string
	LatestActivity  []string
	TerminalSummary string
	SessionID       string
	// QueuePools lists constrained pool names while waiting for admission.
	QueuePools []string `json:"queue_pools,omitempty"`
	// QueueLabel is a short human tag for the waiting work (e.g. "model").
	QueueLabel string `json:"queue_label,omitempty"`
}

// TaskReadRequest loads a bounded transcript slice from a child session.
type TaskReadRequest struct {
	SessionID        string
	Offset           int  // 0-based absolute index into retained events
	Limit            int  // max entries (default 20, max 100); 0 = default
	Last             int  // when > 0, ignore Offset and return the last N entries
	IncludeTools     bool // include tool begin/end rows (default true when unset via pointer elsewhere)
	IncludeReasoning bool
}

// TaskTranscriptEntry is one bounded transcript row for task_read.
type TaskTranscriptEntry struct {
	Index   int    `json:"index"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

// TaskReadResult is a bounded ordered transcript slice.
type TaskReadResult struct {
	SessionID  string                `json:"session_id"`
	Entries    []TaskTranscriptEntry `json:"entries"`
	Offset     int                   `json:"offset"`
	Limit      int                   `json:"limit"`
	Total      int                   `json:"total"`
	Truncated  bool                  `json:"truncated"`
	NextOffset int                   `json:"next_offset"` // == Offset+len(Entries); -1 when at end
}

// TaskMessageRequest steers a running child with additional guidance.
type TaskMessageRequest struct {
	SessionID string
	Text      string
}

// TaskMessageResult acknowledges delivery semantics.
// Status is queued|accepted|rejected.
type TaskMessageResult struct {
	Status    string
	State     string
	SessionID string
	Detail    string
}

// TaskInterruptRequest cancels a running child by session id.
type TaskInterruptRequest struct {
	SessionID string
}

// TaskInterruptResult reports post-interrupt state (idempotent).
type TaskInterruptResult struct {
	State     string
	SessionID string
	Detail    string
}

// AgentRosterRequest lists the implicit session team (no filters today).
type AgentRosterRequest struct{}

// AgentRosterMember is one teammate row for agent_roster.
// State matches task_status vocabulary where possible
// (starting|working|needs_attention|completed|failed|canceled|unknown).
// QueuePools/QueueLabel identify a constrained pool while waiting (not idle).
type AgentRosterMember struct {
	SessionID       string   `json:"session_id"`
	Name            string   `json:"name,omitempty"`
	Agent           string   `json:"agent,omitempty"`
	State           string   `json:"state"`
	Role            string   `json:"role,omitempty"` // lead | member
	ParentSessionID string   `json:"parent_session_id,omitempty"`
	Depth           int      `json:"depth,omitempty"`
	StartedAt       string   `json:"started_at,omitempty"` // RFC3339
	TerminalSummary string   `json:"terminal_summary,omitempty"`
	IsSelf          bool     `json:"is_self"`
	QueuePools      []string `json:"queue_pools,omitempty"`
	QueueLabel      string   `json:"queue_label,omitempty"`
}

// AgentRosterResult is the full team snapshot for agent_roster.
type AgentRosterResult struct {
	LeadID  string              `json:"lead_id"`
	Members []AgentRosterMember `json:"members"`
}

// AgentMessageRequest sends one peer message to a teammate.
// To is a session id (or stable name when aliases are set).
type AgentMessageRequest struct {
	To      string
	Body    string
	Summary string // optional short UI label
}

// AgentMessageResult acknowledges peer delivery (mailbox status vocabulary).
// Status is queued|accepted|rejected.
type AgentMessageResult struct {
	To        string `json:"to"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Dropped   bool   `json:"dropped,omitempty"`
}

// AgentBroadcastRequest sends one body to every other teammate.
type AgentBroadcastRequest struct {
	Body    string
	Summary string
}

// AgentBroadcastDelivery is one recipient outcome from agent_broadcast.
type AgentBroadcastDelivery struct {
	To        string `json:"to"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Dropped   bool   `json:"dropped,omitempty"`
}

// AgentBroadcastResult aggregates per-recipient deliveries.
type AgentBroadcastResult struct {
	Delivered int                      `json:"delivered"` // accepted|queued count
	Rejected  int                      `json:"rejected"`
	Results   []AgentBroadcastDelivery `json:"results"`
}

// TeamTaskRequest mutates or lists the shared team task board.
// Action is create|list|update|claim|complete.
type TeamTaskRequest struct {
	Action          string
	ID              string
	Content         string
	ContentSet      bool // true when JSON included "content" (allows empty reject vs omit)
	Status          string
	ExpectedVersion int // 0 = skip CAS version check
}

// TeamTaskItem is one board row for team_task.
type TeamTaskItem struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	Status    string `json:"status"`
	Owner     string `json:"owner,omitempty"`
	Version   int    `json:"version"`
	CreatedBy string `json:"created_by,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"` // RFC3339
}

// TeamTaskResult is the team_task tool payload.
// On claim/update/complete conflicts, Conflict is true and Task holds the
// current row (Detail explains); list/create still return Tasks when useful.
type TeamTaskResult struct {
	LeadID   string         `json:"lead_id,omitempty"`
	Action   string         `json:"action,omitempty"`
	Task     *TeamTaskItem  `json:"task,omitempty"`
	Tasks    []TeamTaskItem `json:"tasks,omitempty"`
	Conflict bool           `json:"conflict,omitempty"`
	Detail   string         `json:"detail,omitempty"`
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
// TaskStatus/TaskRead/TaskMessage/TaskInterrupt, when non-nil, inspect or
// control owned descendant sessions (never arbitrary sessions).
// AgentRoster, when non-nil, lists the implicit session team (lead + peers).
// AgentMessage/AgentBroadcast, when non-nil, send peer mail on the team.
// TeamTask, when non-nil, mutates the shared lead-scoped team task board.
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
	WorkDir string
	// SandboxMode is the OS process sandbox dial for bash
	// (off|read-only|workspace-write). Empty means workspace-write (default).
	// Wired from config/CLI; see internal/sandbox.ParseMode.
	// Prefer Sandbox when set (permission-compiled policy); Mode zero with
	// empty WorkDir falls back to SandboxMode + WorkDir.
	SandboxMode string
	// Sandbox is the full OS policy for bash (mode, workdir, write denials,
	// network). When non-zero extras are present or WorkDir is set on the
	// policy, bash uses it directly; otherwise SandboxMode is resolved.
	Sandbox sandbox.Policy
	// Scheduler, when non-nil, gates bash via named pools after permission
	// approval and before process start. Shared across roots/children in one
	// Strike process. nil preserves unlimited (no admission wait).
	Scheduler *scheduler.Scheduler
	// SchedulerPolicy classifies bash commands into process/build/test pools.
	// nil treats every command as general (process only). Used only when
	// Scheduler is non-nil.
	SchedulerPolicy *scheduler.Effective
	// SchedulerAcquire, when non-nil, replaces direct Scheduler.Acquire so the
	// engine can emit protocol queue lifecycle events with correlation.
	// Signature matches engine.acquireScheduler (label + pools).
	SchedulerAcquire func(ctx context.Context, label string, pools ...string) (*scheduler.Lease, error)
	Ask              func(ctx context.Context, req AskRequest) error
	SpawnTask        func(ctx context.Context, req TaskRequest) (TaskResult, error)
	TaskStatus       func(ctx context.Context, req TaskStatusRequest) (TaskStatusResult, error)
	TaskRead         func(ctx context.Context, req TaskReadRequest) (TaskReadResult, error)
	TaskMessage      func(ctx context.Context, req TaskMessageRequest) (TaskMessageResult, error)
	// TaskInterrupt cancels an owned running child by session id.
	TaskInterrupt func(ctx context.Context, req TaskInterruptRequest) (TaskInterruptResult, error)
	// AgentRoster lists lead + teammates on the implicit session team.
	AgentRoster func(ctx context.Context, req AgentRosterRequest) (AgentRosterResult, error)
	// AgentMessage sends a peer mailbox message to one teammate.
	AgentMessage func(ctx context.Context, req AgentMessageRequest) (AgentMessageResult, error)
	// AgentBroadcast sends a peer mailbox message to all other teammates.
	AgentBroadcast func(ctx context.Context, req AgentBroadcastRequest) (AgentBroadcastResult, error)
	// TeamTask creates/lists/updates/claims/completes shared team board items.
	TeamTask    func(ctx context.Context, req TeamTaskRequest) (TeamTaskResult, error)
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
