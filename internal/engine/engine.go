// Package engine is the headless agent runtime: it consumes protocol.Ops,
// runs the model turn loop with tool dispatch, and emits protocol.Events.
// Frontends never call into this package beyond New/Run/Ops/Events.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/question"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// defaultMaxStreamAttempts is how many times one logical model request may
// call Provider.Stream on retryable failure before the turn fails.
const (
	defaultMaxStreamAttempts = 3
	// absoluteMaxChildDepth is a hard ceiling against runaway nested task swarms.
	absoluteMaxChildDepth = 8
)

// Model-facing interrupt texts (aliases of protocol.ToolFeedback* helpers).
var (
	canceledToolOutput  = protocol.ToolFeedbackCanceled()
	unstartedToolOutput = protocol.ToolFeedbackUnstarted()
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
	// InitialAutonomy is the exit-gate policy at startup. Empty becomes
	// AutonomySupervised so the dial is always explicit.
	InitialAutonomy protocol.Autonomy
	// InitialPermissionMode is the tool-permission posture at startup. Empty
	// becomes PermissionModeDefault so the dial is always explicit.
	InitialPermissionMode protocol.PermissionMode
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
	// ContextWindow is the selected model's context limit in tokens. Zero
	// means unknown; threshold compaction stays off until LookupContextWindow
	// or a later assignment provides a positive value. Overflow recovery does
	// not require a known window.
	ContextWindow int
	// LookupContextWindow resolves context limits when the provider/model
	// changes. nil skips catalog lookups.
	LookupContextWindow func(provider, model string) int
	// CompactionThreshold is the occupancy fraction (0–1) that triggers
	// automatic compaction before a Stream. Zero defaults to 0.80; >=1
	// disables threshold compaction.
	CompactionThreshold float64
	// CompactionBuffer is extra token headroom reserved with MaxTokens when
	// computing the threshold budget. Zero defaults to 4096.
	CompactionBuffer int
	// KeepUserTurns is how many trailing real user turns to preserve when
	// compacting. Zero defaults to 2.
	KeepUserTurns int
	// CompactionStrategy is "trim" (default) or "summarize". Unknown values
	// fall back to trim.
	CompactionStrategy string
	// CompactionModel optionally pins the model id for summarize compaction.
	// Empty uses the session model (same provider).
	CompactionModel string
	// ProjectRoot is the workspace root (often the git toplevel). Shown in
	// the environment system-prompt layer; empty falls back to WorkDir.
	ProjectRoot string
	// Instructions are preloaded AGENTS.md/CLAUDE.md blocks appended after
	// the environment layer (see config.LoadInstructions).
	Instructions []string
	// Memory, when set, supplies project memory for the auto-loaded
	// project_memory system layer each composition (tagged entries only).
	// nil disables auto-load. Refreshed every turn so memory_write is visible
	// in the same session.
	Memory MemorySource
	// SystemPrompt, when set, replaces the provider overlay for the build
	// agent only (shared baseline still applies). From config systemPrompt.
	SystemPrompt string
	// Rules are permission ruleset layers, earliest first (later wins).
	Rules []permission.Ruleset
	// Hooks are shell-command lifecycle hooks (pre/post tool use). Empty disables.
	Hooks []tool.HookDef
	// HookRules are declarative config rules (event matcher → log/block/notify).
	// Evaluated before shell hooks on pre_tool_use; block skips Execute.
	HookRules permission.HookRuleset
	// PersistProjectRule, when set, is invoked after a DecisionProject grant
	// so the rule can be written to project config. Optional.
	PersistProjectRule func(permission.Rule) error
	// MaxChildDepth bounds foreground task nesting. Zero defaults to 1 in New
	// (root depth 0 may spawn one child; that child may not spawn further).
	// Values above absoluteMaxChildDepth are clamped.
	MaxChildDepth int
	// TaskOneShot marks engines spawned by the task tool: Run exits once the
	// first turn finishes, nested children complete, and idle nudges drain.
	// Root engines leave this false.
	TaskOneShot bool
	// Depth is this engine's lineage depth (0 = root).
	Depth int
	// ParentSessionID is the spawning session's ID; empty on root engines.
	ParentSessionID string
	// PersistSessionMeta, when set, writes durable session metadata (sidecar).
	// The engine emits protocol.SessionMeta after a successful persist.
	PersistSessionMeta func(meta protocol.SessionMeta) error
	// Workflows are named phase sequences (built-in plan-implement plus any
	// loaded from .strike/workflows). Empty falls back to the built-in only.
	Workflows []config.Workflow
	// DefaultWorkflow is entered by enter_plan_mode when set; empty means
	// "plan-implement".
	DefaultWorkflow string
	// OpenChildSession, when set, opens a durable log for a spawned child.
	// parentID and a suggested childID/title are provided; the returned id is
	// used as the child SessionID when non-empty.
	OpenChildSession func(parentID, childID, title string) (id string, err error)
	// AppendChildEvent, when set, persists one event to a child session log.
	AppendChildEvent func(childID string, ev protocol.Event) error
	// CloseChildSession, when set, closes a child session log after completion.
	CloseChildSession func(childID string) error
	// InitialMessages seeds model-facing history (durable resume / --continue).
	// Copied at New; not emitted as transcript events.
	InitialMessages []provider.Message
	// InitialPriority sets the sticky priority tier before Run without
	// emitting FastSelected (TUI seeds fast from resume snapshot).
	InitialPriority bool
	// InitialTitled skips auto SessionTitled when the session was already titled.
	InitialTitled bool
	// InitialPhaseWorkflow / InitialPhaseIndex restore an active workflow
	// phase after agent selection at startup. Empty workflow skips restore.
	InitialPhaseWorkflow string
	InitialPhaseIndex    int
	// InitialAlwaysGrants restores session DecisionAlways rules after the
	// initial agent profile is applied (SetAgentRules clears grants).
	InitialAlwaysGrants permission.Ruleset
	// QuietStartup applies Initial* provider/model/effort/autonomy/
	// permission-mode/agent/phase without emitting *Selected or PhaseChanged.
	// Durable resume sets this: the JSONL already has those events and the TUI
	// seeds from Replay. Cleared before the op loop so user-driven changes
	// still emit.
	QuietStartup bool
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
	// autonomy is the session exit-gate policy (supervised|agent|checks).
	autonomy protocol.Autonomy
	// permMode is the session tool-permission posture dial.
	permMode protocol.PermissionMode
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

	// children tracks non-blocking child engines. Permission/question replies
	// fan out to every child plus the parent; request IDs are session-scoped
	// so only one service matches.
	childMu  sync.Mutex
	children map[string]*childHandle

	// childDone delivers ChildCompleted from drain goroutines to Run so the
	// parent can inject a model-visible summary and auto-nudge when idle.
	// Buffered; non-blocking send on the drain side if Run is shutting down.
	childDone chan protocol.ChildCompleted
	// pendingChildNotices holds formatted child.completed texts queued while
	// a parent turn is active; flushed into a follow-up turn when idle.
	pendingChildNotices []string

	// pendingUserInputs holds UserInput accepted while a turn was active.
	// Drained FIFO one-at-a-time after each turn ends. Survives Interrupt so
	// follow-up prompts typed mid-turn are not lost.
	pendingUserInputs []pendingUserInput

	// pendingAgent is set by tools via SwitchAgent and applied after each tool
	// batch (so the next Stream sees the new agent/prompt) and again in
	// completeTurn if anything remains when the turn ends.
	pendingAgentMu sync.Mutex
	pendingAgent   string

	// workflow/phaseIndex track the active workflow phase (-1 = none).
	workflow   config.Workflow
	phaseIndex int

	// files tracks tool read snapshots so external edits (FilesChanged / /vim)
	// force the model to re-read before edit/write.
	files *tool.FileState

	// checkpoints snapshot pre-mutation file bytes per turn for /undo restore.
	checkpoints *tool.CheckpointStore

	// titled is set after the first SessionTitled emit so auto-titling runs once.
	titled bool

	// lastUsed/lastUsedKnown track the latest provider-reported context
	// occupancy for threshold compaction.
	lastUsed      int
	lastUsedKnown bool
	// contextWindowTokens is the live model limit (from opts or lookup).
	contextWindowTokens int

	// quietStartup suppresses selection/phase confirms during Run's initial
	// apply (see Options.QuietStartup). Owned by Run only.
	quietStartup bool

	// lastEffective is the redacted layer snapshot from the most recent
	// Stream composition. Written by the turn worker; read by inspect on Run.
	effectiveMu   sync.Mutex
	lastEffective effectiveSnapshot
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
	} else if opts.MaxChildDepth > absoluteMaxChildDepth {
		opts.MaxChildDepth = absoluteMaxChildDepth
	}
	if len(opts.Workflows) == 0 {
		opts.Workflows = []config.Workflow{config.BuiltinPlanImplement()}
	}
	e := &Engine{
		opts:                opts,
		ops:                 make(chan protocol.Op, 16),
		events:              make(chan protocol.Event, 256),
		beginReqs:           make(chan beginReq),
		files:               &tool.FileState{},
		checkpoints:         tool.NewCheckpointStore(),
		children:            make(map[string]*childHandle),
		childDone:           make(chan protocol.ChildCompleted, 32),
		contextWindowTokens: opts.ContextWindow,
		priority:            opts.InitialPriority,
		titled:              opts.InitialTitled,
		phaseIndex:          -1,
	}
	if len(opts.InitialMessages) > 0 {
		e.messages = append([]provider.Message(nil), opts.InitialMessages...)
	}
	e.perms = permission.New(e.emit, opts.Rules...)
	if opts.PersistProjectRule != nil {
		e.perms.SetProjectPersister(opts.PersistProjectRule)
	}
	e.questions = question.New(e.emit)
	return e
}

