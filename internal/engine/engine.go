// Package engine is the headless agent runtime: it consumes protocol.Ops,
// runs the model turn loop with tool dispatch, and emits protocol.Events.
// Frontends never call into this package beyond New/Run/Ops/Events.
package engine

import (
	"context"
	"crypto/rand"
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

// Model-facing tool-result outputs when a turn is interrupted mid-batch.
const (
	canceledToolOutput  = "Tool call canceled because the turn was interrupted."
	unstartedToolOutput = "Tool call not executed because the turn was interrupted before it started."
)

// SelectFunc constructs a provider by name, returning the provider, its
// default model, and an error when the name is unknown or credentials are
// missing. The engine starts with no provider; selection happens via the
// SelectModel op (or InitialProvider at startup).
type SelectFunc func(name string) (provider.Provider, string, error)

// Agent is a named persona: a system prompt plus optional provider/model
// pins applied when the agent is selected.
type Agent struct {
	Name        string
	Description string
	Provider    string
	Model       string
	Prompt      string
}

type Options struct {
	// SessionID is stamped on every emitted event. Empty falls back to a
	// random ID so standalone engine use still has a stable session key.
	SessionID string
	Select    SelectFunc
	Registry  *tool.Registry
	WorkDir   string
	// InitialProvider/InitialModel are tried once at startup; failure is
	// silent (the user selects interactively later).
	InitialProvider string
	InitialModel    string
	// Agents are the selectable personas; the first is the default unless
	// InitialAgent names another.
	Agents       []Agent
	InitialAgent string
	MaxTokens    int
	// Rules are permission ruleset layers, earliest first (later wins).
	Rules []permission.Ruleset
}

// beginAck reports whether ToolCallBegin was actually written to Events.
// emitted=false means the call never started (no begin/end boundary).
type beginAck struct {
	emitted bool
}

// beginReq asks Run to emit ToolCallBegin while still servicing ops (so
// Interrupt can cancel a turn blocked on a full Events buffer). Turn
// cancellation is not carried on the request: once Run accepts it, begin
// emission proceeds unless the Run parent context ends or Ops closes.
type beginReq struct {
	begin  protocol.ToolCallBegin
	result chan beginAck
}

type Engine struct {
	opts   Options
	ops    chan protocol.Op
	events chan protocol.Event
	perms  *permission.Service

	// beginReqs is served only by Run so Interrupt stays responsive while a
	// worker needs ToolCallBegin emitted into a full Events buffer.
	beginReqs chan beginReq

	prov     provider.Provider
	provName string
	model    string
	agent    Agent
	messages []provider.Message

	// turnCancel/turnDone/turnFinishing are owned exclusively by Run (reap,
	// start, interrupt, shutdown). The worker only closes the done and
	// finishing channels captured at start.
	turnCancel    context.CancelFunc
	turnDone      chan struct{}
	turnFinishing chan struct{} // closed just before terminal TurnCompleted

	// runCtx is Run's parent context, set for the duration of Run. serveBeginReq
	// uses it so parent cancellation can drop an accepted begin without
	// treating turn Interrupt as a failed emission.
	runCtx context.Context
}

func New(opts Options) *Engine {
	if opts.SessionID == "" {
		opts.SessionID = rand.Text()
	}
	if len(opts.Agents) == 0 {
		opts.Agents = []Agent{{Name: "build", Description: "general coding agent", Prompt: DefaultSystemPrompt}}
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 8192
	}
	e := &Engine{
		opts:      opts,
		ops:       make(chan protocol.Op, 16),
		events:    make(chan protocol.Event, 256),
		beginReqs: make(chan beginReq),
	}
	e.perms = permission.New(e.emit, opts.Rules...)
	return e
}

func (e *Engine) Ops() chan<- protocol.Op       { return e.ops }
func (e *Engine) Events() <-chan protocol.Event { return e.events }

func (e *Engine) emit(ev protocol.Event) { e.events <- ev }

// sessionCorr is session-only correlation for selection events and
// rejected ops that never enter a turn.
func (e *Engine) sessionCorr() protocol.Correlation {
	return protocol.Correlation{SessionID: e.opts.SessionID}
}

// Run processes ops until ctx is canceled or Ops is closed. Turns run in
// their own goroutine so PermissionReply and Interrupt ops stay responsive
// mid-turn. On shutdown, Run cancels any active turn and joins it before
// closing Events.
func (e *Engine) Run(ctx context.Context) {
	e.runCtx = ctx
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
	initialAgent := e.opts.Agents[0].Name
	if e.opts.InitialAgent != "" {
		if _, ok := e.findAgent(e.opts.InitialAgent); ok {
			initialAgent = e.opts.InitialAgent
		}
	}
	e.handleSelectAgent(protocol.SelectAgent{Name: initialAgent})
	for {
		e.reapTurn()
		select {
		case <-ctx.Done():
			e.cancelAndJoinTurn()
			return
		case op, ok := <-e.ops:
			if !ok {
				e.cancelAndJoinTurn()
				return
			}
			e.handleOp(ctx, op)
		case req := <-e.beginReqs:
			if !e.serveBeginReq(req) {
				e.cancelAndJoinTurn()
				return
			}
		}
	}
}

func (e *Engine) handleOp(ctx context.Context, op protocol.Op) {
	// If the worker already emitted terminal TurnCompleted, join it before
	// applying the op so a follow-up UserInput is not rejected as active-turn.
	e.joinFinishingTurn()
	switch op := op.(type) {
	case protocol.UserInput:
		if e.turnActive() {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "a turn is already running; interrupt it first",
			})
			return
		}
		if e.prov == nil {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "no model selected — use /provider <anthropic|openai|xai|echo> [model]",
			})
			return
		}
		e.startTurn(ctx, op.Text)
	case protocol.SelectModel:
		e.handleSelect(op)
	case protocol.SelectAgent:
		if e.turnActive() {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "cannot switch agents while a turn is running",
			})
			return
		}
		e.handleSelectAgent(op)
	case protocol.PermissionReply:
		e.perms.Reply(op)
	case protocol.Interrupt:
		if e.turnCancel != nil {
			e.turnCancel()
		}
	}
}

