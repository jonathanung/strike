// Package engine is the headless agent runtime: it consumes protocol.Ops,
// runs the model turn loop with tool dispatch, and emits protocol.Events.
// Frontends never call into this package beyond New/Run/Ops/Events.
package engine

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/harness"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/question"
	"github.com/jonathanung/strike-cli/internal/scheduler"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// defaultMaxStreamAttempts is how many times one logical model request may
// call Provider.Stream on retryable failure before the turn fails.
const (
	defaultMaxStreamAttempts = 3
	// absoluteMaxChildDepth is a hard ceiling against runaway nested task swarms.
	absoluteMaxChildDepth = 8
)

// errToolLoopDetected ends the turn after the loop detector trips.
var errToolLoopDetected = errors.New("tool loop detected")

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
	// Harness selects the function used when this agent runs as a task subagent.
	// Empty and "default" use the built-in child model/tool loop.
	// An unknown name falls back to default with a startup error.
	Harness     string
	Permissions permission.Ruleset
}

type Options struct {
	// SessionID is stamped on every emitted event. Empty falls back to a
	// random ID so standalone engine use still has a stable session key.
	SessionID string
	Select    SelectFunc
	Registry  *tool.Registry
	WorkDir   string
	// Verify declares independent completion gates for solo/root turns (and
	// custom harness paths that use the built-in turn loop). When non-empty, a
	// successful claim (stopReason end_turn) runs gates via internal/verify,
	// emits verification.started/completed, and attaches the report on
	// TurnCompleted. Distinct from task/delegate child gates (#780). Model
	// self-report cannot pass a configured gate (#806).
	Verify []tool.VerifyGate
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
	// SandboxMode is the OS process sandbox dial for bash
	// (off|read-only|workspace-write). Empty means workspace-write.
	// Distinct from InitialPermissionMode (when the agent is asked).
	SandboxMode string
	// NetworkAllow is the config network.allow host/CIDR list for webfetch.
	// Empty means unrestricted public hosts. Copied onto tool.Context and
	// sandbox.Policy.NetworkAllow for /sandbox explain.
	NetworkAllow []string
	// AllowYoloWithoutSandbox permits permissionMode yolo when SandboxMode is
	// off. Set only from CLI --i-know after an explicit operator override.
	AllowYoloWithoutSandbox bool
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
	// TurnTimeout bounds each turn independently of the Run parent context.
	// Zero means no per-turn deadline (cancel only via Interrupt / parent ctx).
	// On expiry the turn ends with stopReason "timeout" and tool results use
	// error code timeout when applicable.
	TurnTimeout time.Duration
	// StreamRetryBackoff returns the wait before starting nextAttempt
	// (1-based, >=2). nil uses a small exponential default. Tests may return
	// 0 for instant retries.
	StreamRetryBackoff func(nextAttempt int) time.Duration
	// MaxToolRetryAttempts bounds auto-retries for one tool Execute under the
	// error-code × idempotency policy (includes the first attempt). Zero
	// defaults to 3; set to 1 to disable tool auto-retry. Only safe-retry
	// tools retry on transient/timeout — mutative/unsafe never auto-retry.
	MaxToolRetryAttempts int
	// ToolRetryBackoff returns the wait before tool nextAttempt (1-based, >=2).
	// nil uses exponential backoff with full jitter. Tests may return 0.
	ToolRetryBackoff func(nextAttempt int) time.Duration
	// ToolLoopThreshold is how many identical consecutive failing tool+args
	// trip the loop detector (default 3). Values <1 use the default.
	ToolLoopThreshold int
	// ContextWindow is the selected model's context limit in tokens. Zero
	// means unknown; threshold compaction stays off until LookupContextWindow
	// or a later assignment provides a positive value. Overflow recovery does
	// not require a known window.
	ContextWindow int
	// LookupContextWindow resolves context limits when the provider/model
	// changes. nil skips catalog lookups.
	LookupContextWindow func(provider, model string) int
	// ListModels returns catalog model ids for a provider (same merge as the
	// TUI /model picker: models.dev + providers.jsonc overlays). Used to
	// validate task-tool model pins. nil skips validation (tests).
	ListModels func(ctx context.Context, provider string) ([]string, error)
	// LockModel prevents agent profiles from changing provider/model. Set
	// when the task tool pins a model for a child session.
	LockModel bool
	// LockEffort prevents agent profiles from changing the reasoning dial.
	// Set when the task tool pins effort for a child session.
	LockEffort bool
	// CompactionThreshold is the occupancy fraction (0–1) that triggers
	// automatic compaction before a Stream. Zero defaults to 0.70; >=1
	// disables threshold compaction.
	CompactionThreshold float64
	// CompactionBuffer is extra token headroom reserved with MaxTokens when
	// computing the threshold budget. Zero defaults to 4096.
	CompactionBuffer int
	// KeepUserTurns is how many trailing real user turns to preserve when
	// compacting. Zero defaults to 2.
	KeepUserTurns int
	// PruneProtectTokens is how many recent tool-output tokens to keep intact
	// during continuous tool-result prune. Zero defaults to 40000.
	PruneProtectTokens int
	// PruneMinimumTokens is the minimum estimated tokens freed before prune
	// mutates history. Zero defaults to 20000.
	PruneMinimumTokens int
	// PruneKeepUserTurns skips tool results inside the most recent N real user
	// turns during prune. Zero defaults to 2.
	PruneKeepUserTurns int
	// PruneProtectTools names additional tools whose results stay available
	// after prune (merged with the built-in "skill" protect). Empty adds none.
	PruneProtectTools []string
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
	// LeanCode controls agent-scoped lean-code guidance: off|lite|full.
	// Empty defaults to lite. From config leanCode.
	LeanCode string
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
	// RootSessionID is the top-level session id for this lineage. Empty on
	// construction is filled in New (self when ParentSessionID is empty;
	// otherwise ParentSessionID as a depth-1 fallback). spawnChild always
	// passes the resolved root so nested children keep a stable owner id for
	// plan tools and similar root-owned artifacts.
	RootSessionID string
	// ContextBundle is the sealed context package attached at spawn for this
	// engine (children only). Exposed on tool.Context and via context_bundle;
	// empty means no bundle. Root engines leave this zero.
	ContextBundle tool.ContextBundle
	// Team is the implicit session-scoped agent team (lead + children).
	// Root engines create one in New when nil. Child engines receive the
	// lead's shared pointer from spawnChild so nested descendants enroll on
	// the same roster. See team.go for nested membership policy.
	Team *Team
	// OverlapPolicy is off|warn|block for multi-agent path conflicts
	// (session.overlapPolicy). Empty defaults to warn. Applied to Team
	// ownership when the root team is created or inherited.
	OverlapPolicy string
	// DefaultChildBudget is the session default for per-child limits (#774).
	// Spawn-time task budget fields overlay non-zero values. Zero fields mean
	// unlimited (soft stall/loop signals still apply). Nested under any future
	// session maxSessionCostUSD (#577) outer envelope.
	DefaultChildBudget tool.AgentBudgetLimits
	// PersistSessionMeta, when set, writes durable session metadata (sidecar).
	// The engine emits protocol.SessionMeta after a successful persist.
	PersistSessionMeta func(meta protocol.SessionMeta) error
	// Workflows are named phase sequences (built-in plan-implement plus any
	// loaded from .strike/workflows). Empty falls back to the built-in only.
	Workflows []config.Workflow
	// DefaultWorkflow is entered by enter_plan_mode when set; empty means
	// "plan-implement".
	DefaultWorkflow string
	// PlanStore backs unified plan-mode handoff (validate id/version, approve).
	// nil rejects structured plan_id handoffs; legacy_text and skip-all still work.
	PlanStore PlanStore
	// InitialPlanHandoff restores a prior plan.handoff after session resume.
	InitialPlanHandoff PlanHandoffState
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
	// InitialPhaseWorkflow / InitialPhaseIndex / InitialPhaseName /
	// InitialPhaseFingerprint restore an active workflow phase after agent
	// selection at startup. Empty workflow skips restore. When Fingerprint is
	// set, resume fail-closes (recovery status) if the loaded definition is
	// missing or changed; empty Fingerprint is legacy name-only bind.
	InitialPhaseWorkflow    string
	InitialPhaseIndex       int
	InitialPhaseName        string
	InitialPhaseFingerprint string
	// InitialPhaseGrantApproval restores a prior phase-widening decision so
	// resume does not re-prompt when workflow content is unchanged.
	InitialPhaseGrantApproval PhaseGrantApproval
	// InitialAlwaysGrants restores session DecisionAlways rules after the
	// initial agent profile is applied (SetAgentRules clears grants).
	InitialAlwaysGrants permission.Ruleset
	// DangerouslySkipPermissions mirrors --auto / --dangerously-skip-permissions:
	// workflow phase permission widening is accepted without a review prompt.
	// Hard sandbox and path protections are unchanged. Agent denies still apply
	// via normal evaluation order.
	DangerouslySkipPermissions bool
	// QuietStartup applies Initial* provider/model/effort/autonomy/
	// permission-mode/agent/phase without emitting *Selected or PhaseChanged.
	// Durable resume sets this: the JSONL already has those events and the TUI
	// seeds from Replay. Cleared before the op loop so user-driven changes
	// still emit.
	QuietStartup bool
	// HarnessRegistry maps task-subagent Agent.Harness names to complete agent-run
	// functions. nil means every child uses the built-in loop.
	HarnessRegistry *harness.Registry
	// Scheduler is the process-local admission controller shared across
	// concurrent roots and children. Model streams acquire the model pool;
	// agent bash acquires process (+ build/test when classified). nil disables
	// admission (unlimited; preserves pre-scheduler behavior).
	Scheduler *scheduler.Scheduler
	// SchedulerPolicy is the compiled classification policy for bash commands.
	// nil treats all commands as general (process only). Used only when
	// Scheduler is non-nil.
	SchedulerPolicy *scheduler.Effective
	// FileSync, when set, is invoked after successful file tool mutations
	// (write/edit/apply_patch/notebook_edit) so the host can drive LSP
	// document sync. absPath is absolute; deleted marks removals.
	// Nil disables. Must not panic the tool path (callers recover).
	FileSync func(absPath string, content string, deleted bool)
	// CollectDiagnostics, when set, returns model-facing diagnostic text for
	// touched absolute paths after file mutations (one call per tool result).
	// Empty disables injection. Must not panic the tool path (callers recover).
	CollectDiagnostics func(ctx context.Context, absPaths []string) string
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
	// emitMu serializes emit against Events close so child drain can still
	// publish terminal snapshots (ChildCompleted, team.roster) without racing
	// Run's deferred close.
	emitMu       sync.Mutex
	eventsClosed bool
	perms        *permission.Service
	questions    *question.Service

	// beginReqs is served only by Run so Interrupt stays responsive while a
	// worker needs ToolCallBegin emitted into a full Events buffer.
	beginReqs chan beginReq

	prov     provider.Provider
	provName string
	model    string
	effort   protocol.Effort
	// autonomy is the session exit-gate policy (supervised|agent|checks|skip-all).
	autonomy protocol.Autonomy
	// permMode is the session tool-permission posture dial.
	permMode protocol.PermissionMode
	agent    Agent
	// taskHarness is attached only by spawnChild for the selected child agent.
	taskHarness     harness.Func
	taskHarnessName string
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
	// childHistory retains terminal snapshots for owned children after they
	// finish so task_status/task_read can return completed state without a
	// new spawn. Only sessions this engine started are present.
	childHistory map[string]*childRecord
	// team is the implicit lead+children roster. Shared with descendant
	// engines; only the lead dissolves it on Run exit.
	team *Team

	// childDone delivers ChildCompleted from drain goroutines to Run so the
	// parent can inject a model-visible summary and auto-nudge when idle.
	// Buffered; non-blocking send on the drain side if Run is shutting down.
	childDone chan protocol.ChildCompleted
	// noticeMu guards pendingChildNotices and childWake. Notices are queued
	// from Run and consumed either mid-turn (before the next Stream) or via
	// an idle auto-nudge turn.
	noticeMu            sync.Mutex
	pendingChildNotices []string
	// childWake is closed (and replaced) whenever a child completes so an
	// in-flight sleep can return early instead of poll-looping.
	childWake chan struct{}

	// waitMu guards waitSubs — in-flight wait-tool subscriptions notified on
	// child terminal / needs_attention transitions.
	waitMu   sync.Mutex
	waitSubs map[string]*waitSub

	// pendingUserInputs holds UserInput accepted while a turn was active.
	// Drained FIFO one-at-a-time after each turn ends. Survives Interrupt so
	// follow-up prompts typed mid-turn are not lost.
	pendingUserInputs []pendingUserInput

	// mailbox holds unread peer/team messages for this session. Delivery is
	// at tool-round / turn boundaries (injectPendingMailbox /
	// flushPendingMailbox), never mid-tool-call.
	mailbox *Mailbox
	// mailboxMu guards mailboxWake. Wake is signaled when a peer message is
	// enqueued so idle Run can auto-nudge.
	mailboxMu   sync.Mutex
	mailboxWake chan struct{}

	// pendingAgent is set by tools via SwitchAgent and applied after each tool
	// batch (so the next Stream sees the new agent/prompt) and again in
	// completeTurn if anything remains when the turn ends.
	pendingAgentMu sync.Mutex
	pendingAgent   string

	// workflow/phaseIndex track the active workflow phase (-1 = none).
	// phaseRecovery is non-empty (missing|mismatch) when resume could not bind
	// the fingerprinted definition; permissions are not applied until stop/restart.
	workflow      config.Workflow
	phaseIndex    int
	phaseRecovery string
	// phaseGrantApproval is the last accepted widening decision for the
	// active phase (empty when no widening was needed or phase cleared).
	phaseGrantApproval PhaseGrantApproval

	// planHandoff is the last successful unified plan approval + handoff.
	// Active after exit_plan_mode succeeds; restored from protocol.PlanHandoff.
	planHandoff PlanHandoffState

	// files tracks tool read snapshots so external edits (FilesChanged / /vim)
	// force the model to re-read before edit/write.
	files *tool.FileState

	// checkpoints snapshot pre-mutation file bytes per turn for /undo restore
	// (#540). Composes with turnDiff (per-turn create/update/delete summary)
	// and PathOwnership (#772 overlap leases) — one file-state stack, not forks.
	checkpoints *tool.CheckpointStore

	// turnDiff records harness file change kinds for the active turn
	// (emitted on TurnCompleted.Files for timeline/UI).
	turnDiff *tool.TurnDiff

	// toolLoop tracks repeated failing tool+args within the active turn.
	toolLoop *toolLoopDetector
	// toolLoopStop is set when the detector trips; runTurn ends the turn.
	toolLoopStop string

	// mutatedFiles tracks workspace-relative paths touched by mutating tools
	// this session (for structured child completion handoffs).
	mutatedMu    sync.Mutex
	mutatedFiles map[string]struct{}

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
	if opts.RootSessionID == "" {
		if opts.ParentSessionID == "" {
			opts.RootSessionID = opts.SessionID
		} else {
			// Depth-1 fallback when spawn forgot to set RootSessionID.
			opts.RootSessionID = opts.ParentSessionID
		}
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
	if opts.MaxToolRetryAttempts == 0 {
		opts.MaxToolRetryAttempts = tool.DefaultToolRetryMaxAttempts
	}
	if opts.ToolLoopThreshold < 1 {
		opts.ToolLoopThreshold = tool.DefaultToolLoopThreshold
	}
	if opts.MaxChildDepth == 0 {
		opts.MaxChildDepth = 1
	} else if opts.MaxChildDepth > absoluteMaxChildDepth {
		opts.MaxChildDepth = absoluteMaxChildDepth
	}
	if len(opts.Workflows) == 0 {
		opts.Workflows = []config.Workflow{config.BuiltinPlanImplement()}
	}
	// Implicit team: root owns a new Team; nested engines inherit Options.Team.
	team := opts.Team
	if team == nil && opts.Depth == 0 {
		persona := ""
		if opts.InitialAgent != "" {
			persona = opts.InitialAgent
		} else if len(opts.Agents) > 0 {
			persona = opts.Agents[0].Name
		}
		team = NewTeam(opts.SessionID, persona)
	}
	if team != nil && opts.OverlapPolicy != "" {
		team.SetOverlapPolicy(opts.OverlapPolicy)
	}
	opts.Team = team

	e := &Engine{
		opts:                opts,
		ops:                 make(chan protocol.Op, 16),
		events:              make(chan protocol.Event, 256),
		beginReqs:           make(chan beginReq),
		files:               &tool.FileState{},
		checkpoints:         tool.NewCheckpointStore(),
		turnDiff:            &tool.TurnDiff{},
		toolLoop:            newToolLoopDetector(opts.ToolLoopThreshold, 0),
		children:            make(map[string]*childHandle),
		childHistory:        make(map[string]*childRecord),
		team:                team,
		childDone:           make(chan protocol.ChildCompleted, 32),
		childWake:           make(chan struct{}),
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

// Team returns the implicit session-scoped agent team (may be nil on
// non-lead engines that were constructed without Options.Team).
func (e *Engine) Team() *Team {
	if e == nil {
		return nil
	}
	return e.team
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

func (e *Engine) emit(ev protocol.Event) {
	e.emitMu.Lock()
	defer e.emitMu.Unlock()
	if e.eventsClosed {
		return
	}
	e.events <- ev
}

func (e *Engine) closeEvents() {
	e.emitMu.Lock()
	defer e.emitMu.Unlock()
	if e.eventsClosed {
		return
	}
	e.eventsClosed = true
	close(e.events)
}

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

// rootSessionID returns the lineage root session id for root-owned artifacts
// (plans). Prefer Options.RootSessionID, then team lead, then self when this
// engine is a root.
func (e *Engine) rootSessionID() string {
	if id := strings.TrimSpace(e.opts.RootSessionID); id != "" {
		return id
	}
	if e.team != nil {
		if id := strings.TrimSpace(e.team.LeadID()); id != "" {
			return id
		}
	}
	if strings.TrimSpace(e.opts.ParentSessionID) == "" {
		return e.opts.SessionID
	}
	return strings.TrimSpace(e.opts.ParentSessionID)
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
	// Dissolve the team after children shut down so terminal members stay
	// listable for the lead's lifetime, then clear on lead exit.
	defer e.closeEvents()
	defer e.dissolveTeamIfLead()
	defer e.detachMailbox()
	defer e.shutdownChildren()
	if e.team != nil {
		e.team.AttachMailbox(e)
	}
	e.quietStartup = e.opts.QuietStartup
	if e.opts.InitialProvider != "" && e.opts.Select != nil {
		name := config.CanonicalProviderID(e.opts.InitialProvider)
		if p, defaultModel, err := e.opts.Select(name); err == nil {
			// Same normalization as SelectModel: matching "provider/id" → bare
			// id; foreign prefixes → provider default. Bare ids pass through
			// unchanged (without a catalog we cannot tell a bare foreign id
			// from a valid model name on this provider).
			model := resolveSelectModel(name, e.opts.InitialModel, defaultModel)
			e.setProvider(name, p, model)
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
	// implement/custom phase is not clobbered by plan-mode enterPlanPhase.
	// Fingerprint bind fail-closes into recovery when the def is missing/changed.
	// Then re-seed session always-grants (SetAgentRules cleared them).
	if wf := e.opts.InitialPhaseWorkflow; wf != "" {
		e.restoreWorkflowPhase(wf, e.opts.InitialPhaseIndex, e.opts.InitialPhaseName, e.opts.InitialPhaseFingerprint)
	}
	if e.opts.InitialPlanHandoff.Active || e.opts.InitialPlanHandoff.PlanID != "" ||
		e.opts.InitialPlanHandoff.LegacyText != "" || e.opts.InitialPlanHandoff.ApprovalSource != "" {
		e.restorePlanHandoff(e.opts.InitialPlanHandoff)
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
		mailboxWake := e.mailboxWakeCh()
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
		case <-mailboxWake:
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

func (e *Engine) detachMailbox() {
	if e == nil || e.team == nil {
		return
	}
	e.team.DetachMailbox(e.opts.SessionID)
}

// taskOneShotIdle reports whether a task-spawned engine should exit Run:
// at least one turn finished, no nested children, no active turn, and no
// queued follow-ups (child notices or pending user inputs).
func (e *Engine) taskOneShotIdle(turnSeen bool) bool {
	if !e.opts.TaskOneShot || !turnSeen {
		return false
	}
	if e.turnActive() || e.hasPendingChildNotices() || e.hasPendingMailbox() || len(e.pendingUserInputs) > 0 {
		return false
	}
	e.childMu.Lock()
	n := len(e.children)
	e.childMu.Unlock()
	return n == 0
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

// maxPendingUserInputs caps mid-turn UserInput buffering so a runaway sender
// cannot grow memory without bound. Overflow emits EngineError and drops the
// new item (callers such as the TUI keep the draft on failure).
const maxPendingUserInputs = 32
