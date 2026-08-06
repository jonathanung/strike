// Package tool defines the tool contract and the built-in tool set
// (read/glob/grep/edit/write/apply_patch/bash/task/task_status/task_read/
// task_message/task_interrupt/delegate/agent_roster/agent_ownership/agent_message/agent_broadcast/
// task_message/task_interrupt/wait/agent_roster/agent_ownership/agent_message/agent_broadcast/
// team_task/webfetch/todowrite/todoread/
// memory_write/memory_read/issue_write/issue_read/plan_write/plan_read/plan_delegate/
// artifact_write/artifact_read/notebook_edit/sleep/skill/question/enter_plan_mode/
// exit_plan_mode/phase_done/toolsearch/definition/references/symbols).
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
//
// ErrorCode, when non-empty, is a stable failure class (canceled, timeout, …)
// matching protocol.ErrorCode*. The engine settles IsError=true and stamps
// ToolCallEnd.ErrorCode. Empty means success unless Execute returns a non-nil
// error.
type Result struct {
	Title     string
	Output    string
	Metadata  json.RawMessage
	ErrorCode string
}

// Stable tool result error codes (keep in lockstep with protocol.ErrorCode*).
const (
	ErrorCodeCanceled      = "canceled"
	ErrorCodeTimeout       = "timeout"
	ErrorCodeSandboxDenied = "sandbox_denied"
)

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