// serveBeginReq emits ToolCallBegin from Run so ops (Interrupt) stay
// serviceable while the Events buffer is full. The ack reports whether begin
// was actually emitted. Returns false when the Run parent context is done or
// Ops has been closed (Run should cancel, join, and exit). Those paths
// acknowledge emitted=false and write no begin.
//
// After Run accepts a begin request, turn cancellation (normal Interrupt)
// does not suppress emission: Interrupt is applied while blocked, begin is
// still written when Events has capacity, and the worker's post-ack ctx check
// skips Execute and emits the matched canceled ToolCallEnd.
//
// Queued ops are drained before each non-blocking emission attempt. After a
// successful write, any ops that raced with the send are drained before the
// worker is released. Parent cancellation is observed via e.runCtx (set by Run).
func (e *Engine) serveBeginReq(req beginReq) (opsOpen bool) {
	runDone := e.runDone()
	for {
		if e.runCtx != nil && e.runCtx.Err() != nil {
			if e.turnCancel != nil {
				e.turnCancel()
			}
			req.result <- beginAck{emitted: false}
			return false
		}
		// Service queued ops before attempting emission.
		select {
		case op, ok := <-e.ops:
			if !ok {
				if e.turnCancel != nil {
					e.turnCancel()
				}
				req.result <- beginAck{emitted: false}
				return false
			}
			e.handleOp(context.Background(), op)
			continue
		default:
		}
		// Try begin without racing Ops in the same select.
		select {
		case e.events <- req.begin:
			return e.ackBeginEmitted(req)
		default:
		}
		// Buffer full: wait for ops, parent cancel, or Events capacity.
		select {
		case op, ok := <-e.ops:
			if !ok {
				if e.turnCancel != nil {
					e.turnCancel()
				}
				req.result <- beginAck{emitted: false}
				return false
			}
			e.handleOp(context.Background(), op)
		case <-runDone:
			if e.turnCancel != nil {
				e.turnCancel()
			}
			req.result <- beginAck{emitted: false}
			return false
		case e.events <- req.begin:
			return e.ackBeginEmitted(req)
		}
	}
}

