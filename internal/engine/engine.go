// Package engine is the headless agent runtime: it consumes protocol.Ops,
// runs the model turn loop with tool dispatch, and emits protocol.Events.
// Frontends never call into this package beyond New/Run/Ops/Events.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/question"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// defaultMaxStreamAttempts is how many times one logical model request may
// call Provider.Stream on retryable failure before the turn fails.
const defaultMaxStreamAttempts = 3

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

// Agent is a named persona: a system prompt plus optional provider/model/
// effort pins and a permission profile applied when the agent is selected.
type Agent struct {
	Name        string
	Description string
	Provider    string
	Model       string
	Effort      protocol.Effort
	Prompt      string
	Permissions permission.Ruleset
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
	// InitialEffort is the reasoning dial at startup; the zero value leaves
	// each provider's own default in place.
	InitialEffort protocol.Effort
	// Agents are the selectable personas; the first is the default unless
	// InitialAgent names another.
	Agents       []Agent
	InitialAgent string
	MaxTokens    int
	// MaxStreamAttempts bounds provider Stream retries on transient failure
	// for one logical model request (tool-loop iteration). Zero defaults to
	// 3; set to 1 to disable retries. Retries mint a new attempt identity and
	// never re-run tools already completed for a prior successful stream.
	MaxStreamAttempts int
	// StreamRetryBackoff returns the wait before starting nextAttempt
	// (1-based, >=2). nil uses a small exponential default. Tests may return
	// 0 for instant retries.
	StreamRetryBackoff func(nextAttempt int) time.Duration
	// ProjectRoot is the workspace root (often the git toplevel). Shown in
	// the environment system-prompt layer; empty falls back to WorkDir.
	ProjectRoot string
	// Instructions are preloaded AGENTS.md/CLAUDE.md blocks appended after
	// the environment layer (see config.LoadInstructions).
	Instructions []string
	// SystemPrompt, when set, replaces the provider overlay for the build
	// agent only (shared baseline still applies). From config systemPrompt.
	SystemPrompt string
	// Rules are permission ruleset layers, earliest first (later wins).
	Rules []permission.Ruleset
	// MaxChildDepth bounds foreground task nesting. Zero defaults to 1 in New
	// (root depth 0 may spawn one child; that child may not spawn further).
	MaxChildDepth int
	// Depth is this engine's lineage depth (0 = root).
	Depth int
	// ParentSessionID is the spawning session's ID; empty on root engines.
	ParentSessionID string
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
	opts      Options
	ops       chan protocol.Op
	events    chan protocol.Event
	perms     *permission.Service
	questions *question.Service

	// beginReqs is served only by Run so Interrupt stays responsive while a
	// worker needs ToolCallBegin emitted into a full Events buffer.
	beginReqs chan beginReq

	prov     provider.Provider
	provName string
	model    string
	effort   protocol.Effort
	agent    Agent
	// priority requests OpenAI service_tier=priority on subsequent turns.
	// Sticky across model switches; adapters that do not support it no-op.
	priority bool
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

	// activeChildReply / activeChildQuestionReply route replies to a
	// foreground child's services while spawnChild is in flight. Parent and
	// child mint overlapping perm_N / q_N IDs, so dual-Reply must never happen.
	childMu                  sync.Mutex
	activeChildReply         func(protocol.PermissionReply)
	activeChildQuestionReply func(protocol.QuestionReply)
	activeChildOps           chan<- protocol.Op

	// pendingAgent is set by tools via SwitchAgent and applied after each tool
	// batch (so the next Stream sees the new agent/prompt) and again in
	// completeTurn if anything remains when the turn ends.
	pendingAgentMu sync.Mutex
	pendingAgent   string

	// files tracks tool read snapshots so external edits (FilesChanged / /vim)
	// force the model to re-read before edit/write.
	files *tool.FileState
}

func New(opts Options) *Engine {
	if opts.SessionID == "" {
		opts.SessionID = rand.Text()
	}
	if len(opts.Agents) == 0 {
		opts.Agents = []Agent{{Name: "build", Description: "general coding agent"}}
	}
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 8192
	}
	if opts.MaxStreamAttempts == 0 {
		opts.MaxStreamAttempts = defaultMaxStreamAttempts
	}
	if opts.MaxChildDepth == 0 {
		opts.MaxChildDepth = 1
	}
	e := &Engine{
		opts:      opts,
		ops:       make(chan protocol.Op, 16),
		events:    make(chan protocol.Event, 256),
		beginReqs: make(chan beginReq),
		files:     &tool.FileState{},
	}
	e.perms = permission.New(e.emit, opts.Rules...)
	e.questions = question.New(e.emit)
	return e
}

