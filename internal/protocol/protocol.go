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

func (UserInput) isOp()       {}
func (PermissionReply) isOp() {}
func (Interrupt) isOp()       {}
func (SelectModel) isOp()     {}
func (SelectAgent) isOp()     {}
func (SetEffort) isOp()       {}

// Event is an engine -> client notification.
type Event interface{ isEvent() }

// UserMessage echoes accepted user input into the event stream so the
// transcript is fully reconstructable from events alone.
type UserMessage struct {
	Text string `json:"text"`
}

type TurnStarted struct{}

// TextDelta is a chunk of streaming assistant text.
type TextDelta struct {
	Text string `json:"text"`
}

type ToolCallBegin struct {
	CallID string          `json:"callId"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
}

type ToolCallEnd struct {
	CallID  string `json:"callId"`
	Title   string `json:"title"`
	Output  string `json:"output"`
	IsError bool   `json:"isError,omitempty"`
	// Metadata is tool-specific data for rich UI rendering, independent of
	// the model-facing Output.
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// PermissionAsked suspends a tool call until a PermissionReply arrives.
type PermissionAsked struct {
	RequestID  string          `json:"requestId"`
	Permission string          `json:"permission"`
	Patterns   []string        `json:"patterns"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

type PermissionResolved struct {
	RequestID string   `json:"requestId"`
	Decision  Decision `json:"decision"`
}

type TurnCompleted struct {
	StopReason string `json:"stopReason,omitempty"`
}

// ModelSelected confirms the active provider/model, at startup (if an
// initial selection succeeded) and after each SelectModel.
type ModelSelected struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// AgentSelected confirms the active agent.
type AgentSelected struct {
	Name string `json:"name"`
}

// EffortSelected confirms the active reasoning level, at startup and after
// each SetEffort.
type EffortSelected struct {
	Level Effort `json:"level"`
}

type EngineError struct {
	Message string `json:"message"`
}

func (UserMessage) isEvent()        {}
func (TurnStarted) isEvent()        {}
func (TextDelta) isEvent()          {}
func (ToolCallBegin) isEvent()      {}
func (ToolCallEnd) isEvent()        {}
func (PermissionAsked) isEvent()    {}
func (PermissionResolved) isEvent() {}
func (TurnCompleted) isEvent()      {}
func (ModelSelected) isEvent()      {}
func (AgentSelected) isEvent()      {}
func (EffortSelected) isEvent()     {}
func (EngineError) isEvent()        {}