// runDone returns a channel closed when Run's parent context is canceled.
// When Run is not active (unit tests calling serveBeginReq directly), it
// returns nil so the select case is never ready.
func (e *Engine) runDone() <-chan struct{} {
	if e.runCtx == nil {
		return nil
	}
	return e.runCtx.Done()
}

// ackBeginEmitted acknowledges a successful ToolCallBegin write. Ops that
// became ready in a race with the emit are drained before the worker proceeds
// so Interrupt cancels the turn before Execute.
func (e *Engine) ackBeginEmitted(req beginReq) (opsOpen bool) {
	opsOpen = e.drainOps()
	req.result <- beginAck{emitted: true}
	return opsOpen
}

// drainOps applies all currently queued ops without blocking. Returns false
// when Ops has been closed; the active turn is canceled immediately so the
// worker observes shutdown before Execute.
func (e *Engine) drainOps() (opsOpen bool) {
	for {
		select {
		case op, ok := <-e.ops:
			if !ok {
				if e.turnCancel != nil {
					e.turnCancel()
				}
				return false
			}
			e.handleOp(context.Background(), op)
		default:
			return true
		}
	}
}

// reapTurn clears finished turn lifecycle fields. Nonblocking; only Run writes
// turnCancel/turnDone/turnFinishing.
func (e *Engine) reapTurn() {
	if e.turnDone == nil {
		return
	}
	select {
	case <-e.turnDone:
		e.clearTurn()
	default:
	}
}

// joinFinishingTurn blocks when the active turn has already closed its
// finishing channel (terminal TurnCompleted is emitted or about to be) until
// the worker exits, then clears lifecycle fields. No-op while the turn is
// still running or when no turn is active.
func (e *Engine) joinFinishingTurn() {
	if e.turnFinishing == nil {
		return
	}
	select {
	case <-e.turnFinishing:
	default:
		return
	}
	if e.turnDone != nil {
		<-e.turnDone
	}
	e.clearTurn()
}

// clearTurn nils Run-owned turn lifecycle fields. Caller must only invoke
// after the worker is known to have exited (turnDone received) or when no
// turn was started.
func (e *Engine) clearTurn() {
	e.turnDone = nil
	e.turnCancel = nil
	e.turnFinishing = nil
}

// cancelAndJoinTurn cancels any active turn, waits for its worker to finish,
// then clears lifecycle fields. Used on Run shutdown only. Pending beginReqs
// are acknowledged as emitted=false (no best-effort partial begin).
func (e *Engine) cancelAndJoinTurn() {
	if e.turnCancel != nil {
		e.turnCancel()
	}
	if e.turnDone == nil {
		return
	}
	for {
		select {
		case <-e.turnDone:
			e.clearTurn()
			return
		case req := <-e.beginReqs:
			req.result <- beginAck{emitted: false}
		}
	}
}

