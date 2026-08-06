// Package tui is strike's Bubble Tea frontend. It consumes engine events from
// a channel and sends ops back on another (the Go analogue of codex's
// multi-source select loop), and it reaches its host process only through the
// internal/host contract — never internal/auth, config, models, or history
// directly. That boundary (enforced by boundary_test.go) lets the backend
// stage services without touching the UI and lets the UI be exercised against
// fakes. Views are built from internal/tui/ui components and internal/tui/theme
// tokens; no view constructs raw lipgloss boxes or hardcodes a glyph.
package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const (
	composerMinHeight = 2
	composerMaxHeight = 8
	// compactWidth and compactHeight are the breakpoints below which the app
	// drops panel borders and renders the plain viewport+composer ("compact
	// mode"), keeping every function but shedding chrome that will not fit.
	compactWidth  = 60
	compactHeight = 20
)

type noticeCause uint8

const (
	noticeGeneral noticeCause = iota
	noticeNeedsModel
)

// engineEventMsg wraps a protocol.Event for the Update loop. Engine events
// and UI-internal messages stay distinct types on one loop.
type engineEventMsg struct {
	ev protocol.Event
}

type engineClosedMsg struct{}

// activateRootMsg is sent by the root switcher modal to activate a session.
type activateRootMsg struct {
	id string
}

// defaultsSavedMsg reports the outcome of a ctrl+d "save as default".
type defaultsSavedMsg struct {
	text string
	err  error
}

// cellClipboard holds a one-shot OSC52 sequence staged by y-to-copy. Model.View
// prepends and clears it after Canvas so ansi.Cut cannot strip the sequence.
type cellClipboard struct {
	osc string
}

func (c *cellClipboard) stage(text string) {
	if c == nil || text == "" {
		return
	}
	c.osc = ansi.SetSystemClipboard(text)
}

func (c *cellClipboard) take() string {
	if c == nil || c.osc == "" {
		return ""
	}
	osc := c.osc
	c.osc = ""
	return osc
}

// clearCellCopiedFlashMsg ends the brief "copied" flash on a transcript cell.
type clearCellCopiedFlashMsg struct {
	idx int
	gen int
}

type historyAddedMsg struct {
	err error
}

// goalFinishedMsg is delivered when an async /goal run completes.
type goalFinishedMsg struct {
	goal host.Goal
	err  error
	op   string
}

// Options carries frontend-only construction flags. Host capabilities
// (credentials, catalog, settings, history, agents, skills) arrive through
// host.Services instead.
type Options struct {
	DangerouslySkipPermissions bool
	Theme                      *theme.Theme
	// ThemeID is the catalog id of Theme (e.g. "strike", "dracula"). Empty
	// defaults to theme.BuiltinID when Theme is nil.
	ThemeID   string
	SessionID string
	// WorkDir is display identity for the context pane and resolves relative
	// paths for /vim. Empty falls back to os.Getwd at launch time.
	WorkDir string
	// FirstRun forces first-run onboarding UI (welcome card + auto /ftue).
	// When false, New also enables first-run when services.Onboarding reports
	// ShouldAutoOpen (global unacknowledged state). Tests may set this without
	// wiring Onboarding.
	FirstRun bool
	// StartupAlert is a one-shot dismissible modal body shown after Init
	// (e.g. session worktree soft-fail outside a git repository). Empty skips.
	StartupAlert string
	// VimMode selects pane/overlay/takeover for /vim (aliases embedded/modal).
	// Empty defaults to pane.
	VimMode VimMode
	// NanoMode selects pane/overlay/takeover for /nano (aliases embedded/modal).
	// Empty defaults to pane. Same vocabulary as VimMode.
	NanoMode NanoMode
	// MdReadMode selects embedded|modal for /md-read. Empty defaults to embedded.
	MdReadMode SurfacePresentation
	// PermissionAutoApproveSeconds arms permission-modal auto-allow once after
	// N seconds. Zero disables (default). Clamped by the host before wiring.
	PermissionAutoApproveSeconds int
	// PermissionAutoApproveExclude lists permission names that never auto-allow.
	PermissionAutoApproveExclude []string
	// NotifyMode selects desktop notifications: on, off, or unfocused-only
	// (default). Wired from config.notify.
	NotifyMode NotifyMode
	// SandboxMode is the resolved OS sandbox dial (off|read-only|workspace-write).
	// Empty means workspace-write. Displayed by /sandbox; not mid-session mutable.
	SandboxMode string
	// SandboxBackend is the platform launcher name ("bwrap", "sandbox-exec") or
	// empty when unavailable. Displayed by /sandbox.
	SandboxBackend string
	// SandboxAvailable reports whether the OS sandbox backend can run.
	SandboxAvailable bool
	// SandboxExplain is the multi-line generated profile text for /sandbox explain.
	// Compiled from config permission layers at process start.
	SandboxExplain string
	// Replay is a prior session event log for --continue / --session. Seeded
	// via cellsFromEvents + silent selection/child state — never fed through
	// applyEvent (avoids stuck turns, zombie permission modals, orphan children).
	Replay []protocol.Event
	// Keybinds are config overrides (binding id → key sequences). Applied on
	// top of defaultKeyMap at startup; /keys and footer hints show the result.
	Keybinds map[string][]string
	// Telemetry keeps the local system metrics pane (CPU/RAM/disk) and its
	// ~1 Hz sampler on at launch. On by default; toggled via /telemetry on|off.
	Telemetry bool
}

