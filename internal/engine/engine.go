// Package engine is the headless agent runtime: it consumes protocol.Ops,
// runs the model turn loop with tool dispatch, and emits protocol.Events.
// Frontends never call into this package beyond New/Run/Ops/Events.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

const DefaultSystemPrompt = `You are strike, an agentic coding assistant running in a terminal.
You help with software engineering tasks using the tools available to you.
Be concise. Prefer reading and searching before editing. Use the edit tool
for modifications to existing files and write only for new files. When a
tool call is rejected by the user, adjust your approach based on any
feedback instead of retrying the same call.`

// SelectFunc constructs a provider by name, returning the provider, its
// default model, and an error when the name is unknown or credentials are
// missing. The engine starts with no provider; selection happens via the
// SelectModel op (or InitialProvider at startup).
type SelectFunc func(name string) (provider.Provider, string, error)

// Agent is a named persona: a system prompt plus optional provider/model/
// effort pins applied when the agent is selected.
type Agent struct {
	Name        string
	Description string
	Provider    string
	Model       string
	Effort      protocol.Effort
	Prompt      string
}

type Options struct {
	Select   SelectFunc
	Registry *tool.Registry
	WorkDir  string
	// InitialProvider/InitialModel are tried once at startup; failure is
	// silent (the user selects interactively later).
	InitialProvider string
	InitialModel    string
	// InitialEffort is the reasoning dial at startup; the zero value leaves
	// each provider's own default in place.
	InitialEffort protocol.Effort
	// Agents are the selectable personas; the first is the default unless
	// InitialAgent names another.
	Agents       []Agent
	InitialAgent string
	MaxTokens    int
	// Rules are permission ruleset layers, earliest first (later wins).
	Rules []permission.Ruleset
}

type Engine struct {
	opts   Options
	ops    chan protocol.Op
	events chan protocol.Event
	perms  *permission.Service

	prov     provider.Provider
	provName string
	model    string
	effort   protocol.Effort
	agent    Agent
	// priority requests OpenAI service_tier=priority on subsequent turns.
	// Sticky across model switches; adapters that do not support it no-op.
	priority bool
	messages []provider.Message

	turnCancel context.CancelFunc
	turnDone   chan struct{}
}

func New(opts Options) *Engine {
	if len(opts.Agents) == 0 {
		opts.Agents = []Agent{{Name: "build", Description: "general coding agent", Prompt: DefaultSystemPrompt}}
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 8192
	}
	e := &Engine{
		opts:   opts,
		ops:    make(chan protocol.Op, 16),
		events: make(chan protocol.Event, 256),
	}
	e.perms = permission.New(e.emit, opts.Rules...)
	return e
}

func (e *Engine) Ops() chan<- protocol.Op       { return e.ops }
func (e *Engine) Events() <-chan protocol.Event { return e.events }

func (e *Engine) emit(ev protocol.Event) { e.events <- ev }

// Run processes ops until ctx is canceled. Turns run in their own goroutine
// so PermissionReply and Interrupt ops stay responsive mid-turn.
func (e *Engine) Run(ctx context.Context) {
	defer close(e.events)
	if e.opts.InitialProvider != "" && e.opts.Select != nil {
		if p, defaultModel, err := e.opts.Select(e.opts.InitialProvider); err == nil {
			model := e.opts.InitialModel
			if model == "" {
				model = defaultModel
			}
			e.setProvider(e.opts.InitialProvider, p, model)
		}
	}
	// The configured effort is applied before the agent so an agent's own
	// effort pin, if it has one, wins. An unset dial stays silent: there is
	// nothing to confirm, and emitting it would announce "provider default"
	// on every launch.
	if e.opts.InitialEffort != protocol.EffortDefault {
		e.setEffort(e.opts.InitialEffort)
	}
	initialAgent := e.opts.Agents[0].Name
	if e.opts.InitialAgent != "" {
		if _, ok := e.findAgent(e.opts.InitialAgent); ok {
			initialAgent = e.opts.InitialAgent
		}
	}
	e.handleSelectAgent(protocol.SelectAgent{Name: initialAgent})
	for {
		select {
		case <-ctx.Done():
			return
		case op := <-e.ops:
			switch op := op.(type) {
			case protocol.UserInput:
				if e.turnActive() {
					e.emit(protocol.EngineError{Message: "a turn is already running; interrupt it first"})
					continue
				}
				if e.prov == nil {
					e.emit(protocol.EngineError{Message: "no model selected — use /provider <anthropic|openai|xai|echo> [model]"})
					continue
				}
				e.startTurn(ctx, op.Text)
			case protocol.SelectModel:
				e.handleSelect(op)
			case protocol.SelectAgent:
				if e.turnActive() {
					e.emit(protocol.EngineError{Message: "cannot switch agents while a turn is running"})
					continue
				}
				e.handleSelectAgent(op)
			case protocol.SetEffort:
				if e.turnActive() {
					e.emit(protocol.EngineError{Message: "cannot change effort while a turn is running"})
					continue
				}
				e.setEffort(op.Level)
			case protocol.SetFast:
				if e.turnActive() {
					e.emit(protocol.EngineError{Message: "cannot change fast while a turn is running"})
					continue
				}
				e.setFast(op.Enabled)
			case protocol.PermissionReply:
				e.perms.Reply(op)
			case protocol.Interrupt:
				if e.turnCancel != nil {
					e.turnCancel()
				}
			}
		}
	}
}

