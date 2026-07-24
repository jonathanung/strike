// Package provider defines the LLM provider abstraction. Everything
// downstream (engine, TUI) sees one normalized stream-event vocabulary
// regardless of vendor.
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
}

type StreamEventType int

const (
	EventTextDelta StreamEventType = iota
	EventToolCall
	EventDone
	EventError
)

// StreamEvent is the normalized event union all providers emit.
type StreamEvent struct {
	Type       StreamEventType
	Text       string    // EventTextDelta
	ToolCall   *ToolCall // EventToolCall (complete call)
	StopReason string    // EventDone
	Err        error     // EventError
}

// Provider streams one model response. The returned channel is closed when
// the response is complete or failed (an EventError precedes close).
type Provider interface {
	Name() string
	Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)
}