// firstRunSetupMsg opens the /ftue setup wizard once when onboarding is due.
type firstRunSetupMsg struct{}

// onboardingAckMsg is the result of persisting global onboarding acknowledgement.
type onboardingAckMsg struct {
	err error
}

// contextLimitsMsg delivers catalog context-window, output-limit, and optional
// pricing lookups for a provider/model pair. Applied only when that pair is
// still selected.
type contextLimitsMsg struct {
	provider, model string
	contextTokens   int
	contextOK       bool
	outputTokens    int
	outputOK        bool
	attachment      bool
	attachmentOK    bool
	inputCost       float64
	outputCost      float64
	hasCost         bool
}

// Model is the root Bubble Tea model. It holds its host services, the
// transcript cells, the active modal, and composer/viewport state.
type Model struct {
	ops    chan<- protocol.Op
	events <-chan protocol.Event
	// services is the only channel to host effects (credentials, model
	// catalog, saved defaults, prompt history). It is deliberately not part
	// of the engine protocol: these are frontend/host concerns, not
	// agent-loop state.
	services host.Services

	th       theme.Theme
	themeID  string // catalog id of th
	cells    []cell
	toolByID map[string]*toolCell
	// selectedCell is the index into cells for tool/explore selection
	// (-1 = none). Targets collapsible tool/explore cells and reviewable
	// file-mutating tools (edit/write/…).
	selectedCell int
	// transcriptPlainLines mirrors viewport content without ANSI, used for
	// path:line hit-testing (open-at-line).
	transcriptPlainLines []string
	// vpCache holds per-cell rendered transcript blocks so refreshViewport
	// only re-renders dirty cells (typically the streaming tail).
	vpCache viewportCache
	// selectedFileRef is set when the user click-selects a path:line citation
	// (-1 = none). Empty-composer enter opens it when no tool expand applies.
	selectedFileRef int
	// cellClip stages one-shot OSC52 for y-to-copy (pointer so value-receiver
	// View can clear it). Never nil after New.
	cellClip *cellClipboard
	// paint tracks View/refresh/cell render counters (#495) and FPS-coalesce
	// state for soft TextDelta/spinner paints (#496). Pointer so value-receiver
	// View can mutate. Never nil after New.
	paint *paintBudget
	// textSel is app-owned mouse highlight (transcript + prompt only).
	textSel textSel
	// copyFlashGen invalidates in-flight clearCellCopiedFlashMsg timers.
	copyFlashGen int
	// modal is the visible top overlay. modalQueue holds blocking asks that
	// arrived while a user modal was open (or behind a higher-priority peer).
	modal      modal
	modalQueue []modal

	viewport viewport.Model
	composer textarea.Model
	// pendingPastes holds full text for collapsed large-paste chips in the
	// composer. Expanded on send; pruned when the chip leaves the value.
	pendingPastes []pasteChip
	// pendingImages holds image attachments for [image N] chips in the
	// composer. Sent as multimodal UserInput; pruned when the chip leaves.
	pendingImages  []imageChip
	clipboardImage func() ([]byte, error)
	completion     *completionState
	keyMap         keyMap
	// keyOverrides are config keybind remaps (id → chords); used to rebuild
	// keyMap on orientation toggle and /keys reset.
	keyOverrides               map[string][]string
	focus                      paneFocus
	windows                    windowRegistry
	commands                   []commandSpec
	spin                       spinner.Model
	entries                    []string
	historyPos                 int
	historyDraft               string
	dangerouslySkipPermissions bool
	// permissionAutoApproveSeconds > 0 arms countdown auto-allow on asks.
	permissionAutoApproveSeconds int
	permissionAutoApproveExclude []string

	providerName string
	modelName    string
	agentName    string
	// phaseName is the active workflow phase (empty = none); shown in header.
	phaseName     string
	phaseWorkflow string
	effort        protocol.Effort
	// autonomy is the session exit-gate policy; default supervised.
	autonomy protocol.Autonomy
	// permMode is the session tool-permission posture dial; default default.
	permMode protocol.PermissionMode
	// sandboxMode is the process OS sandbox dial (config/CLI); default workspace-write.
	sandboxMode string
	// sandboxBackend is bwrap|sandbox-exec|"" for /sandbox status.
	sandboxBackend string
	// sandboxAvailable is whether the OS backend can apply isolation.
	sandboxAvailable bool
	// sandboxExplain is /sandbox explain body (config-compiled profile).
	sandboxExplain string
	// fastEnabled is the session priority-tier preference from /fast.
	fastEnabled bool
	// showThinking shows reasoning/CoT cells in the transcript (/think).
	// Default false keeps the transcript clean (answer + tools only).
	showThinking bool
	agents       []string     // cycled with tab
	skills       []host.Skill // slash-command templates, pre-filtered by the host
	notice       string
	noticeErr    bool
	noticeCause  noticeCause
	turnRunning  bool
	// queue* projects scheduler.queued/admitted/canceled for the active root
	// so chrome/activity identify the constrained pool (not idle).
	queueRequestID string
	queuePools     []string
	queueLabel     string
	// inputQueue holds prompts typed while turnRunning. Drained FIFO on
	// TurnCompleted; survives Interrupt until the user pops/clears it.
	inputQueue []queuedInput
	// awaitingPermission is true between PermissionAsked/QuestionAsked and
	// the matching Resolved / TurnCompleted. It drives AgentStateAttention.
	awaitingPermission bool
	// sessionErrored is sticky error coloring after a failed turn or an
	// idle-state EngineError, cleared on the next accepted user turn.
	sessionErrored bool
	width          int
	height         int
	ready          bool

	// sessionID and workDir are display identity for the header cwd label and
	// the context pane. workDir also resolves relative paths for /vim.
	sessionID string
	workDir   string
	// pendingResume is set when /session picks another root session; the
	// composition root reads PendingResume after tea.Quit and reopens it.
	pendingResume string
	// pendingUpgrade is set by /upgrade; the composition root runs self-update
	// after tea.Quit (alt screen torn down) and re-execs the new binary.
	pendingUpgrade bool
	// vimMode selects pane/overlay/takeover for /vim.
	vimMode VimMode
	// nanoMode selects pane/overlay/takeover for /nano.
	nanoMode NanoMode
	// mdReadMode selects embedded|modal for /md-read.
	mdReadMode SurfacePresentation
	// usage* hold the latest UsageReported figures; Known=false means unknown
	// (never treat as measured zero). Limits come from the host catalog.
	// usageSession accumulates session totals for /cost (including resume).
	usageInput, usageOutput, usageCacheRead, usageCacheCreation, usageUsed protocol.TokenCount
	usageSource                                                            string
	usageSession                                                           usageTotals
	modelInputCost, modelOutputCost                                        float64
	modelHasCost                                                           bool
	contextLimit                                                           int
	contextLimitKnown                                                      bool
	outputLimit                                                            int
	outputLimitKnown                                                       bool
	// modelAttachment caches catalog multimodal support for the selection.
	modelAttachment      bool
	modelAttachmentKnown bool
	// pendingContextDoctor opens the doctor modal on the next EffectivePrompt.
	pendingContextDoctor bool
	// vizFocusID is the agents-tree node the visualizer follows (cursor or
	// last open). Empty falls back to viewingID / sessionID.
	vizFocusID string

	// firstRun drives the empty-transcript onboarding card and auto /ftue modal.
	firstRun, firstRunModalOpened bool
	// testForceMultiPane disables the pre-first-prompt home layout so unit
	// tests can exercise the multi-pane session surface without seeding a
	// fake user message. Production always leaves this false (#677).
	testForceMultiPane bool
	// homePanesOpen is set when the user opens the right pane column from the
	// lean home screen (ctrl+l / focus-right / pane jump). Sticky until the
	// first transcript cell ends home anyway (#684).
	homePanesOpen bool
	// startupAlert is consumed once into an alertModal after Init.
	startupAlert string
	// turnStartedAt / toolCallsThisTurn power the working-status elapsed label.
	turnStartedAt     time.Time
	toolCallsThisTurn int
	authExpiryNoticed bool
	focused           bool // terminal focus; default true until BlurMsg
	focusKnown        bool // true after first FocusMsg/BlurMsg from the terminal
	notifyMode        NotifyMode
	titleTopic        string

	// splitOrientation is horizontal (left|right) by default; vertical stacks
	// the left body above the right pane (top/bottom).
	splitOrientation splitOrientation
	// appearance is session-local auto|dark|light. detectedDark is the terminal
	// background from tea.BackgroundColorMsg (provisional until the first msg).
	appearance   appearanceMode
	detectedDark bool
	// children tracks active/recent subagent sessions for the activity pane.
	// Lifecycle never appends transcript cells.
	children []childActivity
	// teamMessages is a bounded ring of recent peer mailbox deliveries for the
	// lead activity pane (agent.message). Oldest drop under broadcast storms.
	teamMessages []teamMessage
	// activityCursor / activityAnchorID navigate the newest-first activity feed.
	// activityStickNewest keeps the cursor on the newest row as events arrive.
	// activityDetail expands one entry's chronological body.
	activityCursor      int
	activityAnchorID    string
	activityStickNewest bool
	activityDetail      bool

	// roots holds frozen UI state for concurrent parent sessions (multi-root).
	// The active root's fields live on Model; others sit here until activated.
	roots map[string]*rootPane

	// agentsHidden is an ephemeral Agents-pane filter: session ids dismissed
	// from the tree without deleting JSONL, interrupting turns, or changing
	// /session. Cleared when a hidden root becomes busy or is activated again.
	agentsHidden map[string]bool

	// Subagent transcript navigation (ctrl+x leader chords). Root live
	// cells stay in cells/toolByID; viewingID non-empty and != sessionID
	// shows viewCells loaded from host.Sessions.
	leaderArmed  bool
	leaderGen    int
	viewingID    string
	viewParentID string
	viewTitle    string
	viewCells    []cell
	viewToolByID map[string]*toolCell
	viewGen      int // bumps on open/close to cancel refresh ticks

	// killBuf holds the last composer kill (ctrl+w/u/k) for ctrl+y yank.
	killBuf string

	// loops are session-scoped /loop schedules (canceled on quit; not persisted).
	loops   []scheduledLoop
	loopSeq int

	// frames caches compose layers for dirty-mask skip (#494). Never nil after New.
	frames *frameCache
}