func (e *Engine) handleSelect(op protocol.SelectModel) {
	if e.turnActive() {
		e.emit(protocol.EngineError{Message: "cannot switch models while a turn is running"})
		return
	}
	if e.opts.Select == nil {
		e.emit(protocol.EngineError{Message: "provider selection is not available"})
		return
	}
	p, defaultModel, err := e.opts.Select(op.Provider)
	if err != nil {
		e.emit(protocol.EngineError{Message: err.Error()})
		return
	}
	model := op.Model
	if model == "" {
		model = defaultModel
	}
	e.setProvider(op.Provider, p, model)
}

func (e *Engine) setProvider(name string, p provider.Provider, model string) {
	e.prov, e.provName, e.model = p, name, model
	e.emit(protocol.ModelSelected{Provider: name, Model: model})
}

// setEffort records the reasoning dial and confirms it. An unrecognized level
// is rejected rather than silently forwarded to a provider that would 400 on
// it.
func (e *Engine) setEffort(level protocol.Effort) {
	parsed, ok := protocol.ParseEffort(string(level))
	if !ok {
		e.emit(protocol.EngineError{Message: fmt.Sprintf("unknown effort %q (want %s)", level, effortNames())})
		return
	}
	e.effort = parsed
	e.emit(protocol.EffortSelected{Level: parsed})
}

func effortNames() string {
	names := make([]string, 0, len(protocol.Efforts()))
	for _, level := range protocol.Efforts() {
		names = append(names, string(level))
	}
	return strings.Join(names, "|")
}

// providerEffort translates the frontend-facing dial into the provider
// vocabulary. The two ladders are kept in lockstep by TestProviderEffortCoversEveryLevel.
func providerEffort(level protocol.Effort) provider.Effort {
	switch level {
	case protocol.EffortOff:
		return provider.EffortOff
	case protocol.EffortLow:
		return provider.EffortLow
	case protocol.EffortMedium:
		return provider.EffortMedium
	case protocol.EffortHigh:
		return provider.EffortHigh
	case protocol.EffortXHigh:
		return provider.EffortXHigh
	case protocol.EffortMax:
		return provider.EffortMax
	default:
		return provider.EffortDefault
	}
}

func (e *Engine) setFast(enabled bool) {
	e.priority = enabled
	e.emit(protocol.FastSelected{Enabled: enabled})
}