func (e *Engine) Ops() chan<- protocol.Op       { return e.ops }
func (e *Engine) Events() <-chan protocol.Event { return e.events }

func (e *Engine) emit(ev protocol.Event) { e.events <- ev }

// baseCorr is the immutable session lineage stamped on every event.
func (e *Engine) baseCorr() protocol.Correlation {
	return protocol.Correlation{
		SessionID:       e.opts.SessionID,
		ParentSessionID: e.opts.ParentSessionID,
		Depth:           e.opts.Depth,
	}
}

// sessionCorr is session-only correlation for selection events and
// rejected ops that never enter a turn.
func (e *Engine) sessionCorr() protocol.Correlation {
	return e.baseCorr()
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
			// Same normalization as SelectModel: matching "provider/id" → bare
			// id; foreign prefixes → provider default. Bare ids pass through
			// unchanged (without a catalog we cannot tell a bare foreign id
			// from a valid model name on this provider).
			model := resolveSelectModel(e.opts.InitialProvider, e.opts.InitialModel, defaultModel)
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
	case protocol.SetEffort:
		if e.turnActive() {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "cannot change effort while a turn is running",
			})
			return
		}
		e.setEffort(op.Level)
	case protocol.SetFast:
		if e.turnActive() {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "cannot change fast while a turn is running",
			})
			return
		}
		e.setFast(op.Enabled)
	case protocol.PermissionReply:
		e.childMu.Lock()
		childReply := e.activeChildReply
		e.childMu.Unlock()
		if childReply != nil {
			childReply(op)
			return
		}
		e.perms.Reply(op)
	case protocol.QuestionReply:
		e.childMu.Lock()
		childReply := e.activeChildQuestionReply
		e.childMu.Unlock()
		if childReply != nil {
			childReply(op)
			return
		}
		e.questions.Reply(op)
	case protocol.Interrupt:
		if e.turnCancel != nil {
			e.turnCancel()
		}
		// Best-effort: cancel a foreground child turn if one is active.
		e.childMu.Lock()
		childOps := e.activeChildOps
		e.childMu.Unlock()
		if childOps != nil {
			select {
			case childOps <- protocol.Interrupt{}:
			default:
			}
		}
	case protocol.FilesChanged:
		e.handleFilesChanged(op)
	}
}

// handleFilesChanged invalidates read snapshots for the reported paths and
// emits FilesInvalidated so the session log and TUI observe the change.
// Accepted while a turn is running: external edits can land mid-turn.
func (e *Engine) handleFilesChanged(op protocol.FilesChanged) {
	abs := make([]string, 0, len(op.Paths))
	seen := make(map[string]struct{}, len(op.Paths))
	for _, p := range op.Paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(e.opts.WorkDir, p)
		}
		p = filepath.Clean(p)
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		abs = append(abs, p)
	}
	if len(abs) == 0 {
		return
	}
	e.files.MarkDirty(abs...)
	display := make([]string, len(abs))
	for i, p := range abs {
		display[i] = relDisplayPath(e.opts.WorkDir, p)
	}
	e.emit(protocol.FilesInvalidated{
		Correlation: e.sessionCorr(),
		Paths:       display,
		Reason:      op.Reason,
	})
}