// childActivity is one foreground subagent row in the activity/agents panes.
type childActivity struct {
	sessionID string
	parentID  string // spawning session; empty means direct root child
	agent     string
	prompt    string
	name      string // optional stable teammate alias from task spawn
	title     string // durable display title when known (user rename / create)
	status    string // running | completed | failed | canceled
	// rosterState is a short display chip from team.roster (working, needs you, …).
	rosterState string
	// queue* projects scheduler.queued/admitted/canceled (constrained pool, not idle).
	queueRequestID string
	queuePools     []string
	queueLabel     string
	startedAt      time.Time
	endedAt        time.Time
}

// New builds the frontend model. services supplies every host capability; any
// field of it may be nil/empty and the UI degrades gracefully. Options is
// variadic for backward-compatible call sites.
func New(ops chan<- protocol.Op, events <-chan protocol.Event, services host.Services, options ...Options) Model {
	th := theme.Default()
	themeID := theme.BuiltinID
	for _, option := range options {
		if option.Theme != nil {
			th = *option.Theme
		}
		if option.ThemeID != "" {
			themeID = option.ThemeID
		}
	}
	th = th.Resolve()
	ta := newComposer(th)
	sp := newSpinner(th)

	m := Model{
		ops:                 ops,
		events:              events,
		services:            services,
		agents:              services.Agents,
		skills:              services.Skills,
		commands:            commandCatalog(services.Skills),
		th:                  th,
		themeID:             themeID,
		toolByID:            map[string]*toolCell{},
		selectedCell:        -1,
		selectedFileRef:     -1,
		cellClip:            &cellClipboard{},
		paint:               &paintBudget{},
		frames:              newFrameCache(),
		composer:            ta,
		keyMap:              defaultKeyMap(),
		windows:             newWindowRegistry(),
		spin:                sp,
		historyPos:          -1,
		activityStickNewest: true,
		focused:             true,
		notifyMode:          NotifyUnfocusedOnly,
		appearance:          appearanceAuto,
		detectedDark:        true, // until BackgroundColorMsg; matches lipgloss default
		autonomy:            protocol.AutonomySupervised,
		permMode:            protocol.PermissionModeDefault,
		sandboxMode:         "workspace-write",
	}
	m.applyAppearance()
	var replay []protocol.Event
	for _, option := range options {
		m.dangerouslySkipPermissions = option.DangerouslySkipPermissions
		if option.SessionID != "" {
			m.sessionID = option.SessionID
		}
		if option.WorkDir != "" {
			m.workDir = option.WorkDir
		}
		if option.FirstRun {
			m.firstRun = true
		}
		if option.StartupAlert != "" {
			m.startupAlert = option.StartupAlert
		}
		if option.VimMode != "" {
			m.vimMode = option.VimMode
		}
		if option.NanoMode != "" {
			m.nanoMode = option.NanoMode
		}
		if option.MdReadMode != "" {
			m.mdReadMode = option.MdReadMode
		}
		if option.NotifyMode != "" {
			m.notifyMode = option.NotifyMode
		}
		if option.SandboxMode != "" {
			// Apply backend/availability with mode so a later partial Options
			// (e.g. WorkDir-only) cannot clear a prior sandbox wiring.
			m.sandboxMode = option.SandboxMode
			m.sandboxBackend = option.SandboxBackend
			m.sandboxAvailable = option.SandboxAvailable
			m.sandboxExplain = option.SandboxExplain
		}
		if option.PermissionAutoApproveSeconds != 0 {
			m.permissionAutoApproveSeconds = option.PermissionAutoApproveSeconds
		}
		if option.PermissionAutoApproveExclude != nil {
			m.permissionAutoApproveExclude = option.PermissionAutoApproveExclude
		}
		if len(option.Replay) > 0 {
			replay = option.Replay
		}
		if len(option.Keybinds) > 0 {
			m.keyOverrides = cloneKeybindMap(option.Keybinds)
		}
	}
	m.keyMap = buildKeyMap(m.keyOverrides, m.splitOrientation)
	if m.vimMode == "" {
		m.vimMode = VimModePane
	}
	if m.nanoMode == "" {
		m.nanoMode = VimModePane
	}
	if m.mdReadMode == "" {
		m.mdReadMode = PresentationEmbedded
	}
	if m.notifyMode == "" {
		m.notifyMode = NotifyUnfocusedOnly
	}
	// Host onboarding state drives auto-open when Options.FirstRun was not set.
	// Only interactive TUI calls ShouldAutoOpen (may migrate established installs).
	if !m.firstRun && services.Onboarding != nil && services.Onboarding.ShouldAutoOpen() {
		m.firstRun = true
	}
	if services.History != nil {
		m.entries = services.History.Entries()
	}
	m.windows = configureFilesWindow(m.windows, m.workDir, m.services.Files)
	m.windows = configureDiagnosticsWindow(m.windows, m.workDir, m.services.LSP)
	m.windows = configureMemoryWindow(m.windows, m.services.Memory)
	m.windows = configureIssuesWindow(m.windows, m.services.Issues)
	m.windows = configureTelemetryWindow(m.windows, m.workDir, m.services.Telemetry)
	// Telemetry is on by default (newTelemetryWindow). Options.Telemetry only
	// forces on when callers pass it; Init() arms the sampler via windows.init().
	for _, option := range options {
		if option.Telemetry {
			m.windows, _ = setTelemetryEnabled(m.windows, true)
			break
		}
	}
	if len(replay) > 0 {
		seedFromReplay(&m, replay)
	}
	return m
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		textarea.Blink,
		m.listen(),
		// Spinner ticks only while Working (spinTickCmd); idle init must not
		// redraw the full frame at spinner FPS over SSH (#481).
		m.spinTickCmd(),
		m.windows.init(),
		// Kitty/Ghostty keep separate keyboard stacks per screen; re-enable
		// after alt-screen enter so shift+enter CSI is actually delivered (#187).
		enableEnhancedKeysCmd(),
		// Terminal bg via Bubble Tea (not pre-program OSC 11); feeds appearance.
		tea.RequestBackgroundColor,
	}
	if m.startupAlert != "" {
		cmds = append(cmds, func() tea.Msg { return startupAlertMsg{} })
	} else if m.firstRun {
		// Defer first-run picker when a startup alert will claim the modal first.
		cmds = append(cmds, func() tea.Msg { return firstRunSetupMsg{} })
	}
	if notice := m.authExpiryNoticeCmd(); notice != nil {
		cmds = append(cmds, notice)
	}
	return tea.Batch(cmds...)
}

