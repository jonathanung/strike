// Package provider defines the LLM provider abstraction: internal/engine
// and the provider/* adapters (base, anthropic, openaicompat, chatgpt,
// echo) share one normalized stream-event vocabulary regardless of vendor.
// internal/tool imports it only for ToolSchema, the model-facing tool
// declaration. internal/tui never imports it — turn output reaches the
// frontend only as internal/protocol events, already translated by the
// engine.
package provider

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one entry in the conversation history sent to the model.
type Message struct {
	Role Role
	Text string
	// ToolCalls is set on assistant messages that requested tool use.
	ToolCalls []ToolCall
	// ToolResult is set on RoleTool messages.
	ToolResult *ToolResult
	// Reasoning holds the reasoning artifacts this assistant turn produced,
	// verbatim. Anthropic rejects a thinking block whose content was
	// modified, so these are carried as opaque bytes and replayed byte-for-byte
	// rather than parsed and rebuilt. Only the adapter that produced them
	// consumes them; others ignore the field.
	Reasoning []json.RawMessage
}

type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

type ToolResult struct {
	CallID  string
	Output  string
	IsError bool
}

// ToolSchema is the model-facing declaration of a tool.
type ToolSchema struct {
	Name        string
	Description string
	InputSchema json.RawMessage // JSON Schema
}

type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolSchema
	MaxTokens int
	// Effort is the reasoning dial for this request. EffortDefault leaves the
	// provider's own default in place.
	Effort Effort
}

type StreamEventType int

const (
	EventTextDelta StreamEventType = iota
	EventToolCall
	EventReasoning
	EventDone
	EventError
)

// StreamEvent is the normalized event union all providers emit.
type StreamEvent struct {
	Type     StreamEventType
	Text     string    // EventTextDelta
	ToolCall *ToolCall // EventToolCall (complete call)
	// Reasoning is the opaque reasoning artifact for EventReasoning, to be
	// stored on the assistant message and replayed unmodified.
	Reasoning  json.RawMessage
	StopReason string // EventDone
	Err        error  // EventError
}

// Provider streams one model response. The returned channel is closed when
// the response is complete or failed (an EventError precedes close).
type Provider interface {
	Name() string
	Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)
}
