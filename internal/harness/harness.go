// Package harness defines pluggable agent turn-loop controllers. The engine
// dispatches to a named harness after setting up the turn; the harness decides
// how many model streams to run and which tool calls to execute.
package harness

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jonathanung/strike-cli/internal/provider"
)

// Outcome is one successful model stream result.
type Outcome struct {
	Text       string
	Calls      []provider.ToolCall
	Reasoning  []json.RawMessage
	StopReason string
}

// Result is the harness's final turn outcome.
type Result struct {
	Text       string
	Calls      []provider.ToolCall
	Reasoning  []json.RawMessage
	StopReason string
}

// Request carries per-turn callbacks. The engine handles event emission and
// message history; the harness controls the loop.
type Request struct {
	InvocationID string
	Agent        string
	ProviderName string
	Request      provider.Request

	// Provider performs an engine-selected provider request. Implementations may
	// call it concurrently. The engine forces the selected model and applies its
	// normal authentication, retry, usage, and cancellation behavior without
	// committing speculative output to conversation history.
	Provider func(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error)

	// Stream performs one model request with engine-managed retries,
	// compaction recovery, delta emission, and usage reporting. The engine
	// appends the assistant message to history before returning.
	Stream func(ctx context.Context) (Outcome, error)

	// Execute runs one tool call through the full engine pipeline
	// (permission, hooks, cancel, feedback). Uses the correlation from the
	// most recent successful Stream. The engine appends the tool result
	// message to history. Must only be called after a successful Stream.
	Execute func(ctx context.Context, call provider.ToolCall) provider.Message

	// Progress emits a harness-specific progress event to the frontend.
	// Payload is harness-defined JSON.
	Progress func(payload json.RawMessage)
}

// Harness controls one complete agent turn by orchestrating model streams
// and tool execution callbacks. Custom harnesses implement alternative
// strategies like BestOfN or tree search without touching engine internals.
type Harness interface {
	// Name returns the harness identifier used in agent frontmatter
	// (harness: <name>). The only builtin name is "default".
	Name() string

	// Run executes one turn. The harness may call req.Stream zero or more
	// times, and req.Execute for any tool calls it wants to commit. Return
	// nil error and Result with StopReason when the turn is complete; return
	// a non-nil error to abort the turn with an engine error. Context
	// cancellation (via ctx) signals the turn should stop.
	Run(ctx context.Context, req Request) (Result, error)
}

// Func is an ordinary Go harness function. Register it with Registry.RegisterFunc
// when embedding an application directly instead of using the process ABI.
type Func func(context.Context, Request) (Result, error)

type namedFunc struct {
	name string
	fn   Func
}

func (h namedFunc) Name() string { return h.name }

func (h namedFunc) Run(ctx context.Context, req Request) (Result, error) {
	return h.fn(ctx, req)
}

// Registry maps harness names to constructors. The zero value is ready to
// use (no harnesses beyond builtins). Safe for concurrent reads after
// initial registration.
type Registry struct {
	builtins map[string]Harness
}

// NewRegistry returns a registry seeded with the built-in harnesses.
func NewRegistry() *Registry {
	return &Registry{
		builtins: map[string]Harness{
			"default": &DefaultHarness{},
		},
	}
}

// Get returns the harness registered under name, or nil when not found.
func (r *Registry) Get(name string) Harness {
	if r == nil || r.builtins == nil {
		return nil
	}
	return r.builtins[name]
}

// Register adds or replaces a harness. Panics when name is empty.
func (r *Registry) Register(h Harness) {
	if r.builtins == nil {
		r.builtins = make(map[string]Harness)
	}
	name := h.Name()
	if name == "" {
		panic("harness: empty name")
	}
	r.builtins[name] = h
}

func (r *Registry) RegisterFunc(name string, fn Func) {
	if fn == nil {
		panic("harness: nil function")
	}
	if name == "" {
		panic("harness: empty name")
	}
	if name[0] <= ' ' || name[len(name)-1] <= ' ' {
		panic(fmt.Sprintf("harness: invalid name %q", name))
	}
	r.Register(namedFunc{name: name, fn: fn})
}

// Known returns true when name is registered.
func (r *Registry) Known(name string) bool {
	if r == nil {
		return false
	}
	_, ok := r.builtins[name]
	return ok
}

// Resolve returns the harness for name. Empty names and "default" return the
// DefaultHarness. Unknown names return an error.
func (r *Registry) Resolve(name string) (Harness, error) {
	if name == "" || name == "default" {
		if r == nil {
			return &DefaultHarness{}, nil
		}
		if h := r.Get("default"); h != nil {
			return h, nil
		}
		return &DefaultHarness{}, nil
	}
	if r == nil {
		return nil, fmt.Errorf("harness: unknown harness %q (no registry)", name)
	}
	h := r.Get(name)
	if h == nil {
		return nil, fmt.Errorf("harness: unknown harness %q", name)
	}
	return h, nil
}

// BuiltinNames lists built-in harness names.
func BuiltinNames() []string { return []string{"default"} }