func relDisplayPath(workDir, abs string) string {
	if workDir == "" {
		return abs
	}
	if rel, err := filepath.Rel(workDir, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return abs
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

// splitProviderModel parses "provider/model" form. Both sides of the first
// slash must be non-empty. Only the first slash is considered so model ids
// that themselves contain slashes remain in the model part.
func splitProviderModel(s string) (provider, model string, ok bool) {
	provider, model, found := strings.Cut(s, "/")
	if !found || provider == "" || model == "" {
		return "", "", false
	}
	return provider, model, true
}

// stripMatchingProviderPrefixes repeatedly strips a leading "provider/"
// segment (EqualFold) until none remain. Caps iterations so a pathological
// input cannot loop forever.
//
//	openai + "openai/openai/gpt-5.6-sol" → "gpt-5.6-sol"
//	openai + "openai/gpt-5.6-sol"        → "gpt-5.6-sol"
//	openai + "gpt-5.6-sol"               → "gpt-5.6-sol"
func stripMatchingProviderPrefixes(providerName, model string) string {
	if providerName == "" || model == "" {
		return model
	}
	const maxStrip = 8
	for range maxStrip {
		prov, bare, ok := splitProviderModel(model)
		if !ok || !strings.EqualFold(prov, providerName) {
			return model
		}
		model = bare
	}
	return model
}

// resolveSelectModel normalizes op.Model for a chosen provider: matching
// "provider/id" prefixes (including repeated ones) become bare ids; foreign
// prefixes are discarded so the caller falls back to the provider default;
// bare ids pass through.
func resolveSelectModel(providerName, model, defaultModel string) string {
	if prov, _, ok := splitProviderModel(model); ok {
		if strings.EqualFold(prov, providerName) {
			// First segment matches: strip all matching prefixes (handles
			// doubles like openai/openai/gpt-5.6-sol).
			model = stripMatchingProviderPrefixes(providerName, model)
		} else {
			model = ""
		}
	}
	if model == "" {
		return defaultModel
	}
	return model
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
	model := resolveSelectModel(op.Provider, op.Model, defaultModel)
	e.setProvider(op.Provider, p, model)
}

func (e *Engine) setProvider(name string, p provider.Provider, model string) {
	// Chokepoint: never store a matching provider/ prefix (or doubles) on the
	// active model string. Callers may still pass already-prefixed ids.
	model = stripMatchingProviderPrefixes(name, model)
	e.prov, e.provName, e.model = p, name, model
	e.emit(protocol.ModelSelected{
		Correlation: e.sessionCorr(),
		Provider:    name,
		Model:       model,
	})
}

// setModel stores a bare model id for the current provider, stripping any
// matching provider/ prefixes first, and emits ModelSelected.
func (e *Engine) setModel(model string) {
	if e.provName != "" {
		model = stripMatchingProviderPrefixes(e.provName, model)
	}
	e.model = model
	e.emit(protocol.ModelSelected{
		Correlation: e.sessionCorr(),
		Provider:    e.provName,
		Model:       model,
	})
}

// setEffort records the reasoning dial and confirms it. An unrecognized level
// is rejected rather than silently forwarded to a provider that would 400 on
// it.
func (e *Engine) setEffort(level protocol.Effort) {
	parsed, ok := protocol.ParseEffort(string(level))
	if !ok {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     fmt.Sprintf("unknown effort %q (want %s)", level, effortNames()),
		})
		return
	}
	e.effort = parsed
	e.emit(protocol.EffortSelected{
		Correlation: e.sessionCorr(),
		Level:       parsed,
	})
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
	e.emit(protocol.FastSelected{
		Correlation: e.sessionCorr(),
		Enabled:     enabled,
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
// provider/model pins and permission profile when set.
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
	// Child sessions may only apply Deny rules from an agent profile so a
	// subagent cannot widen parent Deny/Ask via Allow (AG3). Root keeps the
	// full profile (AG1/AG2).
	agentRules := agent.Permissions
	if e.opts.Depth > 0 {
		agentRules = permission.DenyOnly(agentRules)
	}
	e.perms.SetAgentRules(agentRules)
	e.emit(protocol.AgentSelected{
		Correlation: e.sessionCorr(),
		Name:        agent.Name,
	})
	if agent.Effort != protocol.EffortDefault {
		e.setEffort(agent.Effort)
	}

	// Model-only "provider/id" pins promote the prefix to a provider pin.
	// setProvider / resolveSelectModel strip matching prefixes (including
	// doubles) so we never store openai/openai/... on the active model.
	agentProvider, agentModel := agent.Provider, agent.Model
	if agentProvider == "" {
		if prov, _, ok := splitProviderModel(agent.Model); ok {
			agentProvider = prov
		}
	}

	switch {
	case agentProvider != "" && e.opts.Select != nil:
		p, defaultModel, err := e.opts.Select(agentProvider)
		if err != nil {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     fmt.Sprintf("agent %s: %v", agent.Name, err),
			})
			return
		}
		model := resolveSelectModel(agentProvider, agentModel, defaultModel)
		e.setProvider(agentProvider, p, model)
	case agentModel != "" && e.prov != nil:
		e.setModel(agentModel)
	}
}

// queueSwitchAgent validates name and queues it for application after the
// current tool batch (before the next provider Stream) or at turn end.
func (e *Engine) queueSwitchAgent(name string) error {
	if _, ok := e.findAgent(name); !ok {
		return fmt.Errorf("unknown agent %q", name)
	}
	e.pendingAgentMu.Lock()
	e.pendingAgent = name
	e.pendingAgentMu.Unlock()
	return nil
}