// Messages returns a copy of the model-facing conversation history.
func (e *Engine) Messages() []provider.Message {
	if len(e.messages) == 0 {
		return nil
	}
	out := make([]provider.Message, len(e.messages))
	copy(out, e.messages)
	return out
}

func (e *Engine) Ops() chan<- protocol.Op       { return e.ops }
func (e *Engine) Events() <-chan protocol.Event { return e.events }

func (e *Engine) emit(ev protocol.Event) { e.events <- ev }

// emitSelected emits selection/phase confirms unless quietStartup is set
// (resume re-applies state without re-appending the JSONL).
func (e *Engine) emitSelected(ev protocol.Event) {
	if e.quietStartup {
		return
	}
	e.emit(ev)
}

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
	// Keep Events open until children finish so ChildCompleted can emit.
	defer close(e.events)
	defer e.shutdownChildren()
	e.quietStartup = e.opts.QuietStartup
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
	// Autonomy is always applied so the exit gate matches config/restore.
	// Fresh sessions announce it; QuietStartup (resume) skips the emit.
	e.setAutonomy(e.opts.InitialAutonomy)
	// Permission mode rules + emit before AgentSelected so unbuffered event
	// consumers that wait on AgentSelected as "startup ready" do not deadlock.
	// Plan workflow alignment runs after agent select (see below).
	e.applyPermissionMode(e.opts.InitialPermissionMode, false)
	initialAgent := e.opts.Agents[0].Name
	if e.opts.InitialAgent != "" {
		if _, ok := e.findAgent(e.opts.InitialAgent); ok {
			initialAgent = e.opts.InitialAgent
		}
	}
	e.handleSelectAgent(protocol.SelectAgent{Name: initialAgent})
	// Plan posture enters the plan workflow after the default agent is applied
	// so agent select cannot clobber it; resume phase restore may still override.
	if e.permMode == protocol.PermissionModePlan {
		_ = e.enterPlanPhase()
	}
	// Resume: re-enter the recorded workflow phase after mode so a restored
	// implement/custom phase is not clobbered by plan-mode enterPlanPhase,
	// then re-seed session always-grants (SetAgentRules cleared them).
	if wf := e.opts.InitialPhaseWorkflow; wf != "" {
		if w, ok := e.findWorkflow(wf); ok {
			_ = e.enterPhase(w, e.opts.InitialPhaseIndex)
		}
	}
	if len(e.opts.InitialAlwaysGrants) > 0 {
		e.perms.SeedAlwaysGrants(e.opts.InitialAlwaysGrants)
	}
	e.quietStartup = false
	oneshotTurnSeen := false
	for {
		e.reapTurn()
		e.drainIdleFollowups(ctx)
		if e.taskOneShotIdle(oneshotTurnSeen) {
			return
		}
		var turnDone <-chan struct{}
		if e.turnDone != nil {
			turnDone = e.turnDone
		}
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
		case completed := <-e.childDone:
			e.queueChildCompleted(completed)
			e.drainIdleFollowups(ctx)
			if e.taskOneShotIdle(oneshotTurnSeen) {
				return
			}
		case <-turnDone:
			oneshotTurnSeen = true
			e.reapTurn()
			e.drainIdleFollowups(ctx)
			if e.taskOneShotIdle(oneshotTurnSeen) {
				return
			}
		}
	}
}

