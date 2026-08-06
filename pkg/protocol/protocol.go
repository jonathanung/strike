// Package protocol is the public Op/Event wire schema for strike.
//
// Frontends submit Ops; the engine emits Events. The TUI and any other
// frontend depend only on this package (or the internal/protocol re-export
// shim), never on engine internals. The event stream is also the persistence
// format: a session transcript is a JSONL log of Event envelopes (see
// internal/session).
//
// # Import path
//
//	import "github.com/jonathanung/strike-cli/pkg/protocol"
//
// In-tree packages may still import internal/protocol, which re-exports this
// package unchanged so existing call sites keep compiling.
//
// # Stability (semver)
//
// [Version] is the semantic version of this wire schema. Guarantees:
//
//   - Major: breaking JSON field renames/removals, changed envelope type
//     strings, or changed meaning of an existing field.
//   - Minor: new Op/Event types, new optional JSON fields, new type-string
//     cases (unknown types still fail Decode — callers must tolerate forward
//     growth by ignoring or upgrading).
//   - Patch: docs, helpers, and bug fixes that do not change encoded JSON.
//
// Legacy session JSONL without an envelope "v" field is treated as compatible
// with the 1.x line. Additive optional fields use omitempty so old readers
// keep working.
package protocol

import (
	"encoding/json"
	"strings"
	"time"
)

// Effort is the reasoning dial as the user sees it: how much internal
// reasoning the model spends before answering. It is the frontend-facing
// vocabulary, deliberately independent of provider.Effort — the engine
// translates between the two so internal/tui never reaches into the provider
// layer. The zero value means "unset", leaving the provider default in place.
type Effort string

const (
	EffortDefault Effort = ""
	// EffortOff asks for as little reasoning as the provider allows. It is
	// not a guarantee of zero: Anthropic disables thinking outright, but the
	// OpenAI family has no zero setting and floors at "minimal".
	EffortOff    Effort = "off"
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	// EffortXHigh and EffortMax exist on Anthropic's ladder; providers whose
	// own ladder tops out lower clamp them down to their highest level.
	EffortXHigh Effort = "xhigh"
	EffortMax   Effort = "max"
)

// Efforts lists the selectable levels from least to most reasoning,
// excluding the unset sentinel.
func Efforts() []Effort {
	return []Effort{EffortOff, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}
}

// ParseEffort resolves a user-typed level, case- and space-insensitively.
// An empty string yields EffortDefault; anything unrecognized reports false.
func ParseEffort(value string) (Effort, bool) {
	normalized := Effort(strings.ToLower(strings.TrimSpace(value)))
	if normalized == EffortDefault {
		return EffortDefault, true
	}
	for _, level := range Efforts() {
		if normalized == level {
			return level, true
		}
	}
	return EffortDefault, false
}

// Describe returns the one-line rationale rendered in the picker and help.
func (e Effort) Describe() string {
	switch e {
	case EffortOff:
		return "least reasoning the provider allows — fastest and cheapest"
	case EffortLow:
		return "minimal reasoning for short, scoped tasks"
	case EffortMedium:
		return "balanced reasoning for routine work"
	case EffortHigh:
		return "thorough reasoning — the provider default"
	case EffortXHigh:
		return "deeper reasoning, best for coding and agentic work"
	case EffortMax:
		return "maximum reasoning when correctness beats cost"
	default:
		return "provider default"
	}
}

// Autonomy is the per-session exit-gate policy dial: who clears phase
// progression. Unlike Effort, the zero value is not "unset" — Normalize maps
// it to AutonomySupervised so the mode is always explicit in the status line.
// Distinct from PermissionMode (tool-permission posture) and from --auto.
// Runtime phase exits honor this dial via one shared resolver — workflow
// Exit.Type is not authoritative.
type Autonomy string

const (
	// AutonomySupervised requires a human to clear every phase exit (safest default).
	AutonomySupervised Autonomy = "supervised"
	// AutonomyAgent lets the agent self-affirm phase completion (phase_done).
	AutonomyAgent Autonomy = "agent"
	// AutonomyChecks advances when configured check commands exit 0.
	AutonomyChecks Autonomy = "checks"
	// AutonomySkipAll bypasses workflow/plan approval gates only. It does not
	// grant or bypass tool permissions.
	AutonomySkipAll Autonomy = "skip-all"
)

// Autonomies lists selectable modes from most to least human oversight.
func Autonomies() []Autonomy {
	return []Autonomy{AutonomySupervised, AutonomyAgent, AutonomyChecks, AutonomySkipAll}
}

// ParseAutonomy resolves a user-typed mode, case- and space-insensitively.
// Empty input yields AutonomySupervised; unrecognized values report false.
func ParseAutonomy(value string) (Autonomy, bool) {
	normalized := Autonomy(strings.ToLower(strings.TrimSpace(value)))
	if normalized == "" {
		return AutonomySupervised, true
	}
	for _, mode := range Autonomies() {
		if normalized == mode {
			return mode, true
		}
	}
	return "", false
}

// Normalize returns a concrete mode: empty becomes AutonomySupervised.
func (a Autonomy) Normalize() Autonomy {
	if a == "" {
		return AutonomySupervised
	}
	return a
}

// Describe returns the one-line rationale rendered in the picker and help.
func (a Autonomy) Describe() string {
	switch a.Normalize() {
	case AutonomyAgent:
		return "agent clears phase gates itself — less interruption"
	case AutonomyChecks:
		return "commands must pass before a phase advances"
	case AutonomySkipAll:
		return "skip workflow/plan approval — tool perms unchanged"
	default:
		return "you approve phase gates — safest default"
	}
}

// Short is the compact status-line label (fits beside model/effort badges).
func (a Autonomy) Short() string {
	switch a.Normalize() {
	case AutonomyAgent:
		return "agent"
	case AutonomyChecks:
		return "checks"
	case AutonomySkipAll:
		return "skip"
	default:
		return "sup"
	}
}

// PermissionMode is the per-session tool-permission posture dial (distinct from
// Autonomy exit gates). Zero value normalizes to PermissionModeDefault.
type PermissionMode string

const (
	// PermissionModeDefault uses normal ask rules (config + agent + phase).
	PermissionModeDefault PermissionMode = "default"
	// PermissionModePlan is hard read-only (deny write/edit) and aligns with
	// the plan agent / plan-implement workflow.
	PermissionModePlan PermissionMode = "plan"
	// PermissionModeSoftApprove shows permission asks with a visible countdown
	// that auto-allows once if the user does nothing (TUI soft-approve). Engine
	// evaluation matches default; hard denies still win.
	PermissionModeSoftApprove PermissionMode = "soft-approve"
	// PermissionModeAcceptEdits auto-allows edit/write; bash/network still ask.
	PermissionModeAcceptEdits PermissionMode = "accept-edits"
	// PermissionModeYolo skips asks that are not hard-denied (agent/phase/config deny).
	PermissionModeYolo PermissionMode = "yolo"

	// SoftApproveSeconds is the default visible countdown for soft-approve mode
	// when permissionAutoApproveSeconds is unset (0).
	SoftApproveSeconds = 15
)

// PermissionModes lists postures from safest to most permissive for cycling.
func PermissionModes() []PermissionMode {
	return []PermissionMode{
		PermissionModeDefault,
		PermissionModePlan,
		PermissionModeSoftApprove,
		PermissionModeAcceptEdits,
		PermissionModeYolo,
	}
}

