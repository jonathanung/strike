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

// Decision is a user's answer to a permission ask.
type Decision string

const (
	DecisionOnce   Decision = "once"
	DecisionAlways Decision = "always"
	DecisionReject Decision = "reject"
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

func (UserInput) isOp()       {}
func (PermissionReply) isOp() {}
func (QuestionReply) isOp()   {}
func (Interrupt) isOp()       {}
func (SelectModel) isOp()     {}
func (SelectAgent) isOp()     {}
func (SetEffort) isOp()       {}
func (SetFast) isOp()         {}
func (FilesChanged) isOp()    {}

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

type TurnStarted struct {
	Correlation
}

// TextDelta is a chunk of streaming assistant text.
type TextDelta struct {
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

// PermissionAsked suspends a tool call until a PermissionReply arrives.
type PermissionAsked struct {
	Correlation
	RequestID  string          `json:"requestId"`
	Permission string          `json:"permission"`
	Patterns   []string        `json:"patterns"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
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

// EffortSelected confirms the active reasoning level, at startup and after
// each SetEffort.
type EffortSelected struct {
	Correlation
	Level Effort `json:"level"`
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
type UsageReported struct {
	Correlation
	Input  TokenCount `json:"input"`
	Output TokenCount `json:"output"`
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

func (UserMessage) isEvent()        {}
func (TurnStarted) isEvent()        {}
func (TextDelta) isEvent()          {}
func (ToolCallBegin) isEvent()      {}
func (ToolCallEnd) isEvent()        {}
func (ToolCallOutput) isEvent()     {}
func (PermissionAsked) isEvent()    {}
func (PermissionResolved) isEvent() {}
func (QuestionAsked) isEvent()      {}
func (QuestionResolved) isEvent()   {}
func (TurnCompleted) isEvent()      {}
func (ModelSelected) isEvent()      {}
func (AgentSelected) isEvent()      {}
func (EffortSelected) isEvent()     {}
func (FastSelected) isEvent()       {}
func (FilesInvalidated) isEvent()   {}
func (EngineError) isEvent()        {}
func (ChildStarted) isEvent()       {}
func (ChildCompleted) isEvent()     {}
func (UsageReported) isEvent()      {}
func (ProviderRetrying) isEvent()   {}