// applyPendingAgent applies a tool-queued agent switch, if any.
func (e *Engine) applyPendingAgent() {
	e.pendingAgentMu.Lock()
	name := e.pendingAgent
	e.pendingAgent = ""
	e.pendingAgentMu.Unlock()
	if name == "" {
		return
	}
	e.handleSelectAgent(protocol.SelectAgent{Name: name})
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
// provider-request ID and attempt number (retries included). finishing is
// closed exactly once immediately before the terminal TurnCompleted emission
// so Run can join the worker before the next op.
func (e *Engine) runTurn(ctx context.Context, text string, turnID string, finishing chan struct{}) {
	turnCorr := e.baseCorr()
	turnCorr.TurnID = turnID
	e.emit(protocol.UserMessage{Correlation: turnCorr, Text: text})
	e.emit(protocol.TurnStarted{Correlation: turnCorr})
	e.messages = append(e.messages, provider.Message{Role: provider.RoleUser, Text: text})

	for {
		outcome, reqCorr, err := e.streamModel(ctx, turnID)
		if err != nil {
			e.failTurn(err, reqCorr, finishing)
			return
		}

		e.messages = append(e.messages, provider.Message{
			Role:      provider.RoleAssistant,
			Text:      outcome.text,
			ToolCalls: outcome.calls,
			Reasoning: outcome.reasoning,
		})
		if len(outcome.calls) == 0 {
			e.completeTurn(finishing, reqCorr, outcome.stopReason)
			return
		}
		for i, call := range outcome.calls {
			// Unstarted calls: history-only synthetic results, no begin/end/Execute.
			if ctx.Err() != nil {
				e.appendUnstartedToolResults(outcome.calls[i:])
				e.failTurn(ctx.Err(), reqCorr, finishing)
				return
			}
			e.messages = append(e.messages, e.execToolCall(ctx, call, reqCorr))
			if ctx.Err() != nil {
				// Current call was started (and canceled); remaining are unstarted.
				e.appendUnstartedToolResults(outcome.calls[i+1:])
				e.failTurn(ctx.Err(), reqCorr, finishing)
				return
			}
		}
		// Apply tool-queued agent switch so the next Stream uses the new
		// agent system prompt (not only after TurnCompleted).
		e.applyPendingAgent()
	}
}

// streamOutcome is one successful provider stream (after any retries).
type streamOutcome struct {
	text       string
	calls      []provider.ToolCall
	reasoning  []json.RawMessage
	stopReason string
}

// streamModel performs one logical model request, retrying transient stream
// failures with a fresh attempt identity. Tools are never executed here, so a
// retry cannot duplicate completed tool side effects.
func (e *Engine) streamModel(ctx context.Context, turnID string) (streamOutcome, protocol.Correlation, error) {
	maxAttempts := e.opts.MaxStreamAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastCorr protocol.Correlation
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		reqCorr := e.baseCorr()
		reqCorr.TurnID = turnID
		reqCorr.ProviderRequestID = rand.Text()
		reqCorr.Attempt = attempt
		lastCorr = reqCorr

		outcome, err := e.consumeStream(ctx, reqCorr)
		if err == nil {
			return outcome, reqCorr, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return streamOutcome{}, reqCorr, ctx.Err()
		}
		if attempt == maxAttempts || !provider.IsRetryable(err) {
			return streamOutcome{}, reqCorr, err
		}
		delay := e.streamRetryDelay(attempt + 1)
		e.emit(protocol.ProviderRetrying{
			Correlation: reqCorr,
			NextAttempt: attempt + 1,
			DelayMs:     int(delay / time.Millisecond),
			Message:     err.Error(),
		})
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return streamOutcome{}, reqCorr, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return streamOutcome{}, lastCorr, lastErr
}

func (e *Engine) streamRetryDelay(nextAttempt int) time.Duration {
	if e.opts.StreamRetryBackoff != nil {
		return e.opts.StreamRetryBackoff(nextAttempt)
	}
	// 200ms, 400ms, 800ms… capped at 2s.
	shift := nextAttempt - 2
	if shift < 0 {
		shift = 0
	}
	d := 200 * time.Millisecond << shift
	if d > 2*time.Second {
		d = 2 * time.Second
	}
	return d
}

// consumeStream runs one Provider.Stream attempt and applies the terminal
// contract. On success, history-ready text/tool/reasoning are returned and
// usage is emitted; nothing is appended to e.messages here.
func (e *Engine) consumeStream(ctx context.Context, reqCorr protocol.Correlation) (streamOutcome, error) {
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
		return streamOutcome{}, err
	}
	stream = provider.NormalizeStream(stream)

	var textBuf strings.Builder
	var calls []provider.ToolCall
	var reasoning []json.RawMessage
	stopReason := ""
	var streamErr error
	terminated := false
	for ev := range stream {
		if terminated {
			continue
		}
		switch ev.Type {
		case provider.EventTextDelta:
			textBuf.WriteString(ev.Text)
			e.emit(protocol.TextDelta{Correlation: reqCorr, Text: ev.Text})
		case provider.EventToolCall:
			if ev.ToolCall != nil {
				calls = append(calls, *ev.ToolCall)
			}
		case provider.EventReasoning:
			// Kept on the message but never emitted: reasoning artifacts
			// exist so the next request can replay them, and current
			// models do not return readable chain of thought anyway.
			reasoning = append(reasoning, ev.Reasoning)
		case provider.EventDone:
			terminated = true
			stopReason = ev.StopReason
			e.emitUsage(reqCorr, ev.Usage)
		case provider.EventError:
			terminated = true
			streamErr = ev.Err
			if streamErr == nil {
				streamErr = errors.New("provider stream error")
			}
		}
	}
	if ctx.Err() != nil {
		return streamOutcome{}, ctx.Err()
	}
	if !terminated {
		return streamOutcome{}, provider.ErrIncompleteStream
	}
	if streamErr != nil {
		return streamOutcome{}, streamErr
	}
	return streamOutcome{
		text:       textBuf.String(),
		calls:      calls,
		reasoning:  reasoning,
		stopReason: stopReason,
	}, nil
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
		callID := call.ID
		tc := &tool.Context{
			WorkDir: e.opts.WorkDir,
			Files:   e.files,
			Ask: func(ctx context.Context, req tool.AskRequest) error {
				return e.perms.AskWithCorrelation(ctx, req, corr)
			},
			AskUser: func(ctx context.Context, req tool.QuestionRequest) (tool.QuestionResponse, error) {
				prompts := make([]protocol.QuestionPrompt, len(req.Questions))
				for i, q := range req.Questions {
					opts := make([]protocol.QuestionOption, len(q.Options))
					for j, o := range q.Options {
						opts[j] = protocol.QuestionOption{Label: o.Label, Description: o.Description}
					}
					prompts[i] = protocol.QuestionPrompt{
						ID:       q.ID,
						Header:   q.Header,
						Question: q.Question,
						Options:  opts,
					}
				}
				answers, err := e.questions.Ask(ctx, corr, prompts)
				if err != nil {
					return tool.QuestionResponse{}, err
				}
				return tool.QuestionResponse{Answers: answers}, nil
			},
			SwitchAgent: e.queueSwitchAgent,
			ReportOutput: func(data string) {
				if data == "" {
					return
				}
				e.emit(protocol.ToolCallOutput{
					Correlation: corr,
					CallID:      callID,
					Data:        data,
				})
			},
		}
		if e.opts.Depth < e.opts.MaxChildDepth {
			tc.SpawnTask = e.spawnChild
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
		var permRejected *permission.RejectedError
		var qRejected *question.RejectedError
		switch {
		case errors.As(err, &permRejected):
			output = permRejected.Error()
		case errors.As(err, &qRejected):
			output = qRejected.Error()
		default:
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
// Any remaining tool-queued agent switch is applied after TurnCompleted so
// Run's join on turnDone observes the new agent (belt-and-suspenders with the
// post-tool-batch apply in runTurn).
func (e *Engine) completeTurn(finishing chan struct{}, corr protocol.Correlation, stopReason string) {
	close(finishing)
	e.emit(protocol.TurnCompleted{Correlation: corr, StopReason: stopReason})
	e.applyPendingAgent()
}

// emitUsage translates provider.Usage into a protocol.UsageReported event.
// A nil usage means the vendor did not report counts — emit nothing (unknown).
//
// used = InputTokens + CacheReadTokens + CacheCreationTokens + OutputTokens;
// if all those are 0 but TotalTokens > 0, used = TotalTokens and input/output
// stay unknown (a total alone is not a measured zero on the parts).
func (e *Engine) emitUsage(corr protocol.Correlation, u *provider.Usage) {
	if u == nil {
		return
	}
	used := u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens + u.OutputTokens
	input := protocol.KnownTokens(u.InputTokens)
	output := protocol.KnownTokens(u.OutputTokens)
	if used == 0 && u.TotalTokens > 0 {
		used = u.TotalTokens
		input = protocol.UnknownTokens()
		output = protocol.UnknownTokens()
	}
	source := protocol.UsageSourceActual
	if u.Estimated {
		source = protocol.UsageSourceEstimated
	}
	e.emit(protocol.UsageReported{
		Correlation: corr,
		Input:       input,
		Output:      output,
		Used:        protocol.KnownTokens(used),
		Source:      source,
	})
}

func (e *Engine) toolNames() string {
	var names []string
	for _, s := range e.opts.Registry.Schemas() {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}