// ParsePermissionMode resolves a user-typed mode, case- and space-insensitively.
// Empty input yields PermissionModeDefault; unrecognized values report false.
// Accepts "accept_edits"/"acceptedits" for accept-edits and "soft"/"softapprove"
// for soft-approve.
func ParsePermissionMode(value string) (PermissionMode, bool) {
	normalized := PermissionMode(strings.ToLower(strings.TrimSpace(value)))
	normalized = PermissionMode(strings.ReplaceAll(string(normalized), "_", "-"))
	normalized = PermissionMode(strings.ReplaceAll(string(normalized), " ", ""))
	switch normalized {
	case "":
		return PermissionModeDefault, true
	case "acceptedits":
		return PermissionModeAcceptEdits, true
	case "soft", "softapprove":
		return PermissionModeSoftApprove, true
	}
	for _, mode := range PermissionModes() {
		if normalized == mode {
			return mode, true
		}
	}
	return "", false
}

// Normalize returns a concrete mode: empty becomes PermissionModeDefault.
func (m PermissionMode) Normalize() PermissionMode {
	if m == "" {
		return PermissionModeDefault
	}
	return m
}

// Describe returns the one-line rationale for pickers and notices.
func (m PermissionMode) Describe() string {
	switch m.Normalize() {
	case PermissionModePlan:
		return "read-only plan posture — write/edit denied"
	case PermissionModeSoftApprove:
		return "count down 15s then allow once — veto anytime"
	case PermissionModeAcceptEdits:
		return "auto-allow edit/write — still ask on bash/network"
	case PermissionModeYolo:
		return "skip permission asks — explicit denies still apply"
	default:
		return "normal permission prompts"
	}
}

// Short is the compact status-line label.
func (m PermissionMode) Short() string {
	switch m.Normalize() {
	case PermissionModePlan:
		return "plan"
	case PermissionModeSoftApprove:
		return "soft"
	case PermissionModeAcceptEdits:
		return "edits"
	case PermissionModeYolo:
		return "yolo"
	default:
		return "def"
	}
}

// Next returns the following mode in the cycle dial (wraps).
func (m PermissionMode) Next() PermissionMode {
	modes := PermissionModes()
	cur := m.Normalize()
	for i, mode := range modes {
		if mode == cur {
			return modes[(i+1)%len(modes)]
		}
	}
	return PermissionModeDefault
}

// Decision is a user's answer to a permission ask.
type Decision string

const (
	DecisionOnce Decision = "once"
	// DecisionAlways remembers the grant for the rest of this session only.
	// The TUI labels this "allow session"; the wire value stays "always" for
	// JSONL compatibility.
	DecisionAlways Decision = "always"
	// DecisionProject remembers the grant for this session and appends it to
	// the project config so future sessions in the same workdir inherit it.
	DecisionProject Decision = "project"
	DecisionReject  Decision = "reject"
)

// Op is a client -> engine submission.
type Op interface{ isOp() }

// ImageAttachment is one image attached to a user message (paste/drop).
// Data is standard base64 (no data: URI prefix). MIME is a full media type
// such as image/png.
type ImageAttachment struct {
	MIME string `json:"mime"`
	Data string `json:"data"`
}

// UserInput submits a user message, starting a turn.
type UserInput struct {
	Text   string            `json:"text"`
	Images []ImageAttachment `json:"images,omitempty"`
}

// PermissionReply resolves a pending PermissionAsked event.
type PermissionReply struct {
	RequestID string   `json:"requestId"`
	Decision  Decision `json:"decision"`
	// Message optionally carries reject feedback, fed back to the model as
	// the tool result so it can course-correct.
	Message string `json:"message,omitempty"`
}

// QuestionReply resolves a pending QuestionAsked event with one answer
// string per prompt (same order as Questions).
type QuestionReply struct {
	RequestID string   `json:"requestId"`
	Answers   []string `json:"answers"`
}

// Interrupt cancels the running turn, if any.
type Interrupt struct{}

// SelectModel switches the active provider (and optionally model). An empty
// Model selects the provider's default. Rejected while a turn is running.
type SelectModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
}

// SelectAgent switches the active agent (system prompt + optional
// provider/model pins). Rejected while a turn is running.
type SelectAgent struct {
	Name string `json:"name"`
}

// SetEffort changes the reasoning dial for subsequent turns. Rejected while
// a turn is running, like the other selection ops.
type SetEffort struct {
	Level Effort `json:"level"`
}

// SetAutonomy changes the session exit-gate policy. Rejected while a turn is
// running, like the other selection ops.
type SetAutonomy struct {
	Mode Autonomy `json:"mode"`
}

// SetPermissionMode changes the session tool-permission posture. Accepted
// while a turn is running so Shift+Tab / /mode can tighten or loosen asks
// mid-turn; the permission service applies the new posture to subsequent
// tool checks and rejects any in-flight permission asks.
type SetPermissionMode struct {
	Mode PermissionMode `json:"mode"`
}

// SetFast toggles OpenAI priority (fast) service tier for subsequent turns.
// Rejected while a turn is running. Providers and models that do not support
// priority tier ignore the flag silently.
type SetFast struct {
	Enabled bool `json:"enabled"`
}

// StartWorkflow activates a loaded workflow at phase index 0. Exactly one
// workflow may be active per root session; starting replaces any prior active
// workflow after the target is validated. Rejected while a turn is running.
type StartWorkflow struct {
	Name string `json:"name"`
}

// StopWorkflow clears the active workflow phase, context, and phase permission
// profile. No-op when none is active. Rejected while a turn is running.
type StopWorkflow struct{}

// FilesChanged reports paths the user edited outside the agent (for example
// via /vim). The engine invalidates any prior read snapshots for those paths
// so a subsequent edit/write fails until the model re-reads.
type FilesChanged struct {
	Paths  []string `json:"paths"`
	Reason string   `json:"reason,omitempty"`
}

// Compact requests model-history compaction. Rejected while a turn is running.
// Strategy selects trim (drop older turns, default) or summarize (model-authored
// summary). Empty uses the engine/config default.
type Compact struct {
	Strategy string `json:"strategy,omitempty"` // trim | summarize
}

// InspectEffectivePrompt requests a snapshot of the composed system-prompt
// layers (provenance + sizes). Prefer the last Stream composition when one
// exists; otherwise the current composition for the next request. Never
// carries raw API keys — previews are redacted by the engine.
type InspectEffectivePrompt struct{}

// InspectDiagnosticBundle requests a versioned prompt/config diagnostic
// bundle (layer map + effective dials + digests). Prefer the last Stream
// composition when one exists; otherwise the current composition. Never
// carries raw API keys — previews and paths are redacted by the engine.
// See pkg/diag for the export document schema.
type InspectDiagnosticBundle struct{}

// Rewind removes the last completed user↔assistant turn from model-facing
// history. When RestoreFiles is true, also restores per-file checkpoints
// captured before mutating tools in that turn (never git reset --hard).
// Rejected while a turn is running.
type Rewind struct {
	// RestoreFiles reverts disk changes from the last turn's file checkpoints.
	RestoreFiles bool `json:"restoreFiles,omitempty"`
}

func (UserInput) isOp()               {}
func (PermissionReply) isOp()         {}
func (QuestionReply) isOp()           {}
func (Interrupt) isOp()               {}
func (SelectModel) isOp()             {}
func (SelectAgent) isOp()             {}
func (SetEffort) isOp()               {}
func (SetAutonomy) isOp()             {}
func (SetPermissionMode) isOp()       {}
func (SetFast) isOp()                 {}
func (StartWorkflow) isOp()           {}
func (StopWorkflow) isOp()            {}
func (FilesChanged) isOp()            {}
func (Compact) isOp()                 {}
func (InspectEffectivePrompt) isOp()  {}
func (InspectDiagnosticBundle) isOp() {}
func (Rewind) isOp()                  {}

