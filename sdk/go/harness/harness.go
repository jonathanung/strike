// Package harness helps Go subprocesses implement Strike's external harness
// protocol. It is separate from internal/fn, which is the embedded Go API
// compiled into Strike itself.
package harness

import (
	"context"
	"encoding/json"
)

// Request is one normalized model request.
type Request struct {
	Model           string       `json:"model"`
	System          string       `json:"system,omitempty"`
	Messages        []Message    `json:"messages"`
	Tools           []ToolSchema `json:"tools,omitempty"`
	MaxOutputTokens int          `json:"maxOutputTokens,omitempty"`
	Effort          string       `json:"effort,omitempty"`
	Priority        bool         `json:"priority,omitempty"`
}

type Message struct {
	Role       string            `json:"role"`
	Text       string            `json:"text,omitempty"`
	Images     []Image           `json:"images,omitempty"`
	ToolCalls  []ToolCall        `json:"toolCalls,omitempty"`
	ToolResult *ToolResult       `json:"toolResult,omitempty"`
	Reasoning  []json.RawMessage `json:"reasoning,omitempty"`
}

type Image struct {
	MIME string `json:"mime"`
	Data []byte `json:"data"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolResult struct {
	CallID    string `json:"callId"`
	Output    string `json:"output"`
	IsError   bool   `json:"isError,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Tools brokers tool execution through the Strike runtime.
type Tools interface {
	Execute(ToolCall) (ToolResult, error)
}

// Input describes one harness invocation independently of the model provider.
type Input struct {
	Context context.Context
	Request Request
	Tools   Tools
}

// ModelResponse is one completed speculative provider call.
type ModelResponse struct {
	Text       string            `json:"text,omitempty"`
	Reasoning  []json.RawMessage `json:"reasoning,omitempty"`
	ToolCalls  []ToolCall        `json:"toolCalls,omitempty"`
	StopReason string            `json:"stopReason,omitempty"`
	Usage      json.RawMessage   `json:"usage,omitempty"`
}

// Result is the only assistant response committed by Strike.
// Tool calls must be executed via Input.Tools during the run, not returned here.
type Result struct {
	Text       string            `json:"text,omitempty"`
	Reasoning  []json.RawMessage `json:"reasoning,omitempty"`
	ToolCalls  []ToolCall        `json:"toolCalls,omitempty"`
	StopReason string            `json:"stopReason,omitempty"`
}

// Provider performs complete Strike-managed model calls.
type Provider interface {
	Call(Request) (ModelResponse, error)
}

// Emit publishes one JSON-serializable progress payload.
type Emit func(any) error

// Func owns one complete harness invocation.
type Func func(Input, Provider, Emit) (Result, error)
