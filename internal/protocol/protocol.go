// Package protocol defines the seam between the strike engine and its
// frontends. Frontends submit Ops; the engine emits Events. The TUI and any
// other frontend depend only on this package, never on engine internals.
// The event stream is also the persistence format: a session transcript is a
// JSONL log of events (see internal/session).
package protocol

import (
	"encoding/json"
	"strings"
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
type Autonomy string

const (
	// AutonomySupervised requires a human to clear user gates (safest default).
	AutonomySupervised Autonomy = "supervised"
	// AutonomyAgent lets the agent self-affirm phase completion (phase_done).
	AutonomyAgent Autonomy = "agent"
	// AutonomyChecks advances when configured check commands exit 0.
	AutonomyChecks Autonomy = "checks"
)

// Autonomies lists selectable modes from most to least human oversight.
func Autonomies() []Autonomy {
	return []Autonomy{AutonomySupervised, AutonomyAgent, AutonomyChecks}
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
	default:
		return "sup"
	}
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

// UserInput submits a user message, starting a turn.
type UserInput struct {
	Text string `json:"text"`
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

// SetFast toggles OpenAI priority (fast) service tier for subsequent turns.
// Rejected while a turn is running. Providers and models that do not support
// priority tier ignore the flag silently.
type SetFast struct {
	Enabled bool `json:"enabled"`
}

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

// Rewind removes the last completed user↔assistant turn from model-facing
// history. When RestoreFiles is true, also restores per-file checkpoints
// captured before mutating tools in that turn (never git reset --hard).
// Rejected while a turn is running.
type Rewind struct {
	// RestoreFiles reverts disk changes from the last turn's file checkpoints.
	RestoreFiles bool `json:"restoreFiles,omitempty"`
}

func (UserInput) isOp()              {}
func (PermissionReply) isOp()        {}
func (QuestionReply) isOp()          {}
func (Interrupt) isOp()              {}
func (SelectModel) isOp()            {}
func (SelectAgent) isOp()            {}
func (SetEffort) isOp()              {}
func (SetAutonomy) isOp()            {}
func (SetFast) isOp()                {}
func (FilesChanged) isOp()           {}
func (Compact) isOp()                {}
func (InspectEffectivePrompt) isOp() {}
func (Rewind) isOp()                 {}

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
)

// ChildStarted marks the beginning of a foreground child/subagent session.
// Emitted by the parent engine with the child's correlation.
type ChildStarted struct {
	Correlation
	Agent  string `json:"agent,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

// ChildCompleted marks the end of a foreground child/subagent session.
// Emitted by the parent engine with the child's correlation.
type ChildCompleted struct {
	Correlation
	Status  ChildStatus `json:"status"`
	Summary string      `json:"summary,omitempty"`
}

// UserMessage echoes accepted user input into the event stream so the
// transcript is fully reconstructable from events alone.
type UserMessage struct {
	Correlation
	Text string `json:"text"`
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

type TurnCompleted struct {
	Correlation
	StopReason string `json:"stopReason,omitempty"`
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

// PhaseChanged reports the active workflow phase (permission profile +
// context). Empty Phase means no workflow phase is active.
type PhaseChanged struct {
	Correlation
	Workflow string `json:"workflow,omitempty"`
	Phase    string `json:"phase,omitempty"`
	Index    int    `json:"index,omitempty"`
	Gate     string `json:"gate,omitempty"` // agent | check | user
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

type EngineError struct {
	Correlation
	Message string `json:"message"`
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
	PromptLayerProvider    = "provider"
	PromptLayerConfig      = "config_system"
	PromptLayerPersona     = "persona"
	PromptLayerPhase       = "phase"
	PromptLayerPlan        = "plan"
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

// EffectivePrompt is the inspectable composition of the system prompt for the
// last Stream (or the current composition when no stream has run yet).
type EffectivePrompt struct {
	Correlation
	Layers         []PromptLayerInfo `json:"layers"`
	SystemChars    int               `json:"systemChars"`
	MessageCount   int               `json:"messageCount"`
	FromLastStream bool              `json:"fromLastStream,omitempty"`
}

func (UserMessage) isEvent()         {}
func (SessionTitled) isEvent()       {}
func (TurnStarted) isEvent()         {}
func (TextDelta) isEvent()           {}
func (ReasoningDelta) isEvent()      {}
func (ToolCallBegin) isEvent()       {}
func (ToolCallEnd) isEvent()         {}
func (ToolCallOutput) isEvent()      {}
func (ProcessStarted) isEvent()      {}
func (ProcessOutput) isEvent()       {}
func (ProcessExited) isEvent()       {}
func (PermissionAsked) isEvent()     {}
func (PermissionResolved) isEvent()  {}
func (QuestionAsked) isEvent()       {}
func (QuestionResolved) isEvent()    {}
func (TurnCompleted) isEvent()       {}
func (ModelSelected) isEvent()       {}
func (AgentSelected) isEvent()       {}
func (PhaseChanged) isEvent()        {}
func (EffortSelected) isEvent()      {}
func (AutonomySelected) isEvent()    {}
func (FastSelected) isEvent()        {}
func (FilesInvalidated) isEvent()    {}
func (EngineError) isEvent()         {}
func (ChildStarted) isEvent()        {}
func (ChildCompleted) isEvent()      {}
func (UsageReported) isEvent()       {}
func (ProviderRetrying) isEvent()    {}
func (CompactionStarted) isEvent()   {}
func (CompactionCompleted) isEvent() {}
func (SessionMeta) isEvent()         {}
func (SessionRewound) isEvent()      {}
func (HookMatched) isEvent()         {}
func (EffectivePrompt) isEvent()     {}
