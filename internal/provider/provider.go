// Package provider defines the LLM provider abstraction: internal/engine
// and the provider/* adapters (base, anthropic, openaicompat, chatgpt,
// google, echo) share one normalized stream-event vocabulary regardless of vendor.
// internal/tool imports it only for ToolSchema, the model-facing tool
// declaration. internal/tui never imports it — turn output reaches the
// frontend only as internal/protocol events, already translated by the
// engine.
package provider

import (
	"context"
	"encoding/json"
	"strings"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Image is one user-attached image for multimodal requests.
type Image struct {
	MIME string // e.g. image/png
	Data []byte // raw bytes (adapters base64-encode for the wire)
}

// Message is one entry in the conversation history sent to the model.
type Message struct {
	Role Role
	Text string
	// Images are optional multimodal parts on RoleUser messages.
	Images []Image
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
	// ErrorCode is a stable machine code when IsError (e.g. permission_denied).
	// Empty when unknown or on success. Adapters that only send text should
	// still include Output; orchestrators may read ErrorCode directly.
	ErrorCode string
	// Retryable is meaningful when IsError and ErrorCode is set.
	Retryable bool
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
	// CacheKey is an optional stable key for provider prompt-cache routing
	// (OpenAI/xAI prompt_cache_key). Empty omits the wire field. Engine sets
	// this to the session id so turns in one session share cache affinity.
	CacheKey string
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
	Type StreamEventType
	// Text is streaming prose: EventTextDelta (final answer) or optional
	// displayable chain-of-thought on EventReasoning. When EventReasoning
	// leaves Text empty, consumers may try ReasoningText(Reasoning).
	Text     string
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

// ReasoningText extracts human-readable chain-of-thought from an opaque
// EventReasoning payload when the vendor embeds plain prose (Anthropic
// thinking blocks, summary objects). Empty for redacted or binary-only
// artifacts.
func ReasoningText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Plain JSON string.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var block struct {
		Type     string `json:"type"`
		Thinking string `json:"thinking"`
		Text     string `json:"text"`
		Summary  string `json:"summary"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return ""
	}
	switch {
	case strings.TrimSpace(block.Thinking) != "":
		return strings.TrimSpace(block.Thinking)
	case strings.TrimSpace(block.Text) != "":
		return strings.TrimSpace(block.Text)
	case strings.TrimSpace(block.Summary) != "":
		return strings.TrimSpace(block.Summary)
	case strings.TrimSpace(block.Content) != "":
		return strings.TrimSpace(block.Content)
	default:
		return ""
	}
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