// taskOneShotIdle reports whether a task-spawned engine should exit Run:
// at least one turn finished, no nested children, no active turn, and no
// queued follow-ups (child notices or pending user inputs).
func (e *Engine) taskOneShotIdle(turnSeen bool) bool {
	if !e.opts.TaskOneShot || !turnSeen {
		return false
	}
	if e.turnActive() || len(e.pendingChildNotices) > 0 || len(e.pendingUserInputs) > 0 {
		return false
	}
	e.childMu.Lock()
	n := len(e.children)
	e.childMu.Unlock()
	return n == 0
}

func (e *Engine) handleOp(ctx context.Context, op protocol.Op) {
	// If the worker already emitted terminal TurnCompleted, join it before
	// applying the op so a follow-up UserInput is not rejected as active-turn.
	e.joinFinishingTurn()
	switch op := op.(type) {
	case protocol.UserInput:
		if e.turnActive() {
			e.enqueueUserInput(op)
			return
		}
		if e.prov == nil {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "no model selected — use /provider <anthropic|openai|xai|echo> [model]",
			})
			return
		}
		e.startTurn(ctx, op.Text, op.Images)
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
	case protocol.SetAutonomy:
		if e.turnActive() {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "cannot change autonomy while a turn is running",
			})
			return
		}
		e.setAutonomy(op.Mode)
	case protocol.SetPermissionMode:
		if e.turnActive() {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "cannot change permission mode while a turn is running",
			})
			return
		}
		e.setPermissionMode(op.Mode)
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
		e.routePermissionReply(op)
	case protocol.QuestionReply:
		e.routeQuestionReply(op)
	case protocol.Interrupt:
		// Parent turn only — non-blocking children keep running until the
		// engine shuts down or they finish on their own.
		if e.turnCancel != nil {
			e.turnCancel()
		}
	case protocol.FilesChanged:
		e.handleFilesChanged(op)
	case protocol.Compact:
		e.handleCompact(ctx, op)
	case protocol.InspectEffectivePrompt:
		e.handleInspectEffectivePrompt()
	case protocol.Rewind:
		e.handleRewind(op)
	}
}