func (e *Engine) handleSelect(op protocol.SelectModel) {
	if e.turnActive() {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "cannot switch models while a turn is running",
		})
		return
	}
	if e.opts.Select == nil {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "provider selection is not available",
		})
		return
	}
	p, defaultModel, err := e.opts.Select(op.Provider)
	if err != nil {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     err.Error(),
		})
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
	e.emit(protocol.ModelSelected{
		Correlation: e.sessionCorr(),
		Provider:    name,
		Model:       model,
	})
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
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     fmt.Sprintf("unknown agent %q", op.Name),
		})
		return
	}
	e.agent = agent
	e.emit(protocol.AgentSelected{
		Correlation: e.sessionCorr(),
		Name:        agent.Name,
	})
	switch {
	case agent.Provider != "" && e.opts.Select != nil:
		p, defaultModel, err := e.opts.Select(agent.Provider)
		if err != nil {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     fmt.Sprintf("agent %s: %v", agent.Name, err),
			})
			return
		}
		model := agent.Model
		if model == "" {
			model = defaultModel
		}
		e.setProvider(agent.Provider, p, model)
	case agent.Model != "" && e.prov != nil:
		e.model = agent.Model
		e.emit(protocol.ModelSelected{
			Correlation: e.sessionCorr(),
			Provider:    e.provName,
			Model:       e.model,
		})
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
	// Mint turn ID only after input acceptance (provider present, no active turn).
	turnID := rand.Text()
	turnCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	finishing := make(chan struct{})
	e.turnCancel = cancel
	e.turnDone = done
	e.turnFinishing = finishing
	go func() {
		defer close(done)
		defer cancel()
		e.runTurn(turnCtx, text, turnID, finishing)
	}()
}

// runTurn is the core agent loop: stream a model response; if it requested
// tool calls, execute them and feed results back; otherwise the turn is done.
// turnID is immutable for the turn; each Provider.Stream call gets its own
// provider-request ID passed as local values (no engine-wide mutable state).
// finishing is closed exactly once immediately before the terminal
// TurnCompleted emission so Run can join the worker before the next op.
func (e *Engine) runTurn(ctx context.Context, text string, turnID string, finishing chan struct{}) {
	turnCorr := protocol.Correlation{SessionID: e.opts.SessionID, TurnID: turnID}
	e.emit(protocol.UserMessage{Correlation: turnCorr, Text: text})
	e.emit(protocol.TurnStarted{Correlation: turnCorr})
	e.messages = append(e.messages, provider.Message{Role: provider.RoleUser, Text: text})

	for {
		// Distinct provider-request ID immediately before every Stream call.
		reqCorr := protocol.Correlation{
			SessionID:         e.opts.SessionID,
			TurnID:            turnID,
			ProviderRequestID: rand.Text(),
		}
		stream, err := e.prov.Stream(ctx, provider.Request{
			Model:     e.model,
			System:    e.system(),
			Messages:  e.messages,
			Tools:     e.opts.Registry.Schemas(),
			MaxTokens: e.opts.MaxTokens,
		})
		if err != nil {
			e.failTurn(err, reqCorr, finishing)
			return
		}

		var textBuf strings.Builder
		var calls []provider.ToolCall
		stopReason := ""
		for ev := range stream {
			switch ev.Type {
			case provider.EventTextDelta:
				textBuf.WriteString(ev.Text)
				e.emit(protocol.TextDelta{Correlation: reqCorr, Text: ev.Text})
			case provider.EventToolCall:
				calls = append(calls, *ev.ToolCall)
			case provider.EventDone:
				stopReason = ev.StopReason
			case provider.EventError:
				e.failTurn(ev.Err, reqCorr, finishing)
				return
			}
		}
		if ctx.Err() != nil {
			e.failTurn(ctx.Err(), reqCorr, finishing)
			return
		}

		e.messages = append(e.messages, provider.Message{
			Role:      provider.RoleAssistant,
			Text:      textBuf.String(),
			ToolCalls: calls,
		})
		if len(calls) == 0 {
			e.completeTurn(finishing, reqCorr, stopReason)
			return
		}
		for i, call := range calls {
			// Unstarted calls: history-only synthetic results, no begin/end/Execute.
			if ctx.Err() != nil {
				e.appendUnstartedToolResults(calls[i:])
				e.failTurn(ctx.Err(), reqCorr, finishing)
				return
			}
			e.messages = append(e.messages, e.execToolCall(ctx, call, reqCorr))
			if ctx.Err() != nil {
				// Current call was started (and canceled); remaining are unstarted.
				e.appendUnstartedToolResults(calls[i+1:])
				e.failTurn(ctx.Err(), reqCorr, finishing)
				return
			}
		}
	}
}