// Event is an engine -> client notification.
type Event interface{ isEvent() }

// Correlation identifies the session, turn, and provider request that
// produced an event. Embedded anonymously so JSON stays flat; omitempty
// keeps legacy envelopes without IDs decodable and re-encodable as `{}`.
// ParentSessionID and Depth record immutable child-session lineage when a
// foreground subagent is running (root sessions leave both zero-valued).
//
// ProviderRequestID is unique per Stream attempt (including retries).
// Attempt is the 1-based try number within one logical model request of a
// turn (tool-loop iteration); omitempty keeps legacy records without it.
type Correlation struct {
	SessionID         string `json:"sessionId,omitempty"`
	TurnID            string `json:"turnId,omitempty"`
	ProviderRequestID string `json:"providerRequestId,omitempty"`
	Attempt           int    `json:"attempt,omitempty"`
	ParentSessionID   string `json:"parentSessionId,omitempty"`
	Depth             int    `json:"depth,omitempty"`
}

// ChildStatus is the terminal outcome of a foreground child session.
type ChildStatus string

const (
	// Named ChildStatus* so they do not collide with the ChildCompleted event type.
	ChildStatusCompleted ChildStatus = "completed"
	ChildStatusFailed    ChildStatus = "failed"
	ChildStatusCanceled  ChildStatus = "canceled"
	// ChildStatusBlocked means the implementer claimed done but independent
	// verification gates failed (or the task is otherwise blocked pending fix).
	// Distinct from failed (runtime/error) so leads can re-open work.
	ChildStatusBlocked ChildStatus = "blocked"
)

// TeamMemberState is the roster lifecycle state of one agent on an implicit
// session-scoped team (lead + children). Values align with ChildStatus for
// terminal outcomes; "running" covers live members including the lead.
type TeamMemberState string

const (
	TeamMemberRunning   TeamMemberState = "running"
	TeamMemberCompleted TeamMemberState = "completed"
	TeamMemberFailed    TeamMemberState = "failed"
	TeamMemberCanceled  TeamMemberState = "canceled"
	TeamMemberBlocked   TeamMemberState = "blocked"
)

// TeamMemberStateFromChild maps a terminal child outcome onto a roster state.
// Unknown statuses fall back to failed.
func TeamMemberStateFromChild(s ChildStatus) TeamMemberState {
	switch s {
	case ChildStatusCompleted:
		return TeamMemberCompleted
	case ChildStatusCanceled:
		return TeamMemberCanceled
	case ChildStatusBlocked:
		return TeamMemberBlocked
	case ChildStatusFailed:
		return TeamMemberFailed
	default:
		return TeamMemberFailed
	}
}

// TeamRosterMember is one entry in a TeamRoster snapshot event.
// State prefers task_status vocabulary (starting|working|needs_attention|
// completed|failed|canceled|unknown) when the emitter has live detail;
// otherwise terminal TeamMemberState values are used.
// QueuePools/QueueLabel identify a constrained pool while waiting on admission
// (no exact queue-position guarantee).
type TeamRosterMember struct {
	SessionID       string   `json:"sessionId"`
	Name            string   `json:"name,omitempty"`
	Agent           string   `json:"agent,omitempty"`
	State           string   `json:"state"`
	ParentSessionID string   `json:"parentSessionId,omitempty"`
	Depth           int      `json:"depth,omitempty"`
	StartedAt       string   `json:"startedAt,omitempty"` // RFC3339 when known
	TerminalSummary string   `json:"terminalSummary,omitempty"`
	Role            string   `json:"role,omitempty"` // "lead" or "member"
	QueuePools      []string `json:"queuePools,omitempty"`
	QueueLabel      string   `json:"queueLabel,omitempty"`
	// Live observability (#774). Optional; empty when unknown.
	Objective    string   `json:"objective,omitempty"`
	LastAction   string   `json:"lastAction,omitempty"`
	BlockReason  string   `json:"blockReason,omitempty"`
	FilesTouched []string `json:"filesTouched,omitempty"`
	// Budget remaining / usage (camelCase wire). Omitted when no tracking.
	Budget *AgentBudgetView `json:"budget,omitempty"`
}

// AgentBudgetView is the protocol wire shape for per-agent budget remaining.
// Nested under TeamRosterMember and ChildEscalated. Zero limits mean unlimited.
// Session-level maxSessionCostUSD (#577) is the outer envelope when configured;
// per-agent maxCostUSD nests inside it.
type AgentBudgetView struct {
	MaxWallClockS       int      `json:"maxWallClockS,omitempty"`
	MaxTokens           int      `json:"maxTokens,omitempty"`
	MaxCostUSD          float64  `json:"maxCostUsd,omitempty"`
	MaxToolCalls        int      `json:"maxToolCalls,omitempty"`
	MaxDangerousTools   int      `json:"maxDangerousTools,omitempty"`
	StallAfterS         int      `json:"stallAfterS,omitempty"`
	LoopDetectN         int      `json:"loopDetectN,omitempty"`
	ElapsedS            int      `json:"elapsedS,omitempty"`
	TokensUsed          int      `json:"tokensUsed,omitempty"`
	CostUSDUsed         float64  `json:"costUsdUsed,omitempty"`
	ToolCalls           int      `json:"toolCalls,omitempty"`
	DangerousTools      int      `json:"dangerousTools,omitempty"`
	WallClockRemainingS *int     `json:"wallClockRemainingS,omitempty"`
	TokensRemaining     *int     `json:"tokensRemaining,omitempty"`
	ToolCallsRemaining  *int     `json:"toolCallsRemaining,omitempty"`
	DangerousRemaining  *int     `json:"dangerousRemaining,omitempty"`
	CostUSDRemaining    *float64 `json:"costUsdRemaining,omitempty"`
	Stall               bool     `json:"stall,omitempty"`
	Loop                bool     `json:"loop,omitempty"`
	Escalated           bool     `json:"escalated,omitempty"`
	EscalateKind        string   `json:"escalateKind,omitempty"`
	EscalateReason      string   `json:"escalateReason,omitempty"`
}

// TeamRoster is a full snapshot of the implicit session team roster.
// Emitted when membership or member state changes so UIs can render without
// calling the agent_roster tool. Correlation.SessionID is the lead id.
type TeamRoster struct {
	Correlation
	LeadID  string             `json:"leadId"`
	Members []TeamRosterMember `json:"members"`
}