// authExpiryNoticeCmd returns a one-shot notice when the selected provider's
// OAuth credential expires within authExpiryWarn. The noticed flag is set when
// the resulting message is applied so Init (value receiver) stays correct.
func (m Model) authExpiryNoticeCmd() tea.Cmd {
	if m.authExpiryNoticed || m.providerName == "" || m.services.Auth == nil {
		return nil
	}
	for _, s := range m.services.Auth.Statuses() {
		if s.Name != m.providerName {
			continue
		}
		if s.ExpiresAt.IsZero() || time.Until(s.ExpiresAt) >= authExpiryWarn {
			return nil
		}
		return func() tea.Msg { return authExpiryNoticeMsg{} }
	}
	return nil
}

// authExpiryNoticeMsg applies the auth-expiring notice on the update path.
type authExpiryNoticeMsg struct{}

// listen waits for the next engine event; re-issued after each delivery.
func (m Model) listen() tea.Cmd {
	events := m.events
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return engineClosedMsg{}
		}
		return engineEventMsg{ev: ev}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	// Default: full recompose. Spinner may markFrameSkip for unchanged layers.
	m.clearFrameSkip()

	// Default: immediate full-frame paint. Soft paths (TextDelta, spinner)
	// re-arm FPS coalesce before return (#496).
	switch msg.(type) {
	case paintFlushMsg:
		// handled below
	default:
		if !softCoalesceMsg(msg) {
			m.markImmediatePaint()
		}
	}

	switch msg := msg.(type) {
	case paintFlushMsg:
		m.applyPaintFlush()
		return m, nil

	case tea.BackgroundColorMsg:
		m.detectedDark = msg.IsDark()
		m.applyAppearance()
		m.restyleWidgets()
		m.reflow()
		m.refreshViewport()
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = max(0, msg.Width), max(0, msg.Height)
		firstReady := !m.ready
		if !m.ready {
			m.viewport = viewport.New(viewport.WithWidth(max(1, m.width)), viewport.WithHeight(0))
			m.ready = true
		}
		m.reflow()
		m.refreshViewport()
		var cmd tea.Cmd
		if firstReady {
			cmd = tea.Batch(m.broadcastContextState(), m.broadcastAgentsState())
		}
		return m, cmd

	case engineClosedMsg:
		return m, tea.Quit

	case activateRootMsg:
		cmd := m.activateRoot(msg.id)
		if cmd == nil {
			return m, nil
		}
		return m, tea.Batch(cmd, rightPanePollCmd(m.windows))

	case rewindForkMsg:
		return m.applyRewindFork(msg.keepEvents)
	case sessionResumeMsg:
		id := strings.TrimSpace(msg.id)
		if id == "" || id == m.sessionID {
			return m, nil
		}
		// Prefer in-process multi-root open when the host supports it.
		if m.services.Roots != nil {
			cmd := m.openRootInProcess(id)
			// openRootInProcess stashes/restores per-root overlay state.
			m.reflow()
			return m, cmd
		}
		if m.turnRunning {
			m.setNotice("wait for the current turn to finish before switching sessions", true)
			return m, nil
		}
		m.pendingResume = id
		m.clearModalStack()
		return m, tea.Quit

	case engineEventMsg:
		rootID := m.rootForEvent(msg.ev)
		var cmd tea.Cmd
		if rootID != "" && rootID != m.sessionID {
			cmd = m.applyEventToRoot(rootID, msg.ev)
		} else {
			cmd = m.applyEvent(msg.ev)
		}
		m.reflow()
		m.refreshViewport()
		var softCmd tea.Cmd
		if softCoalesceEvent(msg.ev) {
			softCmd = m.coalesceSoftPaint()
		}
		return m, tea.Batch(m.listen(), cmd, softCmd)

	case permissionCountdownMsg:
		pm, ok := m.modal.(*permissionModal)
		if !ok {
			// Hidden/queued asks must not auto-approve from stale ticks.
			return m, nil
		}
		var cmd tea.Cmd
		m.modal, cmd = pm.onCountdown(msg)
		if m.modal == nil {
			cmd = tea.Batch(cmd, m.afterModalClosed())
			m.refreshAwaitingPermission()
		}
		m.reflow()
		return m, cmd

	case contextLimitsMsg:
		if msg.provider != m.providerName || msg.model != m.modelName {
			return m, nil
		}
		m.contextLimit = msg.contextTokens
		m.contextLimitKnown = msg.contextOK
		m.outputLimit = msg.outputTokens
		m.outputLimitKnown = msg.outputOK
		m.modelAttachment = msg.attachment
		m.modelAttachmentKnown = msg.attachmentOK
		m.modelInputCost = msg.inputCost
		m.modelOutputCost = msg.outputCost
		m.modelHasCost = msg.hasCost
		return m, m.broadcastContextState()

	case spinner.TickMsg:
		// Drop the tick chain when idle (#481) or static working chrome (#497)
		// so SSH sessions are not redrawn at spinner FPS without engine events.
		if m.agentState() != theme.AgentStateWorking || staticWorkingChrome() {
			// Keep cached frame; idle/static ticks must not force full rebuilds.
			if p := m.ensurePaint(); p.lastFrame != "" {
				p.suppress = true
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		// Header spinner/elapsed only when a prior View warmed the layer cache
		// (frames.width set by storeGeo). Cold cache fully recomposes (#494).
		if m.frames != nil && m.frames.width > 0 {
			m.markFrameSkip(dirtyLeft | dirtyRight | dirtyFooter)
		}
		// FPS-cap full Canvas rebuilds even when dirty-mask trims layers (#496).
		return m, tea.Batch(cmd, m.coalesceSoftPaint())

	case projectDataMutatedMsg:
		cmd := m.applyProjectDataMutation(msg)
		m.reflow()
		return m, cmd

	case customProviderSavedMsg:
		if msg.err != nil {
			if form, ok := m.modal.(*customProviderFormModal); ok {
				form.err = msg.err.Error()
				return m, nil
			}
			m.setNotice("provider save failed: "+msg.err.Error(), true)
			return m, nil
		}
		m.setNotice("saved provider "+msg.name, false)
		if msg.returnTo != nil {
			m.modal = msg.returnTo
			if sm, ok := m.modal.(*settingsModal); ok {
				sm.reload()
			}
			return m, nil
		}
		m.modal = nil
		return m, m.afterModalClosed()
	case customProviderRemovedMsg:
		if msg.err != nil {
			m.setNotice("remove failed: "+msg.err.Error(), true)
			return m, nil
		}
		m.setNotice("removed provider "+msg.name, false)
		if sm, ok := m.modal.(*settingsModal); ok {
			sm.reload()
		}
		return m, nil
	case authStartedMsg, authDeviceMsg, authPasteErrMsg, authDoneMsg:
		cmd, _ := m.applyAuthMsg(msg)
		m.reflow()
		return m, cmd

	case providerLogoutMsg:
		if pm, ok := m.modal.(*providerModal); ok {
			pm.reloadStatuses()
		}
		switch {
		case msg.err != nil:
			m.setNotice("logout failed: "+msg.err.Error(), true)
		case msg.removed:
			m.setNotice("removed custom provider "+msg.provider, false)
		default:
			m.setNotice("logged out of "+msg.provider, false)
		}
		m.reflow()
		return m, nil

	case modelsLoadedMsg:
		if mm, ok := m.modal.(*modelModal); ok && mm.provider == msg.fallback {
			mm.loading = false
			if msg.err != nil {
				mm.loadErr = msg.err.Error()
			} else {
				mm.all = msg.models
			}
		}
		return m, nil

	case effortChoicesLoadedMsg:
		if msg.modal != nil {
			m.modal = msg.modal
		}
		return m, nil

	case defaultsSavedMsg:
		if msg.err != nil {
			m.setNotice("saving defaults failed: "+msg.err.Error(), true)
		} else {
			m.setNotice("saved as default: "+msg.text, false)
		}
		return m, nil

	case themeSelectedMsg:
		m.applyThemeEntry(msg.entry)
		m.setNotice("theme: "+msg.entry.ID, false)
		return m, nil

	case themeSavedMsg:
		if msg.err != nil {
			m.setNotice("saving theme failed: "+msg.err.Error(), true)
		} else {
			m.setNotice("saved theme default: "+msg.id, false)
		}
		return m, nil

	case settingsSavedMsg:
		if sm, ok := m.modal.(*settingsModal); ok {
			sm.afterSettingsSaved(msg)
		}
		if msg.err != nil {
			m.setNotice("saving settings failed: "+msg.err.Error(), true)
			return m, nil
		}
		if msg.apply.theme != nil {
			m.applyThemeEntry(*msg.apply.theme)
		}
		if msg.apply.hasVim {
			m.vimMode = msg.apply.vimMode
		}
		if msg.apply.hasNano {
			m.nanoMode = msg.apply.nanoMode
		}
		if msg.apply.hasMd {
			m.mdReadMode = msg.apply.mdReadMode
		}
		if msg.apply.hasNotify {
			m.notifyMode = msg.apply.notifyMode
		}
		label := msg.label
		if label == "" {
			label = msg.value
		}
		m.setNotice("saved default: "+label, false)
		m.reflow()
		return m, nil

	case historyAddedMsg:
		if msg.err != nil {
			m.setNotice("saving prompt history failed: "+msg.err.Error(), true)
		} else if m.services.History != nil {
			m.entries = m.services.History.Entries()
		}
		return m, nil

	case goalFinishedMsg:
		if msg.err != nil {
			m.setNotice("goal: "+msg.err.Error(), true)
			return m, nil
		}
		m.setNotice("goal: "+formatGoalStatus(msg.goal), false)
		return m, nil

	case loopTickMsg:
		return m.applyLoopTick(msg)

	case editorFinishedMsg:
		return m.applyEditorFinished(msg)

	case composerEditorFinishedMsg:
		return m.applyComposerEditorFinished(msg)

	case exportFinishedMsg:
		return m.applyExportFinished(msg)

	case terminalOutputMsg:
		return m.applyTerminalOutput()

	case terminalExitMsg:
		return m.applyTerminalExit(msg)

	case firstRunSetupMsg:
		if m.firstRun && !m.firstRunModalOpened && m.modal == nil && len(m.cells) == 0 {
			m.firstRunModalOpened = true
			m.modal = newFTUEModal(m.services, m.providerName, m.modelName, m.th)
			m.reflow()
		}
		return m, nil

	case startupAlertMsg:
		if text := strings.TrimSpace(m.startupAlert); text != "" && m.modal == nil {
			m.startupAlert = ""
			m.modal = newAlertModal("Session worktree", text, ui.ToneWarning)
			m.reflow()
		}
		return m, nil

	case alertDismissedMsg:
		// Startup alert may have deferred first-run /ftue setup.
		if m.firstRun && !m.firstRunModalOpened && m.modal == nil && len(m.cells) == 0 {
			m.firstRunModalOpened = true
			m.modal = newFTUEModal(m.services, m.providerName, m.modelName, m.th)
			m.reflow()
		}
		return m, nil

	case onboardingAckMsg:
		m.firstRun = false
		if msg.err != nil {
			m.setNotice("could not save onboarding state: "+msg.err.Error(), true)
		}
		return m, nil

	case ftueSpawnChildMsg:
		cmd := m.applyFTUESpawnChild(msg)
		m.reflow()
		return m, cmd

	case ftueFinishedMsg:
		cmd := m.applyFTUEFinished()
		m.reflow()
		return m, cmd

	case tourClosedMsg:
		cmd := m.applyTourClosed(msg)
		m.reflow()
		return m, cmd

	case schedulerPresetsAppliedMsg:
		cmd := m.applySchedulerPresetsApplied(msg)
		m.reflow()
		return m, cmd

	case initResultMsg:
		return m.applyInitResult(msg)

	case bangResultMsg:
		return m.applyBangResult(msg)

	case applyDiffResultMsg:
		cmd := m.applyApplyDiffResult(msg)
		return m, cmd

	case inputQueueRunNextMsg:
		if _, ok := m.modal.(*queueModal); ok {
			m.modal = nil
			promote := m.afterModalClosed()
			cmd := m.interruptToNextQueued()
			m.reflow()
			return m, tea.Batch(promote, cmd)
		}
		cmd := m.interruptToNextQueued()
		m.reflow()
		return m, cmd

	case inputQueueEditComposerMsg:
		if _, ok := m.modal.(*queueModal); ok {
			m.modal = nil
			_ = m.afterModalClosed()
		}
		m.applyInputQueueEditComposer(msg.remaining, msg.text)
		return m, m.setPaneFocus(focusLeft)

	case authExpiryNoticeMsg:
		if m.authExpiryNoticed {
			return m, nil
		}
		m.authExpiryNoticed = true
		m.setNotice("auth expiring — run /auth", false)
		return m, nil

	case clearCellCopiedFlashMsg:
		if msg.gen != m.copyFlashGen {
			return m, nil
		}
		cells := m.displayCells()
		if msg.idx >= 0 && msg.idx < len(cells) {
			clearCellCopiedFlash(cells[msg.idx])
			m.reflow()
		}
		return m, nil

	case tea.FocusMsg:
		m.focused = true
		m.focusKnown = true
		m.windows = refreshFilesWindows(m.windows)
		m.windows = refreshDiagnosticsWindows(m.windows)
		return m, nil

	case tea.BlurMsg:
		m.focused = false
		m.focusKnown = true
		return m, nil

	case filesRefreshMsg:
		// Poll only while the files pane is active; session init keeps
		// context/activity visible without a 1 Hz full-frame redraw (#481).
		if !filesWindowActive(m.windows) {
			return m, nil
		}
		m.windows = refreshFilesWindows(m.windows)
		return m, filesRefreshCmd()

	case diagnosticsRefreshMsg:
		if !diagnosticsWindowActive(m.windows) {
			return m, nil
		}
		m.windows = refreshDiagnosticsWindows(m.windows)
		return m, diagnosticsRefreshCmd()

	case telemetryTickMsg, telemetrySampleMsg:
		var cmd tea.Cmd
		m.windows, cmd = applyTelemetryMsg(m.windows, msg)
		return m, cmd

	case paletteInvokeMsg:
		priorNotice, priorNoticeErr := m.notice, m.noticeErr
		entry, ok := m.currentPaletteEntry(msg.Action)
		if !ok {
			m.refreshOpenPalette()
			m.setNotice("palette action is no longer available", true)
			return m.paletteResultFocus(priorNotice, priorNoticeErr, nil)
		}
		if entry.DisabledReason != "" {
			m.refreshOpenPalette()
			if entry.DisabledReason == "select a provider first" {
				m.setNeedsModelNotice(entry.DisabledReason, true)
			} else {
				m.setNotice(entry.DisabledReason, true)
			}
			return m.paletteResultFocus(priorNotice, priorNoticeErr, nil)
		}
		if _, paletteOpen := m.modal.(*paletteModal); paletteOpen {
			m.modal = nil
		}
		composer := m.snapshotComposer()
		switch msg.Action.Kind {
		case paletteActionBuiltin:
			next, cmd := m.handleCommand(msg.Action.Value)
			nm := next.(Model)
			nm.restoreComposer(composer)
			// Promote queued asks only when the command did not open another modal.
			if nm.modal == nil {
				if promote := nm.afterModalClosed(); promote != nil {
					cmd = tea.Batch(cmd, promote)
				}
			}
			return nm.paletteResultFocus(priorNotice, priorNoticeErr, cmd)
		case paletteActionAgent:
			ops, name := m.ops, msg.Action.Value
			return m, func() tea.Msg {
				ops <- protocol.SelectAgent{Name: name}
				return nil
			}
		case paletteActionSkill:
			m.resetHistoryBrowsing()
			text := "/" + msg.Action.Value + " " + m.composer.Value()
			m.setComposerValueAt(text, len([]rune(text)))
			m.recomputeCompletion()
			m.reflow()
			return m, m.setPaneFocus(focusLeft)
		case paletteActionKeybinds:
			m.clearNotice()
			m.modal = newKeybindEditor(m.keyMap, m.keyOverrides, m.services.Settings)
			m.reflow()
			return m, nil
		case paletteActionKeybindEditor:
			m.clearNotice()
			m.modal = newKeybindEditor(m.keyMap, m.keyOverrides, m.services.Settings)
			m.reflow()
			return m, nil
		}
		return m, nil

	case leaderExpiredMsg:
		if msg.gen == m.leaderGen {
			m.clearLeader()
		}
		return m, nil

	case childTranscriptRefreshMsg:
		if msg.gen != m.viewGen || msg.id != m.viewingID {
			return m, nil
		}
		return m, m.refreshViewingTranscript()

	case tea.PasteMsg:
		// Bracketed paste: images → chip; large multi-line text → chip.
		m.handleComposerPaste(msg.Content)
		m.recomputeCompletion()
		m.reflow()
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKeyMsg(msg)

	case tea.MouseMsg:
		// Wheel scrolls the transcript; left-click opens refs/links or expands tools.
		return m.handleMouse(msg)

	case openURIMsg:
		if msg.err != nil {
			m.setNotice("open link: "+msg.err.Error(), true)
			m.reflow()
		}
		return m, nil

	case filesOpenMsg:
		return m.openFilesExplorerPath(msg.path)

	case agentsOpenMsg:
		if id := strings.TrimSpace(msg.sessionID); id != "" {
			m.vizFocusID = id
		}
		cmd := m.handleAgentsOpen(msg)
		m.reflow()
		m.refreshViewport()
		return m, tea.Batch(cmd, m.broadcastAgentsState(), m.broadcastVisualizerState())

	case agentsHighlightMsg:
		m.vizFocusID = strings.TrimSpace(msg.sessionID)
		return m, m.broadcastVisualizerState()

	case agentsSpawnMsg:
		cmd := m.spawnRoot()
		m.reflow()
		return m, cmd

	case agentsInterruptMsg:
		cmd := m.interruptRoot(msg.sessionID)
		return m, cmd

	case agentsHideMsg:
		cmd := m.handleAgentsHide(msg.sessionID)
		m.reflow()
		return m, cmd

	case agentsRenameMsg:
		cmd := m.openRenameModal(msg.sessionID)
		return m, cmd

	case sessionRenamedMsg:
		cmd := m.applySessionRename(msg.id, msg.title)
		m.reflow()
		return m, cmd

	case rebindAppliedMsg:
		if msg.Chords == nil {
			delete(m.keyOverrides, msg.ID)
		} else {
			if m.keyOverrides == nil {
				m.keyOverrides = make(map[string][]string)
			}
			m.keyOverrides[msg.ID] = msg.Chords
		}
		m.keyMap = buildKeyMap(m.keyOverrides, m.splitOrientation)
		m.reflow()
		if ed, ok := m.modal.(*keybindEditor); ok {
			ed.effective = m.keyMap
		}
		return m, nil

	case keybindsSavedMsg:
		if msg.err != nil {
			m.setNotice("save keybinds: "+msg.err.Error(), true)
		} else {
			m.setNotice("keybinds saved to ~/.strike/keybinds.jsonc", false)
			if ed, ok := m.modal.(*keybindEditor); ok {
				if ed.closeAfterSave {
					m.modal = nil
					promote := m.afterModalClosed()
					m.refreshAwaitingPermission()
					m.reflow()
					return m, promote
				}
				ed.saveComplete()
			}
		}
		m.reflow()
		return m, nil
	}

	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	m.reflow()
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}
