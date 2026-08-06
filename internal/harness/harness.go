// Package harness defines function-based agent control flow. A harness receives
// its input, a model provider capability, a brokered tools capability, and a
// progress callback, owns the complete run, and returns one final result.
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/provider"
)

// Result is the harness's final response. Only this value is committed as the
// assistant message. Tool calls must be executed via Input.Tools during the run;
// returning Calls or stopReason "tool_use" fails the child turn.
type Result struct {
	Text       string
	Calls      []provider.ToolCall
	Reasoning  []json.RawMessage
	StopReason string
}

// ModelResponse is one completed speculative model call. Provider streaming is
// consumed by the engine and does not leak into harness control flow.
type ModelResponse struct {
	Text       string
	Calls      []provider.ToolCall
	Reasoning  []json.RawMessage
	StopReason string
	Usage      *provider.Usage
}

// Tools brokers tool execution through the Strike runtime (registry,
// permissions, hooks, sandbox, scheduler, redaction, protocol events, and
// cancellation). Results are returned to the harness only and are not committed
// to conversation history. Concurrent Execute calls are serialized by the host.
type Tools struct {
	// Execute runs one tool call. Denied, unknown, and malformed calls return a
	// structured ToolResult (IsError + ErrorCode) with a nil error. A non-nil
	// error is reserved for host/transport failure. Execute is nil only when a
	// test omits the capability; engine-hosted runs always set it.
	Execute func(provider.ToolCall) (provider.ToolResult, error)
}

// Input describes one harness invocation independently of the model provider it
// may use.
type Input struct {
	Context context.Context
	Request provider.Request
	Tools   Tools
}

// Provider performs complete model requests without committing their responses
// to conversation history.
type Provider struct {
	Call func(provider.Request) (ModelResponse, error)
}

// Emit publishes a harness-defined progress update.
type Emit func(json.RawMessage)

// Func is the complete agent run. Go applications may register a Func compiled
// into their binary; external.Adapter also exposes subprocesses as Func values.
// The function owns all control flow, may make provider and tool calls
// concurrently, emits optional progress, and returns one result.
type Func func(Input, Provider, Emit) (Result, error)

// Registry maps names to already constructed harness functions. It does not
// discover or load Go code: embedded functions must be registered by the
// composition root, while configured subprocesses are registered through an
// external adapter. The zero value is ready to use and is safe for concurrent
// reads after initial registration.
type Registry struct {
	funcs map[string]Func
}

func NewRegistry() *Registry { return &Registry{} }

// Get returns the function registered under name, or nil when not found.
func (r *Registry) Get(name string) Func {
	if r == nil || r.funcs == nil {
		return nil
	}
	return r.funcs[name]
}

// Register adds or replaces a harness function.
func (r *Registry) Register(name string, fn Func) {
	if err := validateName(name); err != nil {
		panic(err)
	}
	if fn == nil {
		panic("harness: nil function")
	}
	if r.funcs == nil {
		r.funcs = make(map[string]Func)
	}
	r.funcs[name] = fn
}

func validateName(name string) error {
	if name == "" {
		return errors.New("harness: empty name")
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("harness: name %q has leading or trailing whitespace", name)
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("harness: name %q is not valid UTF-8", name)
	}
	for _, r := range name {
		if r <= '\u001f' || r >= '\u007f' && r <= '\u009f' {
			return fmt.Errorf("harness: name %q contains a control character", name)
		}
	}
	return nil
}

// Known returns true when name is registered.
func (r *Registry) Known(name string) bool {
	if r == nil {
		return false
	}
	_, ok := r.funcs[name]
	return ok
}

// Resolve returns the named harness function. The engine handles its built-in
// default loop without representing it as a harness.
func (r *Registry) Resolve(name string) (Func, error) {
	if r == nil {
		return nil, fmt.Errorf("harness: unknown harness %q (no registry)", name)
	}
	fn := r.Get(name)
	if fn == nil {
		return nil, fmt.Errorf("harness: unknown harness %q", name)
	}
	return fn, nil
}