// VerifyGate is one independent completion condition declared at task spawn.
// Kind is cmd|schema|path (see internal/verify). The implementer model cannot
// self-certify past configured gates.
type VerifyGate struct {
	Kind        string `json:"kind"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
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
	// Route enables capability-aware routing when "auto". Empty/off keeps
	// legacy pin-or-inherit behavior (#778). Specialty/capabilities without
	// an explicit route also enable auto.
	Route string
	// Specialty is a single required capability tag for route=auto
	// (e.g. explore, test, review, debug).
	Specialty string
	// Capabilities are required capability tags for route=auto (all must match).
	// Merged with Specialty when both are set.
	Capabilities []string
	// MaxCostClass optionally filters auto-route candidates: low|medium|high.
	MaxCostClass string
	// Models is an optional model allow-list for auto-route (bare id or provider/model).
	Models []string
	// MaxConcurrent is the per-persona live-child limit before fallback when
	// route=auto. Zero uses the engine default (1).
	MaxConcurrent int
	// Criteria are optional acceptance criteria recorded on the delegation
	// lifecycle object. When non-empty, successful child completion enters
	// lifecycle state "review" instead of "done" (verification gates / #780).
	Criteria []string
	// Deps lists upstream delegation ids (or linked session ids) that must
	// reach lifecycle "done" before this task spawns. Unmet deps leave the
	// delegation queued without starting a child.
	Deps []string
	// Subscribe lists lifecycle states that should notify the owner/lead
	// (blocked|review|done|failed|canceled|working|queued).
	Subscribe []string
	// Assignee is an optional display label for the intended worker.
	Assignee string
	// Verify declares independent completion gates. When non-empty, implementer
	// completion alone does not yield final completed status — the harness runs
	// these gates and only promotes to completed on pass (else blocked).
	Verify []VerifyGate
	// Budget is an optional per-child limit set at spawn. Zero fields inherit
	// session defaults (engine Options.DefaultChildBudget / config session.agentBudget).
	// Hard exceed interrupts the child and notifies the owner (#774).
	Budget AgentBudgetLimits
	// PlanID/SectionID correlate this child to a plan section refinement
	// (plan_delegate). Empty for ordinary task spawns. On terminal status the
	// engine applies structured handoff fields to that section only.
	PlanID    string
	SectionID string
	// ContextBundle is an optional sealed context package (goal, paths,
	// artifact refs, constraints, file pins). Attached at spawn for the child
	// to read via context_bundle; included on child.started for snapshots.
	ContextBundle ContextBundle
}

// AgentBudgetLimits are optional per-child resource bounds.
// Zero means unlimited / inherit (soft stall/loop signals still apply).
// Session maxSessionCostUSD (#577), when present, remains the outer envelope;
// per-agent MaxCostUSD nests inside it and is only enforced once cost pricing
// is wired.
type AgentBudgetLimits struct {
	MaxWallClockS     int     `json:"max_wall_clock_s,omitempty"`
	MaxTokens         int     `json:"max_tokens,omitempty"`
	MaxCostUSD        float64 `json:"max_cost_usd,omitempty"`
	MaxToolCalls      int     `json:"max_tool_calls,omitempty"`
	MaxDangerousTools int     `json:"max_dangerous_tools,omitempty"`
	// StallAfterS hard-escalates after this many seconds without progress.
	// Zero keeps soft stale signaling only (default 300s observability).
	StallAfterS int `json:"stall_after_s,omitempty"`
	// LoopDetectN hard-escalates when the same tool name repeats N times.
	// Zero keeps soft loop signaling only (default 6).
	LoopDetectN int `json:"loop_detect_n,omitempty"`
}

// AgentBudgetSnapshot is the live budget + signal view for task_status / roster.
type AgentBudgetSnapshot struct {
	Limits              AgentBudgetLimits `json:"limits,omitempty"`
	ElapsedS            int               `json:"elapsed_s"`
	TokensUsed          int               `json:"tokens_used"`
	CostUSDUsed         float64           `json:"cost_usd_used,omitempty"`
	ToolCalls           int               `json:"tool_calls"`
	DangerousTools      int               `json:"dangerous_tools"`
	WallClockRemainingS *int              `json:"wall_clock_remaining_s,omitempty"`
	TokensRemaining     *int              `json:"tokens_remaining,omitempty"`
	ToolCallsRemaining  *int              `json:"tool_calls_remaining,omitempty"`
	DangerousRemaining  *int              `json:"dangerous_remaining,omitempty"`
	CostUSDRemaining    *float64          `json:"cost_usd_remaining,omitempty"`
	// Stall is true when no progress for the stall threshold (soft or hard).
	// Folds stale-child detection (#517) into the same signal.
	Stall bool `json:"stall,omitempty"`
	// Loop is true when the recent tool pattern looks stuck.
	Loop           bool   `json:"loop,omitempty"`
	Escalated      bool   `json:"escalated,omitempty"`
	EscalateKind   string `json:"escalate_kind,omitempty"`
	EscalateReason string `json:"escalate_reason,omitempty"`
}

// TaskResult is the outcome of spawning a child session.
// Status is one of "started", "queued", "completed", "failed", or "canceled".
// Non-blocking spawns return "started" with SessionID set; "queued" means the
// delegation was created but is waiting on dependencies (no child yet).
// Terminal statuses are retained for callers that still wait on completion.
type TaskResult struct {
	Output    string
	Status    string
	SessionID string
	// Name is the stable alias assigned at spawn when requested (may be empty).
	Name string
	// DelegationID is the lifecycle object id (d1, d2, …) when tracked.
	DelegationID string
	// Lifecycle is the delegation state (queued|working|blocked|review|done|…).
	Lifecycle string
	// RouteReason is the structured capability-routing decision when routing ran (#778).
	RouteReason string
}

// Task control request/result types for parent inspection of owned children.

// TaskStatusRequest queries live or terminal state of one child session.
type TaskStatusRequest struct {
	SessionID     string
	IncludeRecent bool
}

// CompletionHandoff is the structured child completion payload exposed on
// task_status (mirrors protocol.CompletionHandoff; snake_case on the wire).
type CompletionHandoff struct {
	Summary               string                `json:"summary"`
	FilesChanged          []string              `json:"files_changed"`
	Verification          string                `json:"verification,omitempty"`
	Findings              []string              `json:"findings"`
	Blockers              []string              `json:"blockers"`
	RecommendedNextAction string                `json:"recommended_next_action,omitempty"`
	MissingContext        []MissingContextEntry `json:"missing_context,omitempty"`
	Provenance            []string              `json:"provenance,omitempty"`
	Incomplete            bool                  `json:"incomplete,omitempty"`
	// Quality is complete|partial|unavailable when set (#879).
	Quality string `json:"quality,omitempty"`
}

// VerificationReport is the harness gate report on task_status (snake_case).
// Distinct from CompletionHandoff.Verification (model self-report string).
type VerificationReport struct {
	Passed     bool                `json:"passed"`
	Claimed    bool                `json:"claimed"`
	Verified   bool                `json:"verified"`
	Checks     []VerificationCheck `json:"checks"`
	Env        VerificationEnv     `json:"env"`
	Summary    string              `json:"summary,omitempty"`
	DurationMs int64               `json:"duration_ms,omitempty"`
}

// VerificationCheck is one gate outcome (snake_case for tools).
type VerificationCheck struct {
	Name       string `json:"name,omitempty"`
	Kind       string `json:"kind"`
	Value      string `json:"value,omitempty"`
	Passed     bool   `json:"passed"`
	ExitCode   int    `json:"exit_code,omitempty"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// VerificationEnv is audit metadata for a verification run.
type VerificationEnv struct {
	WorkDir    string `json:"work_dir,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	WorktreeID string `json:"worktree_id,omitempty"`
	ModelID    string `json:"model_id,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// TaskStatusResult is a model-facing snapshot of a child session.
// State is one of starting|working|needs_attention|completed|failed|canceled|blocked|unknown
// (or "queued" when a delegation exists but no child has started).
// QueuePools/QueueLabel are set while the child is waiting on scheduler admission
// so callers can distinguish queue wait from idle without exact queue positions.
type TaskStatusResult struct {
	State           string
	Elapsed         string
	CurrentTool     string
	LatestActivity  []string
	TerminalSummary string
	SessionID       string
	// Handoff is the structured completion payload when the child is terminal.
	// HasHandoff distinguishes "not terminal yet" from an empty handoff object.
	Handoff    CompletionHandoff
	HasHandoff bool
	// Verification is the harness gate report when gates ran at completion.
	// HasVerification distinguishes "no gates" from an empty report object.
	Verification    VerificationReport
	HasVerification bool
	// QueuePools lists constrained pool names while waiting for admission.
	QueuePools []string `json:"queue_pools,omitempty"`
	// QueueLabel is a short human tag for the waiting work (e.g. "model").
	QueueLabel string `json:"queue_label,omitempty"`
	// DelegationID / Lifecycle / Criteria expose the orchestration object when
	// this child (or queued ref) is tracked as a first-class delegation.
	DelegationID string   `json:"delegation_id,omitempty"`
	Lifecycle    string   `json:"lifecycle,omitempty"`
	Criteria     []string `json:"criteria,omitempty"`
	Deps         []string `json:"deps,omitempty"`
	Version      int      `json:"version,omitempty"`
	BlockReason  string   `json:"block_reason,omitempty"`
	// Observability (#774): objective, last action, files, budget remaining.
	Objective    string              `json:"objective,omitempty"`
	LastAction   string              `json:"last_action,omitempty"`
	FilesTouched []string            `json:"files_touched,omitempty"`
	Budget       AgentBudgetSnapshot `json:"budget,omitempty"`
	HasBudget    bool                `json:"-"` // omit empty budget object when unset
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

// WaitRequest blocks until an owned-child orchestration event matches.
// Events are canonical kinds: task.done, task.failed, task.canceled, task.blocked
// (aliases accepted by the tool). SessionID filters to one child (id or name);
// empty waits on any owned child (wait-any). TimeoutSeconds is required.
type WaitRequest struct {
	Events         []string
	SessionID      string
	TimeoutSeconds float64
}

// WaitResult is the structured outcome of a wait.
// Outcome is matched|timeout|canceled.
type WaitResult struct {
	Outcome        string            `json:"outcome"`
	Event          string            `json:"event,omitempty"`
	SessionID      string            `json:"session_id,omitempty"`
	Name           string            `json:"name,omitempty"`
	Status         string            `json:"status,omitempty"`
	Summary        string            `json:"summary,omitempty"`
	Handoff        CompletionHandoff `json:"handoff,omitempty"`
	HasHandoff     bool              `json:"has_handoff,omitempty"`
	WaitID         string            `json:"wait_id,omitempty"`
	TimeoutSeconds float64           `json:"timeout_seconds,omitempty"`
	Detail         string            `json:"detail,omitempty"`
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
	// Live observability (#774).
	Objective    string              `json:"objective,omitempty"`
	LastAction   string              `json:"last_action,omitempty"`
	BlockReason  string              `json:"block_reason,omitempty"`
	FilesTouched []string            `json:"files_touched,omitempty"`
	Budget       AgentBudgetSnapshot `json:"budget,omitempty"`
	HasBudget    bool                `json:"-"`
}

// AgentRosterResult is the full team snapshot for agent_roster.
type AgentRosterResult struct {
	LeadID  string              `json:"lead_id"`
	Members []AgentRosterMember `json:"members"`
}

// AgentMessageRequest sends one peer message to a teammate.
// To is a session id (or stable name when aliases are set).
// Optional coordination-contract fields bind threads, urgency, and ack TTL.
type AgentMessageRequest struct {
	To                string
	Body              string
	Summary           string  // optional short UI label
	TaskID            string  // team_task or delegation id (thread key)
	Urgency           string  // normal|high|blocker
	Kind              string  // message|request|ack
	RequireAck        bool    // request explicit ack (implied by kind=request)
	AckTimeoutSeconds float64 // TTL when require_ack (default 60, max 300)
	InReplyTo         string  // required for kind=ack
	EscalateTo        string  // ack-timeout target (default lead)
}

// AgentMessageResult acknowledges peer delivery (mailbox status vocabulary).
// Status is queued|accepted|rejected.
type AgentMessageResult struct {
	To                string  `json:"to"`
	Status            string  `json:"status"`
	Detail            string  `json:"detail,omitempty"`
	MessageID         string  `json:"message_id,omitempty"`
	Dropped           bool    `json:"dropped,omitempty"`
	TaskID            string  `json:"task_id,omitempty"`
	Urgency           string  `json:"urgency,omitempty"`
	Kind              string  `json:"kind,omitempty"`
	RequireAck        bool    `json:"require_ack,omitempty"`
	AckStatus         string  `json:"ack_status,omitempty"` // pending|acked|timed_out
	AckTimeoutSeconds float64 `json:"ack_timeout_seconds,omitempty"`
	InReplyTo         string  `json:"in_reply_to,omitempty"`
	EscalateTo        string  `json:"escalate_to,omitempty"`
}

// AgentBroadcastRequest sends one body to every other teammate.
type AgentBroadcastRequest struct {
	Body    string
	Summary string
	TaskID  string // optional thread binding on each copy
	Urgency string // normal|high|blocker
}

// AgentThreadRequest reads a task/delegation-bound message thread.
type AgentThreadRequest struct {
	TaskID string
	Limit  int // default 20, max 64
}

// AgentThreadMessage is one entry in an agent_thread listing.
type AgentThreadMessage struct {
	MessageID  string `json:"message_id"`
	From       string `json:"from"`
	To         string `json:"to"`
	Body       string `json:"body"`
	Summary    string `json:"summary,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	Urgency    string `json:"urgency,omitempty"`
	Kind       string `json:"kind,omitempty"`
	RequireAck bool   `json:"require_ack,omitempty"`
	InReplyTo  string `json:"in_reply_to,omitempty"`
	EscalateTo string `json:"escalate_to,omitempty"`
	AckStatus  string `json:"ack_status,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"` // RFC3339
}

// AgentThreadResult is the agent_thread tool payload.
type AgentThreadResult struct {
	TaskID   string               `json:"task_id"`
	Messages []AgentThreadMessage `json:"messages"`
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

// PatchCollabRequest is one patch_collab tool action.
// Action is submit|list|preview|conflicts|reject|apply.
type PatchCollabRequest struct {
	Action          string
	ID              string
	IDs             []string // conflicts filter
	Patch           string   // submit / inline preview
	Title           string
	Reason          string // reject
	Status          string // list filter: pending|rejected|applied|all
	ArtifactID      string
	ArtifactVersion int
	ExpectedVersion int    // 0 = skip CAS
	WorkDir         string // active worktree (from tool Context)
}

// PatchCollabItem is one row on the team patch board.
type PatchCollabItem struct {
	ID              string   `json:"id"`
	Title           string   `json:"title,omitempty"`
	Status          string   `json:"status"` // pending|rejected|applied
	Submitter       string   `json:"submitter,omitempty"`
	Files           []string `json:"files,omitempty"`
	ArtifactID      string   `json:"artifact_id,omitempty"`
	ArtifactVersion int      `json:"artifact_version,omitempty"`
	RejectReason    string   `json:"reject_reason,omitempty"`
	AppliedSummary  string   `json:"applied_summary,omitempty"`
	Version         int      `json:"version"`
	CreatedAt       string   `json:"created_at,omitempty"` // RFC3339
	UpdatedAt       string   `json:"updated_at,omitempty"`
	// Patch is included on submit/preview/get-style responses (may be large).
	Patch string `json:"patch,omitempty"`
}

// PatchCollabResult is the patch_collab tool payload.
type PatchCollabResult struct {
	LeadID    string              `json:"lead_id,omitempty"`
	Action    string              `json:"action,omitempty"`
	Patch     *PatchCollabItem    `json:"patch,omitempty"`
	Patches   []PatchCollabItem   `json:"patches,omitempty"`
	Preview   *PatchPreview       `json:"preview,omitempty"`
	Conflicts *MultiPatchConflict `json:"conflicts,omitempty"`
	Files     []string            `json:"files,omitempty"` // post-apply paths
	Summary   string              `json:"summary,omitempty"`
	Conflict  bool                `json:"conflict,omitempty"`
	Detail    string              `json:"detail,omitempty"`
}

// DelegateRequest mutates or inspects first-class delegation lifecycle objects.
// Action is create|get|list|transition.
// Create mirrors task spawn fields (prompt/agent/…) plus criteria/deps/subscribe.
// Transition moves state with optional expected_version CAS.
type DelegateRequest struct {
	Action          string
	ID              string
	Prompt          string
	Name            string
	Agent           string
	Model           string
	Effort          string
	Assignee        string
	Criteria        []string
	Deps            []string
	Subscribe       []string
	Verify          []VerifyGate
	ContextBundle   ContextBundle
	State           string // target lifecycle state for transition
	Reason          string
	ExpectedVersion int // 0 = skip CAS
}

// DelegationItem is one lifecycle row for the delegate tool.
type DelegationItem struct {
	ID             string   `json:"id"`
	Prompt         string   `json:"prompt,omitempty"`
	Criteria       []string `json:"criteria,omitempty"`
	Deps           []string `json:"deps,omitempty"`
	Subscribe      []string `json:"subscribe,omitempty"`
	OwnerSessionID string   `json:"owner_session_id,omitempty"`
	Assignee       string   `json:"assignee,omitempty"`
	Agent          string   `json:"agent,omitempty"`
	Model          string   `json:"model,omitempty"`
	Effort         string   `json:"effort,omitempty"`
	Name           string   `json:"name,omitempty"`
	SessionID      string   `json:"session_id,omitempty"`
	State          string   `json:"state"`
	Version        int      `json:"version"`
	BlockReason    string   `json:"block_reason,omitempty"`
	SpawnPending   bool     `json:"spawn_pending,omitempty"`
	CreatedAt      string   `json:"created_at,omitempty"` // RFC3339
	UpdatedAt      string   `json:"updated_at,omitempty"`
}

// DelegateResult is the delegate tool payload.
type DelegateResult struct {
	LeadID   string           `json:"lead_id,omitempty"`
	Action   string           `json:"action,omitempty"`
	Item     *DelegationItem  `json:"item,omitempty"`
	Items    []DelegationItem `json:"items,omitempty"`
	Conflict bool             `json:"conflict,omitempty"`
	Detail   string           `json:"detail,omitempty"`
	// Spawn fields when create starts a child immediately.
	SessionID string `json:"session_id,omitempty"`
	Status    string `json:"status,omitempty"` // started|queued
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
// Wait, when non-nil, blocks until an owned-child event matches (or timeout).
// AgentRoster, when non-nil, lists the implicit session team (lead + peers).
// AgentMessage/AgentBroadcast, when non-nil, send peer mail on the team.
// TeamTask, when non-nil, mutates the shared lead-scoped team task board.
// PatchCollab, when non-nil, submits/previews/applies/rejects inspectable patches.
// Delegate, when non-nil, creates/lists/transitions first-class delegations.
// AskUser, when non-nil, blocks until the user answers a question batch.
// SwitchAgent, when non-nil, queues an agent switch applied when the turn ends.
// EnterPlanPhase starts the built-in plan→implement workflow at plan.
// AdvancePhase runs the active phase exit gate and loads the next phase.
// HandoffPlan is the unified plan approval + handoff used by exit_plan_mode
// (canonical plan id/version or bounded legacy text; records approval source).
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
	// NetworkAllow is the optional host/CIDR allowlist for webfetch (from
	// config network.allow). Empty means unrestricted public hosts; SSRF
	// private/loopback blocks still apply. Nil/empty Context is unrestricted.
	NetworkAllow []string
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
	// Wait blocks until an owned-child orchestration event matches, times out,
	// or the tool context is canceled. Emits wait.started / wait.resolved.
	Wait func(ctx context.Context, req WaitRequest) (WaitResult, error)
	// AgentRoster lists lead + teammates on the implicit session team.
	AgentRoster func(ctx context.Context, req AgentRosterRequest) (AgentRosterResult, error)
	// AgentMessage sends a peer mailbox message to one teammate.
	AgentMessage func(ctx context.Context, req AgentMessageRequest) (AgentMessageResult, error)
	// AgentBroadcast sends a peer mailbox message to all other teammates.
	AgentBroadcast func(ctx context.Context, req AgentBroadcastRequest) (AgentBroadcastResult, error)
	// AgentThread lists messages bound to a task_id / delegation id on the team.
	AgentThread func(ctx context.Context, req AgentThreadRequest) (AgentThreadResult, error)
	// TeamTask creates/lists/updates/claims/completes shared team board items.
	TeamTask func(ctx context.Context, req TeamTaskRequest) (TeamTaskResult, error)
	// PatchCollab submits/previews/rejects/applies inspectable multi-agent patches.
	PatchCollab func(ctx context.Context, req PatchCollabRequest) (PatchCollabResult, error)
	// Delegate creates/lists/transitions first-class delegation lifecycle objects.
	Delegate    func(ctx context.Context, req DelegateRequest) (DelegateResult, error)
	AskUser     func(ctx context.Context, req QuestionRequest) (QuestionResponse, error)
	SwitchAgent func(name string) error
	// EnterPlanPhase starts the default plan-implement workflow at the plan phase
	// (plan convenience adapter over StartWorkflow).
	EnterPlanPhase func() error
	// StartWorkflow activates any loaded workflow at phase 0 (exactly one active).
	StartWorkflow func(name string) error
	// StopWorkflow clears the active workflow phase and phase permissions.
	StopWorkflow func() error
	// AdvancePhase clears the current phase exit gate and advances (or ends).
	AdvancePhase func(ctx context.Context) error
	// HandoffPlan is the unified plan-mode approval + handoff path used by
	// exit_plan_mode. It validates the canonical (or legacy) plan, runs the
	// autonomy gate once, records approval source + plan identity, advances
	// the plan→implement workflow, and routes to build/orchestrator.
	HandoffPlan func(ctx context.Context, req PlanHandoffRequest) (PlanHandoffResult, error)
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
	// Ownership, when non-nil, tracks multi-agent path claims for overlap
	// detection (shared across the session team). Nil disables (solo / no team).
	Ownership *PathOwnership
	// SessionID identifies the calling agent for ownership claims.
	SessionID string
	// RootSessionID is the lineage root session id (empty ParentSessionID
	// ancestor). Plan tools use it as the plan owner identity; mutations
	// require SessionID == RootSessionID so children cannot mutate without
	// later delegated authority. Empty falls back to SessionID when the
	// caller is itself a root. Artifact tools use it as the team boundary
	// (access=team shares within the same root lineage).
	RootSessionID string
	// NotifyArtifact, when non-nil, is invoked after a successful artifact
	// create/update so the engine can emit protocol.ArtifactUpdated.
	// op is "create" or "update". Nil disables the event.
	NotifyArtifact ArtifactNotify
	// NotifyLedger, when non-nil, is invoked after a successful ledger
	// append/invalidate/supersede so the engine can emit protocol.LedgerUpdated.
	// op is "append", "invalidate", or "supersede". Nil disables the event.
	NotifyLedger LedgerNotify
	// ContextBundle is the sealed spawn context for this agent (children only
	// when the lead attached one). Read via the context_bundle tool. Nil/empty
	// means no bundle was attached.
	ContextBundle *ContextBundle
	// MemberName is an optional stable teammate alias for ownership messages.
	MemberName string
	// OnOverlap is invoked when ClaimWrite/lease detects an active conflict
	// (engine emits protocol.path.overlap). Nil skips the callback.
	OnOverlap OverlapNotify
	// OwnershipQuery, when non-nil, returns the team ownership/overlap map
	// (agent_ownership list). Nil means the tool is unavailable.
	OwnershipQuery func(ctx context.Context) (OwnershipSnapshot, error)
	// OwnershipLease, when non-nil, acquires a path-prefix lease for this session.
	OwnershipLease func(ctx context.Context, path string, exclusive bool) (TouchResult, error)
	// OwnershipReleaseLease, when non-nil, drops a lease (or all when path empty).
	OwnershipReleaseLease func(ctx context.Context, path string) error
	// Checkpoint, when non-nil, records pre-mutation file state for undo
	// restore (first touch per turn). Absolute path. Nil disables checkpoints.
	// Composes with TurnDiff (per-turn create/update/delete summary) and
	// PathOwnership (#772); do not fork a second file-state system.
	Checkpoint func(absPath string)
	// CheckpointUncovered, when non-nil, marks the active turn as having
	// possible disk mutations outside per-file snapshots (e.g. bash). Reason
	// is a short stable token. Nil disables. See CheckpointStore.MarkUncovered.
	CheckpointUncovered func(reason string)
	// TurnDiff, when non-nil, records harness file change kinds for the
	// active turn (timeline/UI). Nil disables. Tools call NoteTurnChange.
	TurnDiff *TurnDiff
	// FileSync, when non-nil, is invoked after a successful file mutation so
	// hosts can drive LSP document sync (didOpen/didChange/didClose).
	// absPath is absolute; content is the full new text; deleted is true for
	// removals (content may be empty). Nil disables. Must not panic the tool.
	FileSync func(absPath string, content string, deleted bool)
	// CollectDiagnostics, when non-nil, returns model-facing diagnostic text
	// for the given absolute paths after file mutations (one call per tool
	// result so multi-file patches produce a single block). Empty string means
	// none. Nil disables. Must not panic the tool.
	CollectDiagnostics func(ctx context.Context, absPaths []string) string
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

// MarkUncovered records that this turn may have disk mutations outside
// per-file checkpoints (e.g. bash). Safe on a nil receiver or nil callback.
func (tc *Context) MarkUncovered(reason string) {
	if tc == nil || tc.CheckpointUncovered == nil || reason == "" {
		return
	}
	tc.CheckpointUncovered(reason)
}

// NoteTurnChange records a harness file mutation on TurnDiff when set.
// existedBefore should reflect disk state before the mutation; deleted marks
// removals. Safe on a nil receiver or nil TurnDiff.
func (tc *Context) NoteTurnChange(absPath string, existedBefore, deleted bool) {
	if tc == nil || tc.TurnDiff == nil || absPath == "" {
		return
	}
	rel := RelPathForDiff(tc.WorkDir, absPath)
	tc.TurnDiff.Note(rel, existedBefore, deleted)
}

// NotifyFileSync tells optional listeners (e.g. LSP) about a successful mutation.
// Safe on a nil receiver or nil FileSync. Recovers panics from the callback.
func (tc *Context) NotifyFileSync(absPath, content string, deleted bool) {
	if tc == nil || tc.FileSync == nil || absPath == "" {
		return
	}
	defer func() { _ = recover() }()
	tc.FileSync(absPath, content, deleted)
}

// AppendDiagnostics appends CollectDiagnostics output for absPaths onto res.Output.
// Multi-file tools pass every touched path in one call (one diagnostic block).
// Safe on a nil receiver or nil CollectDiagnostics. Recovers panics (returns res).
func (tc *Context) AppendDiagnostics(ctx context.Context, res Result, absPaths ...string) (out Result) {
	out = res
	if tc == nil || tc.CollectDiagnostics == nil || len(absPaths) == 0 {
		return out
	}
	defer func() { _ = recover() }()
	// Filter empties without allocating when already clean.
	paths := make([]string, 0, len(absPaths))
	for _, p := range absPaths {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return out
	}
	block := tc.CollectDiagnostics(ctx, paths)
	if block == "" {
		return out
	}
	if out.Output == "" {
		out.Output = block
	} else {
		out.Output = out.Output + "\n\n" + block
	}
	return out
}

// Tool is the executable unit registered for model tool-calls.
// Optional Contractor (Contract method) declares side-effect and idempotency;
// LookupContract falls back to DefaultContract when omitted.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage // JSON Schema for the tool's parameters
	Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error)
}