// ChildStarted marks the beginning of a foreground child/subagent session.
// Emitted by the parent engine with the child's correlation.
type ChildStarted struct {
	Correlation
	Agent  string `json:"agent,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	// Name is an optional stable teammate alias assigned at spawn.
	Name string `json:"name,omitempty"`
}

// CompletionHandoff is the structured work product for a delegated child at
// terminal status. Always present on ChildCompleted (empty slices/strings are
// honest). Success fills the full schema; failure/cancel may leave
// verification/findings empty. filesChanged may be engine-tracked and/or
// model-supplied (merged, de-duplicated).
//
// Wire JSON uses camelCase. Model-facing notices and task_status also expose a
// snake_case view of the same fields.
type CompletionHandoff struct {
	// Summary is a short human/agent-readable outcome.
	Summary string `json:"summary"`
	// FilesChanged lists workspace-relative paths created, edited, or deleted.
	FilesChanged []string `json:"filesChanged"`
	// Verification describes what was run/checked and the results.
	Verification string `json:"verification,omitempty"`
	// Findings are notable discoveries, risks, or TODOs.
	Findings []string `json:"findings,omitempty"`
	// Blockers are unresolved blockers (empty when none).
	Blockers []string `json:"blockers,omitempty"`
	// RecommendedNextAction is a concrete next step for the lead or peers.
	RecommendedNextAction string `json:"recommendedNextAction,omitempty"`
	// Incomplete is true when the engine could not parse a model-supplied
	// structured handoff and filled defaults + tracked files only.
	Incomplete bool `json:"incomplete,omitempty"`
	// SectionTitle/SectionBody are optional plan-section refinement fields
	// (plan_delegate). Present when the child refined a correlated section.
	// SectionBodySet distinguishes omitted body from explicit empty string.
	SectionTitle   string `json:"sectionTitle,omitempty"`
	SectionBody    string `json:"sectionBody,omitempty"`
	SectionBodySet bool   `json:"sectionBodySet,omitempty"`
}

// VerificationCheck is one independent gate outcome inside a VerificationReport.
type VerificationCheck struct {
	Name       string `json:"name,omitempty"`
	Kind       string `json:"kind"`
	Value      string `json:"value,omitempty"`
	Passed     bool   `json:"passed"`
	ExitCode   int    `json:"exitCode,omitempty"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// VerificationEnv is audit metadata for a verification run (cwd, session, models).
type VerificationEnv struct {
	WorkDir    string `json:"workDir,omitempty"`
	SessionID  string `json:"sessionId,omitempty"`
	WorktreeID string `json:"worktreeId,omitempty"`
	ModelID    string `json:"modelId,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`  // RFC3339
	FinishedAt string `json:"finishedAt,omitempty"` // RFC3339
}

// VerificationReport is the harness-owned outcome of independent completion
// gates. Distinct from CompletionHandoff.Verification (model self-report text):
// implementer-done is Claimed; harness-verified is Verified/Passed.
// Present on ChildCompleted when gates were configured at spawn (or when a
// solo/harness path attaches verification — shared schema with #806).
type VerificationReport struct {
	Passed     bool                `json:"passed"`
	Claimed    bool                `json:"claimed"`
	Verified   bool                `json:"verified"`
	Checks     []VerificationCheck `json:"checks"`
	Env        VerificationEnv     `json:"env"`
	Summary    string              `json:"summary,omitempty"`
	DurationMs int64               `json:"durationMs,omitempty"`
}

// ChildCompleted marks the end of a foreground child/subagent session.
// Emitted by the parent engine with the child's correlation.
type ChildCompleted struct {
	Correlation
	Status  ChildStatus `json:"status"`
	Summary string      `json:"summary,omitempty"`
	// Name is the stable teammate alias when one was assigned at spawn.
	Name string `json:"name,omitempty"`
	// Handoff is the structured completion payload (always set by the engine).
	Handoff CompletionHandoff `json:"handoff"`
	// DelegationID links this child to its orchestration lifecycle object when set.
	DelegationID string `json:"delegationId,omitempty"`
	// Verification is set when independent completion gates ran. When gates
	// were configured, Status is completed only if Verification.Passed;
	// otherwise Status is blocked (gate failure) with this report attached.
	Verification *VerificationReport `json:"verification,omitempty"`
}

// ChildEscalated reports a per-child budget/stall/loop trip (#774).
// Emitted on the parent stream before interrupt. Action is "interrupted"
// (hard kill in flight) or "signaled" (soft observability only — unused in v1
// hard path). Kind is wall_clock|tokens|cost_usd|tool_calls|dangerous_tools|
// stall|loop. Correlation is the child session.
//
// Soft stall/loop flags also appear on task_status / team.roster without this
// event; hard limits always emit ChildEscalated and stop the child.
// Stale-child detection (#517) is the stall kind, not a separate mechanism.
type ChildEscalated struct {
	Correlation
	// Name is the stable teammate alias when one was assigned at spawn.
	Name string `json:"name,omitempty"`
	// Kind is the trip class (wall_clock, tokens, stall, loop, …).
	Kind string `json:"kind"`
	// Reason is a human/agent-readable explanation.
	Reason string `json:"reason"`
	// Action is interrupted|signaled.
	Action string `json:"action"`
	// TerminalStatus is the intended ChildCompleted status after shutdown
	// (failed for hard resource budgets, blocked for stall/loop).
	TerminalStatus ChildStatus `json:"terminalStatus,omitempty"`
	// Budget is the snapshot at escalation time when known.
	Budget *AgentBudgetView `json:"budget,omitempty"`
}

// DelegationState is the orchestration lifecycle of a first-class delegation
// (task spawn with optional criteria/deps/subscriptions). Distinct from live
// child session pulses (starting|working|needs_attention) and terminal
// ChildStatus values; engines map between them.
type DelegationState string

const (
	DelegationQueued   DelegationState = "queued"
	DelegationWorking  DelegationState = "working"
	DelegationBlocked  DelegationState = "blocked"
	DelegationReview   DelegationState = "review"
	DelegationDone     DelegationState = "done"
	DelegationFailed   DelegationState = "failed"
	DelegationCanceled DelegationState = "canceled"
)

// DelegationChanged reports a lifecycle transition on a delegation object.
// Correlation is the owner (creator) session. Emitted for UI/debug and for
// event-wait consumers (#775). Subscribe hooks on create may also notify the
// lead via mailbox when State matches a subscribed kind.
type DelegationChanged struct {
	Correlation
	// ID is the stable delegation id (d1, d2, …).
	ID string `json:"id"`
	// State is the new lifecycle state after the transition.
	State DelegationState `json:"state"`
	// Prev is the prior state (empty on create).
	Prev DelegationState `json:"prev,omitempty"`
	// Version is the CAS token after the transition.
	Version int `json:"version"`
	// SessionID is the child session when one is linked (may be empty while queued).
	SessionID string `json:"sessionId,omitempty"`
	// Name is the optional teammate alias.
	Name string `json:"name,omitempty"`
	// Reason is an optional human/agent-readable cause (block reason, cancel, …).
	Reason string `json:"reason,omitempty"`
	// OwnerSessionID is the creator session id.
	OwnerSessionID string `json:"ownerSessionId,omitempty"`
}

// Wait outcome labels on WaitResolved.
const (
	WaitOutcomeMatched  = "matched"
	WaitOutcomeTimeout  = "timeout"
	WaitOutcomeCanceled = "canceled"
)

// WaitStarted marks an orchestration wait subscription (wait tool).
// Correlation is the waiting agent. TargetSessionID is an optional filter
// (empty = any owned child). Events lists canonical kinds
// (task.done|task.failed|task.canceled|task.blocked).
type WaitStarted struct {
	Correlation
	WaitID          string   `json:"waitId"`
	Events          []string `json:"events"`
	TargetSessionID string   `json:"targetSessionId,omitempty"`
	TimeoutMs       int      `json:"timeoutMs,omitempty"`
}

// WaitResolved is the outcome of a wait subscription.
// Outcome is matched|timeout|canceled. On matched, Event is the canonical
// kind and TargetSessionID/Status identify the child; Handoff is set for
// terminal task.* outcomes when available.
type WaitResolved struct {
	Correlation
	WaitID          string            `json:"waitId"`
	Outcome         string            `json:"outcome"` // matched | timeout | canceled
	Event           string            `json:"event,omitempty"`
	TargetSessionID string            `json:"targetSessionId,omitempty"`
	TargetName      string            `json:"targetName,omitempty"`
	Status          string            `json:"status,omitempty"`
	Summary         string            `json:"summary,omitempty"`
	Handoff         CompletionHandoff `json:"handoff,omitempty"`
	HasHandoff      bool              `json:"hasHandoff,omitempty"`
}

// Agent message urgency levels (coordination contracts).
const (
	AgentUrgencyNormal  = "normal"
	AgentUrgencyHigh    = "high"
	AgentUrgencyBlocker = "blocker"
)

// Agent message kinds (coordination contracts on the mailbox plane).
const (
	AgentMessageKindMessage    = "message"
	AgentMessageKindRequest    = "request"
	AgentMessageKindAck        = "ack"
	AgentMessageKindTimeout    = "timeout"
	AgentMessageKindEscalation = "escalation"
)

// AgentMessage records a peer/team mailbox delivery for UI and debugging.
// Correlation is the recipient session. Body is the message text; From/To are
// session ids; TeamID is the lead session id (team identity).
// Emitted on the recipient engine at boundary injection (tool-round / idle
// nudge), never mid-tool-call.
//
// Optional coordination-contract fields (task binding, urgency, ack/request
// kinds) are additive; empty values preserve pre-contract plain messages.
type AgentMessage struct {
	Correlation
	From string `json:"from"`
	To   string `json:"to"`
	Body string `json:"body"`
	// Summary is an optional short UI label (not required for delivery).
	Summary string `json:"summary,omitempty"`
	TeamID  string `json:"teamId,omitempty"`
	// MessageID is a stable id for ack/dedup within the session.
	MessageID string `json:"messageId,omitempty"`
	// TaskID binds the message to a team_task or delegation id (thread key).
	TaskID string `json:"taskId,omitempty"`
	// Urgency is normal|high|blocker (empty = normal).
	Urgency string `json:"urgency,omitempty"`
	// Kind is message|request|ack|timeout|escalation (empty = message).
	Kind string `json:"kind,omitempty"`
	// RequireAck is true when the sender requested an explicit ack.
	RequireAck bool `json:"requireAck,omitempty"`
	// InReplyTo is the message id being acked (kind=ack) or related.
	InReplyTo string `json:"inReplyTo,omitempty"`
	// EscalateTo is the session id that receives timeout escalation (default lead).
	EscalateTo string `json:"escalateTo,omitempty"`
	// AckStatus is pending|acked|timed_out when tracked on a request-ack message.
	AckStatus string `json:"ackStatus,omitempty"`
}

// AgentContractTimeout reports that a require-ack peer message was not acked
// before its TTL. Correlation is the original sender session. An escalation
// mailbox delivery to EscalateTo (default lead) is also enqueued when live.
type AgentContractTimeout struct {
	Correlation
	MessageID  string `json:"messageId"`
	From       string `json:"from"`
	To         string `json:"to"`
	TaskID     string `json:"taskId,omitempty"`
	TeamID     string `json:"teamId,omitempty"`
	Urgency    string `json:"urgency,omitempty"`
	EscalateTo string `json:"escalateTo,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// UserMessage echoes accepted user input into the event stream so the
// transcript is fully reconstructable from events alone.
type UserMessage struct {
	Correlation
	Text   string            `json:"text"`
	Images []ImageAttachment `json:"images,omitempty"`
}

// SessionTitled records the human-readable session title. Emitted once when
// the first user message is accepted (derived from that text). Later emits
// may rename; consumers should take the last title in the log.
type SessionTitled struct {
	Correlation
	Title string `json:"title"`
}

type TurnStarted struct {
	Correlation
}

// TextDelta is a chunk of streaming assistant text.
type TextDelta struct {
	Correlation
	Text string `json:"text"`
}

// ReasoningDelta is a chunk of model chain-of-thought / reasoning text.
// Distinct from TextDelta (the final answer). Frontends may hide it behind a
// toggle; it must never be treated as assistant reply content.
type ReasoningDelta struct {
	Correlation
	Text string `json:"text"`
}

type ToolCallBegin struct {
	Correlation
	CallID string          `json:"callId"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
}

type ToolCallEnd struct {
	Correlation
	CallID  string `json:"callId"`
	Title   string `json:"title"`
	Output  string `json:"output"`
	IsError bool   `json:"isError,omitempty"`
	// ErrorCode is a stable machine code when IsError (canceled, timeout, …).
	// Empty on success. See ErrorCode* constants.
	ErrorCode string `json:"errorCode,omitempty"`
	// Metadata is tool-specific data for rich UI rendering, independent of
	// the model-facing Output.
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// ToolCallOutput is a chunk of streaming tool stdout/stderr while a call runs.
// Frontends append Data in order for a live tail; ToolCallEnd still carries
// the final model-facing Output (including truncation/exit suffixes).
type ToolCallOutput struct {
	Correlation
	CallID string `json:"callId"`
	Data   string `json:"data"`
}

// Process stream labels on ProcessOutput.
const (
	ProcessStreamStdout = "stdout"
	ProcessStreamStderr = "stderr"
)

// ProcessStatus is the terminal outcome of a managed subprocess.
type ProcessStatus string

const (
	ProcessStatusExited   ProcessStatus = "exited"
	ProcessStatusTimeout  ProcessStatus = "timeout"
	ProcessStatusCanceled ProcessStatus = "canceled"
	ProcessStatusError    ProcessStatus = "error"
)

// ProcessStarted marks the beginning of a managed subprocess (bash, hooks, …).
// CallID is set when the process belongs to a tool call.
type ProcessStarted struct {
	Correlation
	ProcessID string   `json:"processId"`
	CallID    string   `json:"callId,omitempty"`
	Argv      []string `json:"argv"`
	Cwd       string   `json:"cwd,omitempty"`
}

// ProcessOutput is a chunk of subprocess stdout or stderr.
type ProcessOutput struct {
	Correlation
	ProcessID string `json:"processId"`
	Stream    string `json:"stream"` // stdout | stderr
	Data      string `json:"data"`
}

// ProcessExited marks the end of a managed subprocess.
type ProcessExited struct {
	Correlation
	ProcessID string        `json:"processId"`
	ExitCode  int           `json:"exitCode"`
	Status    ProcessStatus `json:"status"`
}

// PermissionAsked suspends a tool call until a PermissionReply arrives.
type PermissionAsked struct {
	Correlation
	RequestID  string   `json:"requestId"`
	Permission string   `json:"permission"`
	Patterns   []string `json:"patterns"`
	// Always is the pattern set a DecisionAlways grant should persist.
	// Empty means the grant uses Patterns. Recorded for session resume.
	Always   []string        `json:"always,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type PermissionResolved struct {
	Correlation
	RequestID string   `json:"requestId"`
	Decision  Decision `json:"decision"`
}

// QuestionOption is one selectable choice on a QuestionPrompt.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// QuestionPrompt is one question in a QuestionAsked batch.
type QuestionPrompt struct {
	ID       string           `json:"id"`
	Header   string           `json:"header,omitempty"`
	Question string           `json:"question"`
	Options  []QuestionOption `json:"options,omitempty"`
}

// QuestionAsked suspends a tool call until a QuestionReply arrives.
type QuestionAsked struct {
	Correlation
	RequestID string           `json:"requestId"`
	Questions []QuestionPrompt `json:"questions"`
}

// QuestionResolved closes a pending QuestionAsked (answer, reject, or cancel).
type QuestionResolved struct {
	Correlation
	RequestID string `json:"requestId"`
}

// TurnFileChange is one harness-touched path in a completed turn (create /
// update / delete). Populated on TurnCompleted for timeline/UI; empty when the
// turn made no file tool mutations.
type TurnFileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // create | update | delete
}

type TurnCompleted struct {
	Correlation
	StopReason string `json:"stopReason,omitempty"`
	// Files lists harness edit/write/apply_patch/notebook_edit paths touched
	// this turn with change kind. Omitempty keeps legacy readers happy.
	Files []TurnFileChange `json:"files,omitempty"`
	// Verification is set when solo/harness Options.Verify gates ran for this
	// turn. Claimed reflects a successful model claim (typically stopReason
	// end_turn); Verified/Passed are harness-owned and independent of model
	// prose. Absent when no gates were configured (#806).
	Verification *VerificationReport `json:"verification,omitempty"`
}

// Verification scope labels on VerificationStarted / VerificationCompleted.
const (
	// VerificationScopeTurn is a solo/root (or harness) turn without a child
	// delegation object.
	VerificationScopeTurn = "turn"
	// VerificationScopeChild is a delegated child/subagent completion (#780).
	VerificationScopeChild = "child"
)

// VerificationStarted marks the beginning of independent completion gates.
// Emitted before gates run so timeline/session audit can span start→end (#790).
// Scope is VerificationScopeTurn or VerificationScopeChild.
type VerificationStarted struct {
	Correlation
	Scope     string `json:"scope,omitempty"`
	GateCount int    `json:"gateCount,omitempty"`
}

// VerificationCompleted is the harness-owned outcome of independent gates.
// Report.Claimed vs Report.Verified is the wire distinction between
// claimed_done and verified_done; model self-report text is never evidence.
type VerificationCompleted struct {
	Correlation
	Scope  string             `json:"scope,omitempty"`
	Report VerificationReport `json:"report"`
}

// HarnessProgress is emitted by a custom harness to report intermediate state
// (search tree, scores, iteration count, etc.) during a turn. The Name
// identifies the harness; Payload is harness-specific JSON.
type HarnessProgress struct {
	Correlation
	Name    string          `json:"name"`
	Payload json.RawMessage `json:"payload"`
}

// ModelSelected confirms the active provider/model, at startup (if an
// initial selection succeeded) and after each SelectModel.
type ModelSelected struct {
	Correlation
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// AgentSelected confirms the active agent.
type AgentSelected struct {
	Correlation
	Name string `json:"name"`
}

// Phase recovery statuses on PhaseChanged.Status. Empty Status means the
// phase is active and enforced. Non-empty values are fail-closed resume
// recovery: permissions/context are not applied until stop or restart.
const (
	// PhaseStatusMissing: recorded workflow name is not in the loaded catalog.
	PhaseStatusMissing = "missing"
	// PhaseStatusMismatch: loaded definition fingerprint (or phase identity)
	// does not match the session record.
	PhaseStatusMismatch = "mismatch"
)

// PhaseChanged reports the active workflow phase (permission profile +
// context). Empty Phase means no workflow phase is active.
//
// Source and Fingerprint bind enforcement to the same definition that was
// started (resume fail-closed when the catalog entry is missing or changed).
// Status is empty while healthy; PhaseStatusMissing / PhaseStatusMismatch
// surface recovery without applying phase permissions.
type PhaseChanged struct {
	Correlation
	Workflow    string `json:"workflow,omitempty"`
	Phase       string `json:"phase,omitempty"`
	Index       int    `json:"index,omitempty"`
	Gate        string `json:"gate,omitempty"`        // agent | check | user | skip (effective; from autonomy)
	Source      string `json:"source,omitempty"`      // builtin | global | project | plugin
	Fingerprint string `json:"fingerprint,omitempty"` // canonical SHA-256 of formatted def
	Status      string `json:"status,omitempty"`      // empty | missing | mismatch
}

// Plan approval sources recorded on PlanHandoff.ApprovalSource.
const (
	// PlanApprovalUser: supervised autonomy — human cleared the exit gate.
	PlanApprovalUser = "user"
	// PlanApprovalAgent: agent autonomy — exit_plan_mode was the self-affirmation.
	PlanApprovalAgent = "agent"
	// PlanApprovalChecks: checks autonomy — phase check command passed.
	PlanApprovalChecks = "checks"
	// PlanApprovalSkipAll: skip-all autonomy — approval bypassed (tool perms unchanged).
	PlanApprovalSkipAll = "skip-all"
)

// PlanHandoff records that plan mode handed an exact plan identity (or a
// bounded legacy text plan) to the implementer. Emitted once per successful
// unified approval+handoff. Resume restores this so the implementer still
// sees the approved artifact after --continue.
type PlanHandoff struct {
	Correlation
	// PlanID is the structured plan id when handing off a canonical plan.
	PlanID string `json:"planId,omitempty"`
	// PlanVersion is the CAS version approved at handoff time.
	PlanVersion int `json:"planVersion,omitempty"`
	// ApprovalSource is user|agent|checks|skip-all.
	ApprovalSource string `json:"approvalSource"`
	// Title is a short label (structured plan title or "legacy plan").
	Title string `json:"title,omitempty"`
	// Agent is the implementer routed to (build|orchestrator).
	Agent string `json:"agent,omitempty"`
	// LegacyText is a bounded pre-feature text plan when PlanID is empty.
	// Omitted for structured and skip-all-empty handoffs.
	LegacyText string `json:"legacyText,omitempty"`
}

// PhaseGrantRule is one effective permission widening approved for a phase.
type PhaseGrantRule struct {
	Permission string `json:"permission"`
	Pattern    string `json:"pattern"`
	Action     string `json:"action"` // allow | ask
}

// PhaseGrantApproved records acceptance of workflow phase permission
// widenings (user review or --auto / --dangerously-skip-permissions).
// Resume skips re-prompt when Fingerprint and Grants still match.
type PhaseGrantApproved struct {
	Correlation
	Workflow    string           `json:"workflow"`
	Phase       string           `json:"phase"`
	Index       int              `json:"index"`
	Fingerprint string           `json:"fingerprint"`
	Grants      []PhaseGrantRule `json:"grants"`
	Auto        bool             `json:"auto,omitempty"`
}

// EffortSelected confirms the active reasoning level, at startup and after
// each SetEffort.
type EffortSelected struct {
	Correlation
	Level Effort `json:"level"`
}

// AutonomySelected confirms the session exit-gate policy, at startup and
// after each SetAutonomy.
type AutonomySelected struct {
	Correlation
	Mode Autonomy `json:"mode"`
}

// PermissionModeSelected confirms the session tool-permission posture, at
// startup and after each SetPermissionMode.
type PermissionModeSelected struct {
	Correlation
	Mode PermissionMode `json:"mode"`
}

// FastSelected confirms the session priority-tier preference after SetFast.
type FastSelected struct {
	Correlation
	Enabled bool `json:"enabled"`
}

// FilesInvalidated confirms the engine dropped read snapshots for paths
// reported by a FilesChanged op (external editor, post-edit review, …).
type FilesInvalidated struct {
	Correlation
	Paths  []string `json:"paths"`
	Reason string   `json:"reason,omitempty"`
}

// PathOverlapHolder is one other active claimant on a PathOverlap event.
type PathOverlapHolder struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name,omitempty"`
	Source    string `json:"source,omitempty"` // touch | lease
	Mode      string `json:"mode,omitempty"`   // exclusive | shared (leases)
}

// PathOverlap reports concurrent multi-agent claims on the same path.
// Emitted when a write touch or lease hits another active holder under
// session.overlapPolicy warn|block. Correlation is the claiming session.
type PathOverlap struct {
	Correlation
	Path    string              `json:"path"`
	Policy  string              `json:"policy"` // warn | block
	Blocked bool                `json:"blocked,omitempty"`
	Holders []PathOverlapHolder `json:"holders,omitempty"`
	// Warning is the human/agent-facing message (also appended to tool output).
	Warning string `json:"warning,omitempty"`
}

type EngineError struct {
	Correlation
	Message string `json:"message"`
	// Code is an optional stable machine code (e.g. queue_full). Empty when
	// the error is free-text only.
	Code string `json:"code,omitempty"`
}

// Usage source labels on UsageReported.
const (
	UsageSourceActual    = "actual"
	UsageSourceEstimated = "estimated"
)

// TokenCount distinguishes unknown from zero: Known=false means the count
// was not reported; Known=true with N=0 means the vendor reported zero.
type TokenCount struct {
	N     int  `json:"n,omitempty"`
	Known bool `json:"known"`
}

// KnownTokens is a reported token count (including zero).
func KnownTokens(n int) TokenCount { return TokenCount{N: n, Known: true} }

// UnknownTokens is an absent token count.
func UnknownTokens() TokenCount { return TokenCount{} }

// UsageReported carries token accounting for one completed provider stream.
// Emitted on every EventDone that has usage (including tool-loop intermediate
// streams), correlated to the provider request.
//
// CacheRead/CacheCreation are Known only when the vendor broke out those
// parts (including zero). When only a total is available, Input/Output/cache
// stay unknown and Used carries the total — never fabricate measured zeros.
type UsageReported struct {
	Correlation
	Input         TokenCount `json:"input"`
	Output        TokenCount `json:"output"`
	CacheRead     TokenCount `json:"cacheRead"`
	CacheCreation TokenCount `json:"cacheCreation"`
	// Used is context-window numerator (last request occupancy).
	Used   TokenCount `json:"used"`
	Source string     `json:"source,omitempty"` // actual | estimated
}

// ProviderRetrying announces that a transient provider stream failure will be
// retried with a new attempt identity. Correlation identifies the failed
// attempt; NextAttempt is the 1-based number of the upcoming Stream call.
// Retries only happen at the model boundary — never after tool side effects
// from the failed attempt have committed.
type ProviderRetrying struct {
	Correlation
	NextAttempt int    `json:"nextAttempt"`
	DelayMs     int    `json:"delayMs,omitempty"`
	Message     string `json:"message,omitempty"`
}

// Scheduler cancel reasons on SchedulerCanceled.
const (
	SchedulerReasonCanceled = "canceled"
	SchedulerReasonClosed   = "closed"
)

// SchedulerQueued marks that work is waiting for named pool capacity inside
// this Strike process. RequestID correlates queued → admitted|canceled.
// Pools lists the constrained pool names (no exact queue position — FIFO
// order is internal and not a stable wire guarantee). Label is a short
// human tag (e.g. "model", "bash", "bash:build").
type SchedulerQueued struct {
	Correlation
	RequestID string   `json:"requestId"`
	Pools     []string `json:"pools"`
	Label     string   `json:"label,omitempty"`
}

// SchedulerAdmitted marks that capacity was granted after a SchedulerQueued
// wait. WaitMs is time spent waiting. Engine emitters skip admitted when the
// acquire never blocked (unlimited/free capacity). After SchedulerCanceled for
// the same RequestID, Admitted must not appear.
type SchedulerAdmitted struct {
	Correlation
	RequestID string   `json:"requestId"`
	Pools     []string `json:"pools"`
	Label     string   `json:"label,omitempty"`
	WaitMs    int64    `json:"waitMs,omitempty"`
}

// SchedulerCanceled marks that a waiter left without capacity (context cancel
// or scheduler Close). Clears any queued UI state for RequestID.
type SchedulerCanceled struct {
	Correlation
	RequestID string   `json:"requestId"`
	Pools     []string `json:"pools"`
	Label     string   `json:"label,omitempty"`
	WaitMs    int64    `json:"waitMs,omitempty"`
	Reason    string   `json:"reason,omitempty"` // canceled | closed
}

// Compaction reason labels on CompactionStarted / CompactionCompleted.
const (
	CompactionReasonManual    = "manual"
	CompactionReasonThreshold = "threshold"
	CompactionReasonOverflow  = "overflow"
)

// Compaction strategy labels on CompactionStarted / CompactionCompleted.
const (
	CompactionStrategyTrim      = "trim"
	CompactionStrategySummarize = "summarize"
)

// CompactionStarted announces that model-facing history compaction is about
// to replace older messages. Emitted before the history mutation.
type CompactionStarted struct {
	Correlation
	Reason   string `json:"reason"`             // manual | threshold | overflow
	Strategy string `json:"strategy,omitempty"` // trim | summarize (requested)
}

// CompactionCompleted records that model-facing history was replaced.
// Removed/Kept count provider messages (not transcript events).
// Strategy is the strategy actually applied (may fall back from summarize to trim).
// Summary is the model-authored text when Strategy is summarize (for restore).
type CompactionCompleted struct {
	Correlation
	Reason   string `json:"reason"`
	Strategy string `json:"strategy,omitempty"` // trim | summarize (applied)
	Removed  int    `json:"removed"`
	Kept     int    `json:"kept"`
	Summary  string `json:"summary,omitempty"`
}

// SessionMeta records durable session-level metadata (e.g. a PR opened while
// shipping). Also written to the session sidecar by the host; the event keeps
// the JSONL transcript self-describing.
//
// PRState is open, merged, or closed when known (empty when unset).
type SessionMeta struct {
	Correlation
	PRURL    string `json:"prUrl,omitempty"`
	PRNumber int    `json:"prNumber,omitempty"`
	PRState  string `json:"prState,omitempty"`
}

// SessionRewound records that the last completed user↔assistant turn was
// dropped from model-facing history. Restore applies the same drop so
// --continue stays consistent. File restore is best-effort per path and is
// recorded here; it is not re-applied on JSONL resume (disk already changed).
type SessionRewound struct {
	Correlation
	// Removed is how many provider messages were dropped (0 when unknown).
	Removed int `json:"removed,omitempty"`
	// TurnID is the checkpoint turn id when known (matches the undone turn).
	TurnID string `json:"turnId,omitempty"`
	// RestoreFiles echoes whether the Rewind op requested disk restore.
	RestoreFiles bool `json:"restoreFiles,omitempty"`
	// FilesRestored is how many paths were written back or deleted.
	FilesRestored int `json:"filesRestored,omitempty"`
	// FilesSkipped is how many checkpointed paths could not be restored
	// (oversized/unreadable originals).
	FilesSkipped int `json:"filesSkipped,omitempty"`
}

// HookMatched records that a declarative config hook rule fired (log/block/
// notify). Persisted in the session JSONL for review and notify sinks.
type HookMatched struct {
	Correlation
	Event   string `json:"event"`
	Action  string `json:"action"`
	Matcher string `json:"matcher,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Message string `json:"message,omitempty"`
	// CallID is set for tool lifecycle hooks when a call is in flight.
	CallID string `json:"callId,omitempty"`
}

// Prompt layer kind / mode labels used on EffectivePrompt.Layers.
const (
	PromptLayerShared      = "shared"
	PromptLayerTools       = "tools"
	PromptLayerProvider    = "provider"
	PromptLayerConfig      = "config_system"
	PromptLayerPersona     = "persona"
	PromptLayerPhase       = "phase"
	PromptLayerPlan        = "plan"
	PromptLayerLean        = "lean_code"
	PromptLayerEnvironment = "environment"
	PromptLayerInstruction = "instruction"
	PromptLayerMemory      = "project_memory"

	PromptLayerAppend  = "append"
	PromptLayerReplace = "replace"
)

// PromptLayerInfo is one ordered system-prompt segment with provenance.
// Text content is not included — only kind/source/mode/size and an optional
// redacted preview suitable for logs and the TUI.
type PromptLayerInfo struct {
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	Mode    string `json:"mode"` // append | replace
	Chars   int    `json:"chars"`
	Preview string `json:"preview,omitempty"`
}

// RequestTokenAttribution breaks model-facing input into slices for one
// stream request (or the composition that would be sent next).
//
// Local estimates use ~4 chars/token and set Source to UsageSourceEstimated.
// Providers do not currently report per-slice measured tokens; when they do,
// Source may be UsageSourceActual. Never treat these as billing precision.
type RequestTokenAttribution struct {
	System      TokenCount `json:"system"`
	Tools       TokenCount `json:"tools"`       // tool schemas bound on the request
	Messages    TokenCount `json:"messages"`    // user/assistant text + tool_use (excl. tool_result bodies)
	ToolResults TokenCount `json:"toolResults"` // tool_result bodies (pruned placeholders stay small)
	Total       TokenCount `json:"total"`
	Source      string     `json:"source,omitempty"` // actual | estimated
}

// EffectivePrompt is the inspectable composition of the system prompt for the
// last Stream (or the current composition when no stream has run yet).
type EffectivePrompt struct {
	Correlation
	Layers         []PromptLayerInfo `json:"layers"`
	SystemChars    int               `json:"systemChars"`
	MessageCount   int               `json:"messageCount"`
	FromLastStream bool              `json:"fromLastStream,omitempty"`
	// Attribution is the estimate-labeled request-slice token breakdown
	// (system / tools / messages / tool_results) for the same scope.
	Attribution RequestTokenAttribution `json:"attribution"`
}

// DiagnosticSession is session lineage on a DiagnosticBundle (solo + child).
type DiagnosticSession struct {
	SessionID       string `json:"sessionId,omitempty"`
	ParentSessionID string `json:"parentSessionId,omitempty"`
	RootSessionID   string `json:"rootSessionId,omitempty"`
	Depth           int    `json:"depth"`
	IsChild         bool   `json:"isChild,omitempty"`
}

// DiagnosticPrompt is the ordered layer map section of a DiagnosticBundle.
type DiagnosticPrompt struct {
	Precedence     []string                `json:"precedence"`
	Layers         []PromptLayerInfo       `json:"layers"`
	LayerCount     int                     `json:"layerCount"`
	SystemChars    int                     `json:"systemChars"`
	MessageCount   int                     `json:"messageCount"`
	FromLastStream bool                    `json:"fromLastStream,omitempty"`
	Attribution    RequestTokenAttribution `json:"attribution"`
}

// DiagnosticCompaction holds effective compaction/prune dials on a bundle.
type DiagnosticCompaction struct {
	Strategy           string   `json:"strategy,omitempty"`
	Model              string   `json:"model,omitempty"`
	Threshold          float64  `json:"threshold,omitempty"`
	Buffer             int      `json:"buffer,omitempty"`
	KeepUserTurns      int      `json:"keepUserTurns,omitempty"`
	PruneProtectTokens int      `json:"pruneProtectTokens,omitempty"`
	PruneMinimumTokens int      `json:"pruneMinimumTokens,omitempty"`
	PruneKeepUserTurns int      `json:"pruneKeepUserTurns,omitempty"`
	PruneProtectTools  []string `json:"pruneProtectTools,omitempty"`
}

// DiagnosticScheduler holds scheduler pool limits on a bundle.
type DiagnosticScheduler struct {
	Limits map[string]int `json:"limits,omitempty"`
}

// DiagnosticConfig is the effective dial snapshot + digests (no secrets).
type DiagnosticConfig struct {
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	Agent          string `json:"agent,omitempty"`
	Effort         string `json:"effort,omitempty"`
	Autonomy       string `json:"autonomy,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
	Sandbox        string `json:"sandbox,omitempty"`
	LeanCode       string `json:"leanCode,omitempty"`
	Fast           bool   `json:"fast,omitempty"`
	MaxTokens      int    `json:"maxTokens,omitempty"`
	MaxChildDepth  int    `json:"maxChildDepth,omitempty"`
	ContextWindow  int    `json:"contextWindow,omitempty"`
	WorkDir        string `json:"workDir,omitempty"`
	ProjectRoot    string `json:"projectRoot,omitempty"`

	Compaction DiagnosticCompaction `json:"compaction"`
	Scheduler  DiagnosticScheduler  `json:"scheduler"`
	// Digests maps stable names → hex SHA-256 of canonical non-secret JSON.
	Digests map[string]string `json:"digests,omitempty"`
}

// DiagnosticBundle is the inspectable prompt/config diagnostic document for
// the last Stream (or current composition). Field layout matches pkg/diag.Bundle
// JSON so frontends can export the event payload directly. Never carries raw
// API keys — engine redacts previews and paths before emit.
type DiagnosticBundle struct {
	Correlation
	SchemaVersion   string            `json:"schemaVersion"`
	ProtocolVersion string            `json:"protocolVersion,omitempty"`
	StrikeVersion   string            `json:"strikeVersion,omitempty"`
	ExportedAt      time.Time         `json:"exportedAt"`
	Redacted        bool              `json:"redacted"`
	Note            string            `json:"note,omitempty"`
	Session         DiagnosticSession `json:"session"`
	Prompt          DiagnosticPrompt  `json:"prompt"`
	Config          DiagnosticConfig  `json:"config"`
	Warnings        []string          `json:"warnings,omitempty"`
}

func (UserMessage) isEvent()            {}
func (SessionTitled) isEvent()          {}
func (TurnStarted) isEvent()            {}
func (TextDelta) isEvent()              {}
func (ReasoningDelta) isEvent()         {}
func (ToolCallBegin) isEvent()          {}
func (ToolCallEnd) isEvent()            {}
func (ToolCallOutput) isEvent()         {}
func (ProcessStarted) isEvent()         {}
func (ProcessOutput) isEvent()          {}
func (ProcessExited) isEvent()          {}
func (PermissionAsked) isEvent()        {}
func (PermissionResolved) isEvent()     {}
func (QuestionAsked) isEvent()          {}
func (QuestionResolved) isEvent()       {}
func (TurnCompleted) isEvent()          {}
func (VerificationStarted) isEvent()    {}
func (VerificationCompleted) isEvent()  {}
func (HarnessProgress) isEvent()        {}
func (ModelSelected) isEvent()          {}
func (AgentSelected) isEvent()          {}
func (PhaseChanged) isEvent()           {}
func (PlanHandoff) isEvent()            {}
func (PhaseGrantApproved) isEvent()     {}
func (EffortSelected) isEvent()         {}
func (AutonomySelected) isEvent()       {}
func (PermissionModeSelected) isEvent() {}
func (FastSelected) isEvent()           {}
func (FilesInvalidated) isEvent()       {}
func (PathOverlap) isEvent()            {}
func (EngineError) isEvent()            {}
func (ChildStarted) isEvent()           {}
func (ChildCompleted) isEvent()         {}
func (ChildEscalated) isEvent()         {}
func (DelegationChanged) isEvent()      {}
func (WaitStarted) isEvent()            {}
func (WaitResolved) isEvent()           {}
func (AgentMessage) isEvent()           {}
func (AgentContractTimeout) isEvent()   {}
func (TeamRoster) isEvent()             {}
func (UsageReported) isEvent()          {}
func (ProviderRetrying) isEvent()       {}
func (SchedulerQueued) isEvent()        {}
func (SchedulerAdmitted) isEvent()      {}
func (SchedulerCanceled) isEvent()      {}
func (CompactionStarted) isEvent()      {}
func (CompactionCompleted) isEvent()    {}
func (SessionMeta) isEvent()            {}
func (SessionRewound) isEvent()         {}
func (HookMatched) isEvent()            {}
func (EffectivePrompt) isEvent()        {}
func (DiagnosticBundle) isEvent()       {}
