// Package protocol defines the seam between the strike engine and its
// frontends. Frontends submit Ops; the engine emits Events. The TUI and any
// other frontend depend only on this package, never on engine internals.
// The event stream is also the persistence format: a session transcript is a
// JSONL log of events (see internal/session).
package protocol

import "encoding/json"

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

func (UserInput) isOp()       {}
func (PermissionReply) isOp() {}
func (Interrupt) isOp()       {}
func (SelectModel) isOp()     {}
func (SelectAgent) isOp()     {}

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
func (EngineError) isEvent()        {}
