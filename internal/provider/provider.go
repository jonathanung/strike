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
	// Priority requests the provider's accelerated service tier when one
	// exists (OpenAI platform service_tier=priority). Adapters that do not
	// support it ignore the flag.
	Priority bool
}

type StreamEventType int

const (
	EventTextDelta StreamEventType = iota
	EventToolCall
	EventReasoning
	EventDone
	EventError
)

// Usage is token accounting for one completed provider request.
// A nil *Usage on StreamEvent means the vendor did not report usage (unknown).
// Zero token fields with a non-nil Usage mean the vendor reported zero.
//
// Context occupancy (used) is computed by the engine as:
//
//	used = InputTokens + CacheReadTokens + CacheCreationTokens + OutputTokens
//	// if all those are 0 but TotalTokens > 0, used = TotalTokens
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
	// TotalTokens if the vendor supplied it; 0 means not supplied.
	TotalTokens int
	// Estimated is true only for synthetic estimates (echo). Real providers
	// never set this — missing vendor usage stays nil, not fabricated.
	Estimated bool
}

// StreamEvent is the normalized event union all providers emit.
//
// Terminal contract (enforced by NormalizeStream / base.Stream):
//   - Exactly one terminal event per stream: EventDone (success) or
//     EventError (failure), then the channel closes.
//   - Non-terminal events (text, tool call, reasoning) may only precede the
//     terminal event. Duplicates after the first terminal are dropped.
//   - Closing without a terminal event is normalized to EventError with
//     ErrIncompleteStream so consumers never hang on an open-ended stream.
type StreamEvent struct {
	Type     StreamEventType
	Text     string    // EventTextDelta
	ToolCall *ToolCall // EventToolCall (complete call)
	// Reasoning is the opaque reasoning artifact for EventReasoning, to be
	// stored on the assistant message and replayed unmodified.
	Reasoning  json.RawMessage
	StopReason string // EventDone
	// Usage is set on EventDone when the vendor reported token counts.
	// nil means unknown (vendor omitted usage); never fabricate for real providers.
	Usage *Usage
	Err   error // EventError
}

// Provider streams one model response. Stream returns a channel that obeys
// the terminal contract above (adapters should use base.Stream or
// NormalizeStream). A non-nil error means the attempt never started — no
// events will be delivered. Each Stream call is one attempt identity; the
// engine mints a fresh provider-request ID (and attempt number) per call and
// may retry only before any tool side effects from that attempt commit.
type Provider interface {
	Name() string
	Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)
}