// appendUnstartedToolResults adds synthetic RoleTool error results for calls
// that never began execution after a turn interrupt.
func (e *Engine) appendUnstartedToolResults(calls []provider.ToolCall) {
	for _, call := range calls {
		e.messages = append(e.messages, toolResultMessage(call.ID, unstartedToolOutput, true))
	}
}

func toolResultMessage(callID, output string, isError bool) provider.Message {
	return provider.Message{
		Role:       provider.RoleTool,
		ToolResult: &provider.ToolResult{CallID: callID, Output: output, IsError: isError},
	}
}

// execToolCall runs one tool call and returns the tool-result message to
// feed back to the model. Failures (unknown tool, bad args, rejection)
// become correctable error results, never turn aborts. Cancellation after
// ToolCallBegin yields one correlated ToolCallEnd with a deterministic
// canceled output; it does not invent PermissionResolved. If begin was
// never emitted, only a history-only unstarted result is returned.
func (e *Engine) execToolCall(ctx context.Context, call provider.ToolCall, corr protocol.Correlation) provider.Message {
	begin := protocol.ToolCallBegin{
		Correlation: corr,
		CallID:      call.ID,
		Name:        call.Name,
		Args:        call.Args,
	}
	// Ask Run to emit begin so Interrupt can be applied while Events is full.
	result := make(chan beginAck, 1)
	select {
	case e.beginReqs <- beginReq{begin: begin, result: result}:
	case <-ctx.Done():
		// Canceled before Run accepted the begin request — unstarted.
		return toolResultMessage(call.ID, unstartedToolOutput, true)
	}
	ack := <-result
	if !ack.emitted {
		return toolResultMessage(call.ID, unstartedToolOutput, true)
	}
	// Begin was emitted. Pre-Execute cancel/shutdown check (no Execute).
	if ctx.Err() != nil {
		return e.canceledToolResult(call.ID, corr)
	}

	var res tool.Result
	var err error
	t, ok := e.opts.Registry.Get(call.Name)
	if !ok {
		err = fmt.Errorf("unknown tool %q; available tools: %s", call.Name, e.toolNames())
	} else {
		tc := &tool.Context{
			WorkDir: e.opts.WorkDir,
			Ask: func(ctx context.Context, req tool.AskRequest) error {
				return e.perms.AskWithCorrelation(ctx, req, corr)
			},
		}
		res, err = t.Execute(ctx, call.Args, tc)
	}

	// Normalize cancellation after Execute (including permission-wait cancel).
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return e.canceledToolResult(call.ID, corr)
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
		Correlation: corr,
		CallID:      call.ID,
		Title:       res.Title,
		Output:      output,
		IsError:     isError,
		Metadata:    res.Metadata,
	})
	return toolResultMessage(call.ID, output, isError)
}

func (e *Engine) canceledToolResult(callID string, corr protocol.Correlation) provider.Message {
	e.emit(protocol.ToolCallEnd{
		Correlation: corr,
		CallID:      callID,
		Output:      canceledToolOutput,
		IsError:     true,
	})
	return toolResultMessage(callID, canceledToolOutput, true)
}

func (e *Engine) failTurn(err error, corr protocol.Correlation, finishing chan struct{}) {
	if errors.Is(err, context.Canceled) {
		e.completeTurn(finishing, corr, "interrupted")
		return
	}
	e.emit(protocol.EngineError{Correlation: corr, Message: err.Error()})
	e.completeTurn(finishing, corr, "error")
}

// completeTurn closes finishing then emits the terminal TurnCompleted. Call
// only once per turn, after all history mutations and any preceding EngineError.
func (e *Engine) completeTurn(finishing chan struct{}, corr protocol.Correlation, stopReason string) {
	close(finishing)
	e.emit(protocol.TurnCompleted{Correlation: corr, StopReason: stopReason})
}

func (e *Engine) toolNames() string {
	var names []string
	for _, s := range e.opts.Registry.Schemas() {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}