// handleInspectEffectivePrompt emits the last Stream layer map when available,
// otherwise the current composition for the next request.
func (e *Engine) handleInspectEffectivePrompt() {
	snap := e.lastOrCurrentEffective()
	e.emit(protocol.EffectivePrompt{
		Correlation:    e.sessionCorr(),
		Layers:         snap.Layers,
		SystemChars:    snap.SystemChars,
		MessageCount:   snap.MessageCount,
		FromLastStream: snap.FromLastStream,
	})
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
	e.refreshContextWindow()
	e.emitSelected(protocol.ModelSelected{
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
	e.refreshContextWindow()
	e.emitSelected(protocol.ModelSelected{
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
	e.emitSelected(protocol.EffortSelected{
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

// setAutonomy records the session exit-gate policy and confirms it. Empty
// normalizes to supervised; unrecognized values are rejected.
func (e *Engine) setAutonomy(mode protocol.Autonomy) {
	parsed, ok := protocol.ParseAutonomy(string(mode))
	if !ok {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     fmt.Sprintf("unknown autonomy %q (want %s)", mode, autonomyNames()),
		})
		return
	}
	e.autonomy = parsed
	e.emitSelected(protocol.AutonomySelected{
		Correlation: e.sessionCorr(),
		Mode:        parsed,
	})
}

func autonomyNames() string {
	names := make([]string, 0, len(protocol.Autonomies()))
	for _, mode := range protocol.Autonomies() {
		names = append(names, string(mode))
	}
	return strings.Join(names, "|")
}

// setPermissionMode records tool-permission posture, updates the permission
// service, and aligns plan posture with the plan-implement workflow. Empty
// normalizes to default; unrecognized values are rejected.
func (e *Engine) setPermissionMode(mode protocol.PermissionMode) {
	e.applyPermissionMode(mode, true)
}

// applyPermissionMode is the shared implementation for startup and SetPermissionMode.
// When alignPlan is false (startup), only rules + confirm are applied; the caller
// enters the plan workflow after agent select. When true (user dial), plan is
// entered or left immediately.
func (e *Engine) applyPermissionMode(mode protocol.PermissionMode, alignPlan bool) {
	parsed, ok := protocol.ParsePermissionMode(string(mode))
	if !ok {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     fmt.Sprintf("unknown permission mode %q (want %s)", mode, permissionModeNames()),
		})
		return
	}
	prev := e.permMode
	e.permMode = parsed
	e.perms.SetPermissionMode(parsed)
	if alignPlan {
		switch {
		case parsed == protocol.PermissionModePlan:
			_ = e.enterPlanPhase()
		case prev == protocol.PermissionModePlan && parsed != protocol.PermissionModePlan:
			// Leaving plan posture via the dial: drop plan phase hard-denies and
			// return to build when still on the plan agent.
			if phase, ok := e.currentPhase(); ok && phase.Name == "plan" {
				e.clearPhase()
				if e.agent.Name == "plan" {
					if _, ok := e.findAgent("build"); ok {
						e.handleSelectAgent(protocol.SelectAgent{Name: "build"})
					}
				}
			}
		}
	}
	e.emitSelected(protocol.PermissionModeSelected{
		Correlation: e.sessionCorr(),
		Mode:        parsed,
	})
}

func permissionModeNames() string {
	names := make([]string, 0, len(protocol.PermissionModes()))
	for _, mode := range protocol.PermissionModes() {
		names = append(names, string(mode))
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
	e.emitSelected(protocol.FastSelected{
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

func agentNamesList(agents []Agent) string {
	if len(agents) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(agents))
	for _, a := range agents {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

// handleSelectAgent switches the active persona and syncs workflow phase
// when the user picks build/plan (tab, /agent, tools via SwitchAgent).
func (e *Engine) handleSelectAgent(op protocol.SelectAgent) {
	if !e.applyAgent(op.Name) {
		return
	}
	// Root only: child sessions do not drive parent workflow phases.
	if e.opts.Depth == 0 {
		e.syncPhaseWithAgent(op.Name)
	}
}

// applyAgent switches persona, agent permission profile, and optional
// provider/model pins without touching workflow phase state.
func (e *Engine) applyAgent(name string) bool {
	agent, ok := e.findAgent(name)
	if !ok {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     fmt.Sprintf("unknown agent %q (available: %s)", name, agentNamesList(e.opts.Agents)),
		})
		return false
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
	e.emitSelected(protocol.AgentSelected{
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
			return true
		}
		model := resolveSelectModel(agentProvider, agentModel, defaultModel)
		e.setProvider(agentProvider, p, model)
	case agentModel != "" && e.prov != nil:
		e.setModel(agentModel)
	}
	return true
}

// queueSwitchAgent validates name and queues it for application after the
// current tool batch (before the next provider Stream) or at turn end.
func (e *Engine) queueSwitchAgent(name string) error {
	if _, ok := e.findAgent(name); !ok {
		return fmt.Errorf("unknown agent %q (available: %s)", name, agentNamesList(e.opts.Agents))
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

// maxPendingUserInputs caps mid-turn UserInput buffering so a runaway sender
// cannot grow memory without bound. Overflow emits EngineError and drops the
// new item (callers such as the TUI keep the draft on failure).
const maxPendingUserInputs = 32

// pendingUserInput is one mid-turn buffered prompt (text + optional images).
type pendingUserInput struct {
	text   string
	images []protocol.ImageAttachment
}

// protocolImagesToProvider decodes base64 session attachments into provider images.
// Invalid entries are skipped so a corrupt log line does not block restore/send.
func protocolImagesToProvider(images []protocol.ImageAttachment) []provider.Image {
	if len(images) == 0 {
		return nil
	}
	out := make([]provider.Image, 0, len(images))
	for _, img := range images {
		mime := strings.TrimSpace(img.MIME)
		if mime == "" || img.Data == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(img.Data)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(img.Data)
			if err != nil {
				continue
			}
		}
		if len(raw) == 0 {
			continue
		}
		out = append(out, provider.Image{MIME: mime, Data: raw})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// enqueueUserInput buffers input for FIFO start after the active turn ends.
// Empty/whitespace-only text with no images is ignored. Queue survives Interrupt.
func (e *Engine) enqueueUserInput(op protocol.UserInput) {
	if strings.TrimSpace(op.Text) == "" && len(op.Images) == 0 {
		return
	}
	if len(e.pendingUserInputs) >= maxPendingUserInputs {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "input queue full; wait for the current turn to finish",
		})
		return
	}
	e.pendingUserInputs = append(e.pendingUserInputs, pendingUserInput{
		text:   op.Text,
		images: append([]protocol.ImageAttachment(nil), op.Images...),
	})
}

// drainIdleFollowups starts at most one follow-up turn when idle: preferred
// user-queued input, otherwise pending child-completion notices.
func (e *Engine) drainIdleFollowups(ctx context.Context) {
	if e.startNextPendingUserInput(ctx) {
		return
	}
	e.flushPendingChildNotices(ctx)
}

// startNextPendingUserInput pops and starts the next queued UserInput when
// idle with a provider. Returns true when a turn was started.
func (e *Engine) startNextPendingUserInput(ctx context.Context) bool {
	if len(e.pendingUserInputs) == 0 {
		return false
	}
	e.joinFinishingTurn()
	if e.turnActive() || e.prov == nil || ctx.Err() != nil {
		return false
	}
	item := e.pendingUserInputs[0]
	e.pendingUserInputs = e.pendingUserInputs[1:]
	if len(e.pendingUserInputs) == 0 {
		e.pendingUserInputs = nil
	}
	e.startTurn(ctx, item.text, item.images)
	return true
}

func (e *Engine) startTurn(ctx context.Context, text string, images []protocol.ImageAttachment) {
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
		e.runTurn(turnCtx, text, images, turnID, finishing)
	}()
}

// maybeTitleSession emits SessionTitled once from the first non-empty user text.
func (e *Engine) maybeTitleSession(text string) {
	if e.titled {
		return
	}
	title := sessionTitleFromText(text)
	if title == "" {
		return
	}
	e.titled = true
	e.emit(protocol.SessionTitled{Correlation: e.sessionCorr(), Title: title})
}

// sessionTitleFromText collapses whitespace, drops controls, and truncates.
// Kept local so engine does not import internal/session (cmd/strike only).
// Logic mirrors session.TitleFromText.
func sessionTitleFromText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	prevSpace := false
	for _, r := range text {
		switch {
		case r == '\u00a0':
			r = ' '
			fallthrough
		case unicode.IsSpace(r):
			if b.Len() == 0 || prevSpace {
				continue
			}
			b.WriteByte(' ')
			prevSpace = true
		case r < 0x20 || r == 0x7f:
			continue
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		return ""
	}
	const maxRunes = 60
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes])
}

// runTurn is the core agent loop: stream a model response; if it requested
// tool calls, execute them and feed results back; otherwise the turn is done.
// turnID is immutable for the turn; each Provider.Stream call gets its own
// provider-request ID and attempt number (retries included). finishing is
// closed exactly once immediately before the terminal TurnCompleted emission
// so Run can join the worker before the next op.
func (e *Engine) runTurn(ctx context.Context, text string, images []protocol.ImageAttachment, turnID string, finishing chan struct{}) {
	turnCorr := e.baseCorr()
	turnCorr.TurnID = turnID
	e.checkpoints.BeginTurn(turnID)
	e.emit(protocol.UserMessage{Correlation: turnCorr, Text: text, Images: images})
	e.maybeTitleSession(text)
	e.emit(protocol.TurnStarted{Correlation: turnCorr})
	e.fireHookRules(turnCorr, permission.HookEventTurnStart, "", "")
	e.messages = append(e.messages, provider.Message{
		Role:   provider.RoleUser,
		Text:   text,
		Images: protocolImagesToProvider(images),
	})

	for {
		e.maybeThresholdCompact(ctx, turnID)
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
// retry cannot duplicate completed tool side effects. A classified context
// overflow triggers at most one compaction + model-only retry.
func (e *Engine) streamModel(ctx context.Context, turnID string) (streamOutcome, protocol.Correlation, error) {
	outcome, corr, err := e.streamModelAttempts(ctx, turnID)
	if err == nil {
		return outcome, corr, nil
	}
	if ctx.Err() != nil {
		return streamOutcome{}, corr, ctx.Err()
	}
	if !provider.IsContextOverflow(err) {
		return streamOutcome{}, corr, err
	}
	overflowCorr := e.baseCorr()
	overflowCorr.TurnID = turnID
	if !e.applyCompaction(ctx, protocol.CompactionReasonOverflow, overflowCorr, "") {
		return streamOutcome{}, corr, fmt.Errorf("context window exceeded; compaction could not reduce history: %w", err)
	}
	// Single recovery pass: model-only, no tool replay (tools run after success).
	outcome, corr, err = e.streamModelAttempts(ctx, turnID)
	if err != nil && provider.IsContextOverflow(err) {
		return streamOutcome{}, corr, fmt.Errorf("context window exceeded after compaction: %w", err)
	}
	return outcome, corr, err
}

// streamModelAttempts retries transient provider failures for one logical
// model request. Overflow is not retried here (see streamModel).
func (e *Engine) streamModelAttempts(ctx context.Context, turnID string) (streamOutcome, protocol.Correlation, error) {
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
	layers := e.systemLayers()
	system := joinPromptLayerTexts(layers)
	e.recordStreamEffective(layers, system)
	stream, err := e.prov.Stream(ctx, provider.Request{
		Model:     e.model,
		System:    system,
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
			// Opaque bytes stay on the assistant message for vendor replay
			// (Anthropic requires thinking blocks verbatim). Displayable
			// prose, when present, streams to the frontend as ReasoningDelta.
			if len(ev.Reasoning) > 0 {
				reasoning = append(reasoning, ev.Reasoning)
			}
			text := ev.Text
			if text == "" {
				text = provider.ReasoningText(ev.Reasoning)
			}
			if text != "" {
				e.emit(protocol.ReasoningDelta{Correlation: reqCorr, Text: text})
			}
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
		e.messages = append(e.messages, e.settleToolFeedback(toolFeedback{
			CallID:  call.ID,
			Output:  unstartedToolOutput,
			IsError: true,
		}))
	}
}

// toolFeedback is the uniform settlement for one tool call: optional
// ToolCallEnd for the frontend plus a RoleTool message for the model.
// EmitEnd is false for unstarted calls (history-only synthetic results).
type toolFeedback struct {
	Corr     protocol.Correlation
	CallID   string
	Output   string
	IsError  bool
	Title    string
	Metadata json.RawMessage
	EmitEnd  bool
}

// settleToolFeedback is the formal tool-result feedback path: one place that
// pairs model history with (when EmitEnd) a ToolCallEnd event. Permission
// denials, user rejects, hook blocks, interrupts, and ordinary results all
// settle here so future phase bounces and hook messages share the same shape.
func (e *Engine) settleToolFeedback(fb toolFeedback) provider.Message {
	if fb.EmitEnd {
		e.emit(protocol.ToolCallEnd{
			Correlation: fb.Corr,
			CallID:      fb.CallID,
			Title:       fb.Title,
			Output:      fb.Output,
			IsError:     fb.IsError,
			Metadata:    fb.Metadata,
		})
	}
	return provider.Message{
		Role:       provider.RoleTool,
		ToolResult: &provider.ToolResult{CallID: fb.CallID, Output: fb.Output, IsError: fb.IsError},
	}
}

// modelFacingToolOutput maps Execute errors onto protocol.ToolFeedback* text.
// Success returns the tool's own output unchanged.
func modelFacingToolOutput(res tool.Result, err error) (output string, isError bool) {
	if err == nil {
		return res.Output, false
	}
	var permDenied *permission.DeniedError
	var permRejected *permission.RejectedError
	var qRejected *question.RejectedError
	switch {
	case errors.As(err, &permDenied):
		return permDenied.Error(), true
	case errors.As(err, &permRejected):
		return permRejected.Error(), true
	case errors.As(err, &qRejected):
		return qRejected.Error(), true
	default:
		return protocol.ToolFeedbackError(err.Error()), true
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
		return e.settleToolFeedback(toolFeedback{
			CallID:  call.ID,
			Output:  unstartedToolOutput,
			IsError: true,
		})
	}
	ack := <-result
	if !ack.emitted {
		return e.settleToolFeedback(toolFeedback{
			CallID:  call.ID,
			Output:  unstartedToolOutput,
			IsError: true,
		})
	}
	// Begin was emitted. Pre-Execute cancel/shutdown check (no Execute).
	if ctx.Err() != nil {
		return e.canceledToolResult(call.ID, corr)
	}

	// Declarative rules first (cheap, no process). Block skips shell + Execute.
	if d := e.fireHookRules(corr, permission.HookEventPreToolUse, call.Name, call.ID); d.Block {
		return e.settleToolFeedback(toolFeedback{
			Corr:    corr,
			CallID:  call.ID,
			Output:  protocol.ToolFeedbackBlocked(d.BlockMessage()),
			IsError: true,
			EmitEnd: true,
		})
	}

	pre, err := e.runToolHooks(ctx, tool.HookEventPreToolUse, call, corr, "", false)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return e.canceledToolResult(call.ID, corr)
		}
		return e.settleToolFeedback(toolFeedback{
			Corr:    corr,
			CallID:  call.ID,
			Output:  protocol.ToolFeedbackError(err.Error()),
			IsError: true,
			EmitEnd: true,
		})
	}
	if !pre.Allow {
		return e.settleToolFeedback(toolFeedback{
			Corr:    corr,
			CallID:  call.ID,
			Output:  protocol.ToolFeedbackBlocked(pre.Inject),
			IsError: true,
			EmitEnd: true,
		})
	}

	var res tool.Result
	t, ok := e.opts.Registry.Get(call.Name)
	if !ok {
		err = fmt.Errorf("unknown tool %q; available tools: %s", call.Name, e.toolNames())
	} else {
		callID := call.ID
		tc := &tool.Context{
			WorkDir:    e.opts.WorkDir,
			Files:      e.files,
			Checkpoint: e.checkpoints.Snapshot,
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
			SwitchAgent:    e.queueSwitchAgent,
			EnterPlanPhase: e.enterPlanPhase,
			AdvancePhase:   e.advancePhase,
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
			Process: tool.ProcessObserver{
				Started: func(id string, argv []string) {
					e.emit(protocol.ProcessStarted{
						Correlation: corr,
						ProcessID:   id,
						CallID:      callID,
						Argv:        argv,
						Cwd:         e.opts.WorkDir,
					})
				},
				Output: func(id, stream, data string) {
					if data == "" {
						return
					}
					e.emit(protocol.ProcessOutput{
						Correlation: corr,
						ProcessID:   id,
						Stream:      stream,
						Data:        data,
					})
				},
				Exited: func(id string, exitCode int, status tool.ProcessStatus) {
					e.emit(protocol.ProcessExited{
						Correlation: corr,
						ProcessID:   id,
						ExitCode:    exitCode,
						Status:      protocol.ProcessStatus(status),
					})
				},
			},
			RecordSessionPR: e.recordSessionPR(corr),
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

	output, isError := modelFacingToolOutput(res, err)
	if pre.Inject != "" {
		if output == "" {
			output = pre.Inject
		} else {
			output = output + "\n" + pre.Inject
		}
	}

	post, postErr := e.runToolHooks(ctx, tool.HookEventPostToolUse, call, corr, output, isError)
	if postErr != nil {
		if ctx.Err() != nil || errors.Is(postErr, context.Canceled) {
			return e.canceledToolResult(call.ID, corr)
		}
		// Post-hook infrastructure errors do not discard a successful tool result.
	} else if !post.Allow {
		isError = true
		if post.Inject != "" {
			output = protocol.ToolFeedbackBlocked(post.Inject)
		} else {
			output = protocol.ToolFeedbackBlocked("")
		}
	} else if post.Inject != "" {
		if output == "" {
			output = post.Inject
		} else {
			output = output + "\n" + post.Inject
		}
	}

	// Declarative post rules observe the completed call (log/notify only).
	e.fireHookRules(corr, permission.HookEventPostToolUse, call.Name, call.ID)

	return e.settleToolFeedback(toolFeedback{
		Corr:     corr,
		CallID:   call.ID,
		Output:   output,
		IsError:  isError,
		Title:    res.Title,
		Metadata: res.Metadata,
		EmitEnd:  true,
	})
}

// runToolHooks runs configured shell hooks for a tool lifecycle event.
// Trust is gated via permission "hook" (first-run ask by default).
func (e *Engine) runToolHooks(ctx context.Context, event string, call provider.ToolCall, corr protocol.Correlation, toolOutput string, isError bool) (tool.HookOutcome, error) {
	if len(e.opts.Hooks) == 0 {
		return tool.HookOutcome{Allow: true}, nil
	}
	payload := tool.HookPayload{
		Event:      event,
		SessionID:  e.opts.SessionID,
		CWD:        e.opts.WorkDir,
		ToolName:   call.Name,
		ToolCallID: call.ID,
		ToolInput:  call.Args,
		ToolOutput: toolOutput,
		IsError:    isError,
	}
	return tool.RunHooks(ctx, e.opts.Hooks, event, payload, e.opts.WorkDir, func(ctx context.Context, command string) error {
		return e.perms.AskWithCorrelation(ctx, tool.AskRequest{
			Permission: "hook",
			Patterns:   []string{command},
			Always:     []string{command},
		}, corr)
	})
}

// recordSessionPR returns a tool callback that persists PR linkage and emits
// protocol.SessionMeta. Nil when neither persist nor emission is useful.
func (e *Engine) recordSessionPR(corr protocol.Correlation) func(tool.SessionPR) error {
	return func(pr tool.SessionPR) error {
		if pr.URL == "" {
			return nil
		}
		state := strings.ToLower(strings.TrimSpace(pr.State))
		if state == "" {
			state = "open"
		}
		meta := protocol.SessionMeta{
			Correlation: corr,
			PRURL:       pr.URL,
			PRNumber:    pr.Number,
			PRState:     state,
		}
		if e.opts.PersistSessionMeta != nil {
			if err := e.opts.PersistSessionMeta(meta); err != nil {
				return err
			}
		}
		e.emit(meta)
		return nil
	}
}

func (e *Engine) canceledToolResult(callID string, corr protocol.Correlation) provider.Message {
	return e.settleToolFeedback(toolFeedback{
		Corr:    corr,
		CallID:  callID,
		Output:  canceledToolOutput,
		IsError: true,
		EmitEnd: true,
	})
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
	e.checkpoints.CommitTurn()
	e.fireHookRules(corr, permission.HookEventTurnEnd, "", "")
	e.emit(protocol.TurnCompleted{Correlation: corr, StopReason: stopReason})
	e.applyPendingAgent()
}

// fireHookRules evaluates declarative config rules and emits HookMatched for
// each log/notify/block hit. Returns the decision so pre_tool_use can block
// before shell hooks and Execute. callID is optional tool correlation.
func (e *Engine) fireHookRules(corr protocol.Correlation, event, subject, callID string) permission.HookDecision {
	d := permission.EvaluateHooks(e.opts.HookRules, event, subject)
	if d.Block && strings.TrimSpace(d.BlockHit.Message) == "" {
		d.BlockHit.Message = permission.DefaultBlockMessage(event, d.BlockHit.Matcher, subject)
	}
	emitHit := func(hit permission.HookHit) {
		e.emit(protocol.HookMatched{
			Correlation: corr,
			Event:       hit.Event,
			Action:      hit.Action,
			Matcher:     hit.Matcher,
			Tool:        hit.Tool,
			Message:     hit.Message,
			CallID:      callID,
		})
	}
	for _, hit := range d.Log {
		emitHit(hit)
	}
	for _, hit := range d.Notify {
		emitHit(hit)
	}
	if d.Block {
		emitHit(d.BlockHit)
	}
	return d
}

// emitUsage translates provider.Usage into a protocol.UsageReported event.
// A nil usage means the vendor did not report counts — emit nothing (unknown).
//
// used = InputTokens + CacheReadTokens + CacheCreationTokens + OutputTokens;
// if all those are 0 but TotalTokens > 0, used = TotalTokens and input/output/
// cache stay unknown (a total alone is not a measured zero on the parts).
func (e *Engine) emitUsage(corr protocol.Correlation, u *provider.Usage) {
	if u == nil {
		return
	}
	used := u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens + u.OutputTokens
	input := protocol.KnownTokens(u.InputTokens)
	output := protocol.KnownTokens(u.OutputTokens)
	cacheRead := protocol.KnownTokens(u.CacheReadTokens)
	cacheCreation := protocol.KnownTokens(u.CacheCreationTokens)
	if used == 0 && u.TotalTokens > 0 {
		used = u.TotalTokens
		input = protocol.UnknownTokens()
		output = protocol.UnknownTokens()
		cacheRead = protocol.UnknownTokens()
		cacheCreation = protocol.UnknownTokens()
	}
	source := protocol.UsageSourceActual
	if u.Estimated {
		source = protocol.UsageSourceEstimated
	}
	e.lastUsed = used
	e.lastUsedKnown = true
	e.emit(protocol.UsageReported{
		Correlation:   corr,
		Input:         input,
		Output:        output,
		CacheRead:     cacheRead,
		CacheCreation: cacheCreation,
		Used:          protocol.KnownTokens(used),
		Source:        source,
	})
}

func (e *Engine) toolNames() string {
	var names []string
	for _, s := range e.opts.Registry.Schemas() {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}