func (e *Engine) findAgent(name string) (Agent, bool) {
	for _, a := range e.opts.Agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

// handleSelectAgent switches the active persona and applies its
// provider/model pins when set.
func (e *Engine) handleSelectAgent(op protocol.SelectAgent) {
	agent, ok := e.findAgent(op.Name)
	if !ok {
		e.emit(protocol.EngineError{Message: fmt.Sprintf("unknown agent %q", op.Name)})
		return
	}
	e.agent = agent
	e.emit(protocol.AgentSelected{Name: agent.Name})
	if agent.Effort != protocol.EffortDefault {
		e.setEffort(agent.Effort)
	}
	switch {
	case agent.Provider != "" && e.opts.Select != nil:
		p, defaultModel, err := e.opts.Select(agent.Provider)
		if err != nil {
			e.emit(protocol.EngineError{Message: fmt.Sprintf("agent %s: %v", agent.Name, err)})
			return
		}
		model := agent.Model
		if model == "" {
			model = defaultModel
		}
		e.setProvider(agent.Provider, p, model)
	case agent.Model != "" && e.prov != nil:
		e.model = agent.Model
		e.emit(protocol.ModelSelected{Provider: e.provName, Model: e.model})
	}
}

// system returns the active system prompt.
func (e *Engine) system() string {
	if e.agent.Prompt != "" {
		return e.agent.Prompt
	}
	return DefaultSystemPrompt
}

func (e *Engine) turnActive() bool {
	if e.turnDone == nil {
		return false
	}
	select {
	case <-e.turnDone:
		return false
	default:
		return true
	}
}

func (e *Engine) startTurn(ctx context.Context, text string) {
	turnCtx, cancel := context.WithCancel(ctx)
	e.turnCancel = cancel
	done := make(chan struct{})
	e.turnDone = done
	go func() {
		defer close(done)
		defer cancel()
		e.runTurn(turnCtx, text)
	}()
}

// runTurn is the core agent loop: stream a model response; if it requested
// tool calls, execute them and feed results back; otherwise the turn is done.
func (e *Engine) runTurn(ctx context.Context, text string) {
	e.emit(protocol.UserMessage{Text: text})
	e.emit(protocol.TurnStarted{})
	e.messages = append(e.messages, provider.Message{Role: provider.RoleUser, Text: text})

	for {
		stream, err := e.prov.Stream(ctx, provider.Request{
			Model:     e.model,
			System:    e.system(),
			Messages:  e.messages,
			Tools:     e.opts.Registry.Schemas(),
			MaxTokens: e.opts.MaxTokens,
			Effort:    providerEffort(e.effort),
			Priority:  e.priority,
		})
		if err != nil {
			e.failTurn(err)
			return
		}

		var textBuf strings.Builder
		var calls []provider.ToolCall
		var reasoning []json.RawMessage
		stopReason := ""
		for ev := range stream {
			switch ev.Type {
			case provider.EventTextDelta:
				textBuf.WriteString(ev.Text)
				e.emit(protocol.TextDelta{Text: ev.Text})
			case provider.EventToolCall:
				calls = append(calls, *ev.ToolCall)
			case provider.EventReasoning:
				// Kept on the message but never emitted: reasoning artifacts
				// exist so the next request can replay them, and current
				// models do not return readable chain of thought anyway.
				reasoning = append(reasoning, ev.Reasoning)
			case provider.EventDone:
				stopReason = ev.StopReason
			case provider.EventError:
				e.failTurn(ev.Err)
				return
			}
		}
		if ctx.Err() != nil {
			e.failTurn(ctx.Err())
			return
		}

		e.messages = append(e.messages, provider.Message{
			Role:      provider.RoleAssistant,
			Text:      textBuf.String(),
			ToolCalls: calls,
			Reasoning: reasoning,
		})
		if len(calls) == 0 {
			e.emit(protocol.TurnCompleted{StopReason: stopReason})
			return
		}
		for _, call := range calls {
			e.messages = append(e.messages, e.execToolCall(ctx, call))
			if ctx.Err() != nil {
				e.failTurn(ctx.Err())
				return
			}
		}
	}
}

// execToolCall runs one tool call and returns the tool-result message to
// feed back to the model. Failures (unknown tool, bad args, rejection)
// become correctable error results, never turn aborts.
func (e *Engine) execToolCall(ctx context.Context, call provider.ToolCall) provider.Message {
	e.emit(protocol.ToolCallBegin{CallID: call.ID, Name: call.Name, Args: call.Args})

	var res tool.Result
	var err error
	t, ok := e.opts.Registry.Get(call.Name)
	if !ok {
		err = fmt.Errorf("unknown tool %q; available tools: %s", call.Name, e.toolNames())
	} else {
		tc := &tool.Context{WorkDir: e.opts.WorkDir, Ask: e.perms.Ask}
		res, err = t.Execute(ctx, call.Args, tc)
	}

	output := res.Output
	isError := false
	if err != nil {
		isError = true
		var rejected *permission.RejectedError
		if errors.As(err, &rejected) {
			output = rejected.Error()
		} else {
			output = "Error: " + err.Error()
		}
	}
	e.emit(protocol.ToolCallEnd{
		CallID:   call.ID,
		Title:    res.Title,
		Output:   output,
		IsError:  isError,
		Metadata: res.Metadata,
	})
	return provider.Message{
		Role:       provider.RoleTool,
		ToolResult: &provider.ToolResult{CallID: call.ID, Output: output, IsError: isError},
	}
}

func (e *Engine) failTurn(err error) {
	if errors.Is(err, context.Canceled) {
		e.emit(protocol.TurnCompleted{StopReason: "interrupted"})
		return
	}
	e.emit(protocol.EngineError{Message: err.Error()})
	e.emit(protocol.TurnCompleted{StopReason: "error"})
}

func (e *Engine) toolNames() string {
	var names []string
	for _, s := range e.opts.Registry.Schemas() {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}
