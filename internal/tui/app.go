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
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	// FirstRun is true when the host detected a fresh strike home (no global
	// config and no real provider credentials). The TUI shows onboarding.
	FirstRun bool
	// VimMode selects pane/overlay/takeover for /vim. Empty defaults to pane.
	VimMode VimMode
	// PermissionAutoApproveSeconds arms permission-modal auto-allow once after
	// N seconds. Zero disables (default). Clamped by the host before wiring.
	PermissionAutoApproveSeconds int
	// PermissionAutoApproveExclude lists permission names that never auto-allow.
	PermissionAutoApproveExclude []string
	// NotifyMode selects desktop notifications: on, off, or unfocused-only
	// (default). Wired from config.notify.
	NotifyMode NotifyMode
	// Replay is a prior session event log for --continue / --session. Seeded
	// via cellsFromEvents + silent selection/child state — never fed through
	// applyEvent (avoids stuck turns, zombie permission modals, orphan children).
	Replay []protocol.Event
	// Keybinds are config overrides (binding id → key sequences). Applied on
	// top of defaultKeyMap at startup; /keys and footer hints show the result.
	Keybinds map[string][]string
}

// firstRunSetupMsg opens the provider picker once on a fresh install.
type firstRunSetupMsg struct{}

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
	// selectedFileRef is set when the user click-selects a path:line citation
	// (-1 = none). Empty-composer enter opens it when no tool expand applies.
	selectedFileRef int
	// cellClip stages one-shot OSC52 for y-to-copy (pointer so value-receiver
	// View can clear it). Never nil after New.
	cellClip *cellClipboard
	// copyFlashGen invalidates in-flight clearCellCopiedFlashMsg timers.
	copyFlashGen int
	modal        modal

	viewport viewport.Model
	composer textarea.Model
	// pendingPastes holds full text for collapsed large-paste chips in the
	// composer. Expanded on send; pruned when the chip leaves the value.
	pendingPastes []pasteChip
	// pendingImages holds image attachments for [image N] chips in the
	// composer. Sent as multimodal UserInput; pruned when the chip leaves.
	pendingImages []imageChip
	completion    *completionState
	keyMap        keyMap
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

	// firstRun drives the empty-transcript onboarding card and auto provider modal.
	firstRun, firstRunModalOpened bool
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
	// appearance is session-local auto|dark|light (lipgloss adaptive bg).
	appearance appearanceMode
	// children tracks active/recent subagent sessions for the activity pane.
	// Lifecycle never appends transcript cells.
	children []childActivity

	// roots holds frozen UI state for concurrent parent sessions (multi-root).
	// The active root's fields live on Model; others sit here until activated.
	roots map[string]*rootPane

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
}

// childActivity is one foreground subagent row in the activity/agents panes.
type childActivity struct {
	sessionID string
	parentID  string // spawning session; empty means direct root child
	agent     string
	prompt    string
	status    string // running | completed | failed | canceled
	startedAt time.Time
	endedAt   time.Time
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
		ops:             ops,
		events:          events,
		services:        services,
		agents:          services.Agents,
		skills:          services.Skills,
		commands:        commandCatalog(services.Skills),
		th:              th,
		themeID:         themeID,
		toolByID:        map[string]*toolCell{},
		selectedCell:    -1,
		selectedFileRef: -1,
		cellClip:        &cellClipboard{},
		composer:        ta,
		keyMap:          defaultKeyMap(),
		windows:         newWindowRegistry(),
		spin:            sp,
		historyPos:      -1,
		focused:         true,
		notifyMode:      NotifyUnfocusedOnly,
		appearance:      appearanceAuto,
		autonomy:        protocol.AutonomySupervised,
	}
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
		if option.VimMode != "" {
			m.vimMode = option.VimMode
		}
		if option.NotifyMode != "" {
			m.notifyMode = option.NotifyMode
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
	if m.notifyMode == "" {
		m.notifyMode = NotifyUnfocusedOnly
	}
	if services.History != nil {
		m.entries = services.History.Entries()
	}
	m.windows = configureFilesWindow(m.windows, m.workDir, m.services.Files)
	m.windows = configureMemoryWindow(m.windows, m.services.Memory)
	m.windows = configureIssuesWindow(m.windows, m.services.Issues)
	if len(replay) > 0 {
		seedFromReplay(&m, replay)
	}
	return m
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		textarea.Blink,
		m.listen(),
		m.spin.Tick,
		m.windows.init(),
		tea.SetWindowTitle(windowTitle(m)),
		// Kitty/Ghostty keep separate keyboard stacks per screen; re-enable
		// after WithAltScreen so shift+enter CSI is actually delivered (#187).
		enableEnhancedKeysCmd(),
	}
	if m.firstRun {
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

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(0, msg.Width), max(0, msg.Height)
		firstReady := !m.ready
		if !m.ready {
			m.viewport = viewport.New(max(1, m.width), 0)
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

	case sessionResumeMsg:
		id := strings.TrimSpace(msg.id)
		if id == "" || id == m.sessionID {
			return m, nil
		}
		// Prefer in-process multi-root open when the host supports it.
		if m.services.Roots != nil {
			cmd := m.openRootInProcess(id)
			m.modal = nil
			m.reflow()
			return m, cmd
		}
		if m.turnRunning {
			m.setNotice("wait for the current turn to finish before switching sessions", true)
			return m, nil
		}
		m.pendingResume = id
		m.modal = nil
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
		return m, tea.Batch(m.listen(), cmd)

	case permissionCountdownMsg:
		pm, ok := m.modal.(*permissionModal)
		if !ok {
			return m, nil
		}
		var cmd tea.Cmd
		m.modal, cmd = pm.onCountdown(msg)
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
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

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
		} else {
			m.modal = nil
		}
		return m, nil
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
		default:
			m.setNotice("logged out of "+msg.provider, false)
		}
		m.reflow()
		return m, nil

	case modelsLoadedMsg:
		if mm, ok := m.modal.(*modelModal); ok && mm.provider == msg.provider {
			mm.loading = false
			if msg.err != nil {
				mm.loadErr = msg.err.Error()
			} else {
				mm.all = msg.models
			}
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

	case historyAddedMsg:
		if msg.err != nil {
			m.setNotice("saving prompt history failed: "+msg.err.Error(), true)
		} else if m.services.History != nil {
			m.entries = m.services.History.Entries()
		}
		return m, nil

	case editorFinishedMsg:
		return m.applyEditorFinished(msg)

	case composerEditorFinishedMsg:
		return m.applyComposerEditorFinished(msg)

	case terminalOutputMsg:
		return m.applyTerminalOutput()

	case terminalExitMsg:
		return m.applyTerminalExit(msg)

	case firstRunSetupMsg:
		if m.firstRun && !m.firstRunModalOpened && m.modal == nil && len(m.cells) == 0 {
			m.firstRunModalOpened = true
			m.modal = newProviderModal(m.services, m.providerName, m.ops, m.th)
			m.reflow()
		}
		return m, nil

	case initResultMsg:
		return m.applyInitResult(msg)

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
		return m, nil

	case tea.BlurMsg:
		m.focused = false
		m.focusKnown = true
		return m, nil

	case filesRefreshMsg:
		m.windows = refreshFilesWindows(m.windows)
		return m, filesRefreshCmd()

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
		switch msg.Action.Kind {
		case paletteActionBuiltin:
			next, cmd := m.handleCommand(msg.Action.Value)
			return next.(Model).paletteResultFocus(priorNotice, priorNoticeErr, cmd)
		case paletteActionAgent:
			m.resetComposer()
			ops, name := m.ops, msg.Action.Value
			return m, func() tea.Msg {
				ops <- protocol.SelectAgent{Name: name}
				return nil
			}
		case paletteActionSkill:
			m.resetHistoryBrowsing()
			text := "/" + msg.Action.Value + " "
			m.setComposerValueAt(text, len([]rune(text)))
			m.recomputeCompletion()
			m.reflow()
			return m, m.setPaneFocus(focusLeft)
		case paletteActionKeybinds:
			m.modal = newKeysModal(m.keyMap)
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

	case tea.KeyMsg:
		if key.Matches(msg, m.keyMap.Quit) {
			return m, tea.Quit
		}
		if m.modal != nil {
			var cmd tea.Cmd
			m.modal, cmd = m.modal.update(msg)
			m.reflow()
			return m, cmd
		}
		// Leader chords before other routing so ctrl+x down is not eaten.
		if m.leaderArmed {
			if handled, cmd := m.handleLeaderKey(msg); handled {
				m.reflow()
				return m, cmd
			}
		}
		if key.Matches(msg, m.keyMap.Leader) {
			m.completion = nil
			return m, m.armLeader()
		}
		if handled, cmd := m.handleSessionNavKeys(msg); handled {
			m.reflow()
			return m, cmd
		}
		if m.focus == focusLeft && m.completion != nil {
			switch {
			case key.Matches(msg, m.keyMap.CompletionDismiss):
				m.completion = nil
				m.reflow()
				return m, nil
			case key.Matches(msg, m.keyMap.CompletionAccept):
				m.applyCompletion()
				return m, nil
			case key.Matches(msg, m.keyMap.CompletionPrev):
				m.completion.move(-1)
				m.reflow()
				return m, nil
			case key.Matches(msg, m.keyMap.CompletionNext):
				m.completion.move(1)
				m.reflow()
				return m, nil
			case key.Matches(msg, m.keyMap.Newline):
				m.composer.InsertString("\n")
				m.recomputeCompletion()
				m.reflow()
				return m, nil
			}
		}
		// Composer readline before nav chords so ctrl+k kills in the input
		// instead of cycling windows / focusing the right pane.
		if m.focus == focusLeft {
			if next, cmd, ok := m.applyComposerReadline(msg); ok {
				return next, cmd
			}
			// Composer newline before focus/cycle: bare LF (KeyCtrlJ) is how
			// many terminals deliver shift+enter. It must insert "\n", never
			// cycle windows or steal focus (#187). ctrl+j still cycles when
			// the right pane is focused (bindings checked below).
			if key.Matches(msg, m.keyMap.Newline) {
				m.resetHistoryBrowsing()
				m.composer.InsertString("\n")
				m.recomputeCompletion()
				m.reflow()
				return m, nil
			}
		}
		if key.Matches(msg, m.keyMap.FocusLeft) {
			m.completion = nil
			cmd := m.focusPane(focusLeft)
			m.reflow()
			return m, cmd
		}
		if key.Matches(msg, m.keyMap.FocusRight) {
			m.completion = nil
			cmd := m.focusPane(focusRight)
			m.reflow()
			return m, cmd
		}
		if key.Matches(msg, m.keyMap.CycleWindowNext) {
			m.completion = nil
			m.windows = m.windows.cycleBy(1)
			m.windows = refreshProjectDataWindows(m.windows)
			m.reflow()
			return m, nil
		}
		if key.Matches(msg, m.keyMap.CycleWindowPrev) {
			m.completion = nil
			m.windows = m.windows.cycleBy(-1)
			m.windows = refreshProjectDataWindows(m.windows)
			m.reflow()
			return m, nil
		}
		if key.Matches(msg, m.keyMap.Palette) {
			m.completion = nil
			m.modal = newPaletteModal(m.commands, m.agents, m.currentPaletteAvailability())
			m.reflow()
			return m, nil
		}
		if key.Matches(msg, m.keyMap.KeyHelp) {
			m.completion = nil
			m.modal = newKeysModal(m.keyMap)
			m.reflow()
			return m, nil
		}
		if key.Matches(msg, m.keyMap.ToggleOrientation) {
			m.completion = nil
			m.toggleOrientation()
			return m, nil
		}
		// Transcript scroll/jump always target the chat viewport, never the
		// right-pane terminal — handle before focusRight window routing.
		if key.Matches(msg, m.keyMap.ScrollUp) {
			m.viewport.HalfViewUp()
			return m, nil
		}
		if key.Matches(msg, m.keyMap.ScrollDown) {
			m.viewport.HalfViewDown()
			return m, nil
		}
		if key.Matches(msg, m.keyMap.JumpBottom) {
			m.viewport.GotoBottom()
			return m, nil
		}
		if key.Matches(msg, m.keyMap.Interrupt) {
			if m.turnRunning {
				ops := m.ops
				return m, func() tea.Msg {
					ops <- protocol.Interrupt{}
					return nil
				}
			}
			// Idle: esc clears a leftover input queue (rare once auto-drain runs).
			if m.clearInputQueue() {
				m.reflow()
				return m, nil
			}
		}
		if m.focus == focusRight {
			var cmd tea.Cmd
			m.windows, cmd = m.windows.update(msg)
			return m, cmd
		}
		if m.handleHistoryKey(msg) {
			return m, nil
		}
		if handled, cmd := m.handleToolCellKeys(msg); handled {
			m.reflow()
			m.refreshViewport()
			return m, cmd
		}
		// Empty composer + queued prompts: backspace pops last item for edit.
		if m.focus == focusLeft && m.composer.Value() == "" && len(m.inputQueue) > 0 {
			if msg.Type == tea.KeyBackspace || msg.Type == tea.KeyCtrlH {
				if m.popInputQueueToComposer() {
					return m, nil
				}
			}
		}
		// Bracketed paste: images → chip; large multi-line text → chip.
		if msg.Paste {
			m.handleComposerPaste(string(msg.Runes))
			m.recomputeCompletion()
			m.reflow()
			return m, nil
		}
		switch {
		case key.Matches(msg, m.keyMap.Newline):
			// Left-focus only (right pane returned above). Distinct from Send
			// (enter) and from scroll chords (pgup/ctrl+up).
			m.resetHistoryBrowsing()
			m.composer.InsertString("\n")
			m.recomputeCompletion()
			m.reflow()
			return m, nil
		case key.Matches(msg, m.keyMap.ExternalEditor):
			return m.openComposerExternalEditor()
		case key.Matches(msg, m.keyMap.Send):
			// Expand paste chips before send so the model sees full content.
			text := strings.TrimSpace(m.composerTextExpanded())
			images := pendingImageAttachments(m.pendingImages)
			if text == "" && len(images) == 0 {
				// Empty enter is tool expand / open-at-line (handleToolCellKeys).
				return m, nil
			}
			if text != "" && strings.HasPrefix(text, "/") && len(images) == 0 {
				return m.handleCommand(text)
			}
			if m.providerName == "" {
				m.setNeedsModelNotice("No model selected — use /provider <anthropic|openai|xai|echo> [model]", true)
				return m, nil // keep the typed prompt in the composer
			}
			if len(images) > 0 {
				if ok, known := m.modelSupportsImages(); known && !ok {
					m.setNotice(imageUnsupportedMsg, true)
					return m, nil // keep text + chips
				}
			}
			// @file mentions: history/display keep tokens; model text gets contents.
			modelText, notices := expandFileMentions(text, m.services.Files)
			display := displayPromptWithImages(text, m.pendingImages)
			next, cmd := m.submit(protocol.UserInput{Text: modelText, Images: images}, display)
			if len(notices) > 0 {
				mm := next.(Model)
				mm.setNotice(strings.Join(notices, "; "), false)
				mm.reflow()
				return mm, cmd
			}
			return next, cmd
		case key.Matches(msg, m.keyMap.SaveDefaults):
			// Persist the current provider/model/agent as global defaults.
			if m.providerName == "" {
				m.setNeedsModelNotice("nothing to save — select a provider first", true)
				return m, nil
			}
			return m, m.saveDefaultsCmd(m.providerName, m.modelName, m.agentName, string(m.effort), dotJoin(m.th, m.providerName+"/"+m.modelName, m.agentName))
		case key.Matches(msg, m.keyMap.Agent):
			// Tab cycles agents (opencode-style build/plan switching).
			if len(m.agents) > 1 && !m.turnRunning {
				next := m.agents[0]
				for i, name := range m.agents {
					if name == m.agentName {
						next = m.agents[(i+1)%len(m.agents)]
						break
					}
				}
				ops := m.ops
				return m, func() tea.Msg {
					ops <- protocol.SelectAgent{Name: next}
					return nil
				}
			}
			return m, nil
		}
		return m.updateComposer(msg)

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
		cmd := m.handleAgentsOpen(msg)
		m.reflow()
		m.refreshViewport()
		return m, tea.Batch(cmd, m.broadcastAgentsState())

	case agentsSpawnMsg:
		cmd := m.spawnRoot()
		m.reflow()
		return m, cmd

	case agentsInterruptMsg:
		cmd := m.interruptRoot(msg.sessionID)
		return m, cmd
	}

	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	m.reflow()
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m Model) updateComposer(msg tea.Msg) (tea.Model, tea.Cmd) {
	before := m.composer.Value()
	var cmd tea.Cmd
	m.composer, cmd = m.composer.Update(msg)
	if cleaned := stripComposerOSCLeak(m.composer.Value()); cleaned != m.composer.Value() {
		m.composer.SetValue(cleaned)
	}
	if m.historyPos >= 0 && m.composer.Value() != before {
		m.resetHistoryBrowsing()
	}
	if m.composer.Value() != before {
		m.pendingPastes = prunePendingPastes(m.composer.Value(), m.pendingPastes)
		m.pendingImages = prunePendingImages(m.composer.Value(), m.pendingImages)
	}
	m.recomputeCompletion()
	m.reflow()
	return m, cmd
}

// applyComposerReadline handles focusLeft readline chords so they are not
// stolen by window-cycle / focus bindings (notably ctrl+k). Palette and other
// global chords are matched earlier and remain global.
//
// ctrl+k only claims the event when it deletes text; at EOL / empty composer it
// falls through so vertical FocusRight and horizontal CycleWindowPrev still work.
func (m Model) applyComposerReadline(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keyMap.Yank):
		if m.killBuf == "" {
			return m, nil, true
		}
		m.resetHistoryBrowsing()
		m.composer.InsertString(m.killBuf)
		m.recomputeCompletion()
		m.reflow()
		return m, nil, true
	case key.Matches(msg, m.keyMap.WordBackward), key.Matches(msg, m.keyMap.WordForward):
		next, cmd := m.updateComposer(msg)
		return next.(Model), cmd, true
	case key.Matches(msg, m.keyMap.KillWord), key.Matches(msg, m.keyMap.KillLineStart):
		before := m.composer.Value()
		next, cmd := m.updateComposer(msg)
		nm := next.(Model)
		if killed, ok := contiguousDeletion(before, nm.composer.Value()); ok {
			nm.killBuf = killed
		}
		return nm, cmd, true
	case key.Matches(msg, m.keyMap.KillLineEnd):
		before := m.composer.Value()
		next, cmd := m.updateComposer(msg)
		nm := next.(Model)
		killed, ok := contiguousDeletion(before, nm.composer.Value())
		if !ok {
			// No deletion — leave the key for nav (cycle prev / focus bottom).
			return m, nil, false
		}
		nm.killBuf = killed
		return nm, cmd, true
	default:
		return m, nil, false
	}
}

// contiguousDeletion returns the single deleted span when after is before with
// one contiguous rune range removed (kill-word / kill-line style edits).
func contiguousDeletion(before, after string) (string, bool) {
	br, ar := []rune(before), []rune(after)
	if len(ar) >= len(br) {
		return "", false
	}
	i := 0
	for i < len(ar) && br[i] == ar[i] {
		i++
	}
	deleted := len(br) - len(ar)
	if i+deleted > len(br) {
		return "", false
	}
	if string(br[i+deleted:]) != string(ar[i:]) {
		return "", false
	}
	killed := string(br[i : i+deleted])
	if killed == "" {
		return "", false
	}
	return killed, true
}

func (m *Model) recomputeCompletion() {
	if m.historyPos >= 0 {
		m.completion = nil
		return
	}
	line := m.composer.Line()
	info := m.composer.LineInfo()
	col := info.StartColumn + info.ColumnOffset
	value := m.composer.Value()
	m.completion = leadingSlashCompletion(value, line, col, m.commands)
	if m.completion != nil {
		return
	}
	m.completion = m.atFileCompletionAt(value, line, col)
}

// atFileCompletionAt runs @file fuzzy search when Files is available.
func (m *Model) atFileCompletionAt(value string, line, col int) *completionState {
	if m.services.Files == nil {
		return nil
	}
	query, ok := activeAtQueryParts(value, line, col)
	if !ok {
		return nil
	}
	paths, err := m.services.Files.SearchFiles(query, 30)
	if err != nil || len(paths) == 0 {
		return nil
	}
	return atFileCompletion(value, line, col, paths)
}

func activeAtQueryParts(value string, row, col int) (string, bool) {
	lines := strings.Split(value, "\n")
	if row < 0 || row >= len(lines) {
		return "", false
	}
	line := []rune(lines[row])
	if col < 0 || col > len(line) {
		return "", false
	}
	end := col
	start := end
	for start > 0 {
		r := line[start-1]
		if isFileMentionPathRune(r) {
			start--
			continue
		}
		if r == '@' {
			start--
			break
		}
		return "", false
	}
	if start >= end || start >= len(line) || line[start] != '@' {
		return "", false
	}
	if start > 0 && !unicode.IsSpace(line[start-1]) {
		return "", false
	}
	if col <= start {
		return "", false
	}
	return string(line[start+1 : end]), true
}

func (m *Model) applyCompletion() {
	if m.completion == nil || m.completion.Selected >= len(m.completion.Candidates) {
		return
	}
	candidate := m.completion.Candidates[m.completion.Selected]
	replacement := m.completion.Replace
	value := []rune(m.composer.Value())
	if replacement.Start < 0 || replacement.End < replacement.Start || replacement.End > len(value) {
		m.completion = nil
		m.reflow()
		return
	}
	var name []rune
	delimiter := []rune(nil)
	if candidate.Path != "" {
		name = []rune("@" + candidate.Path)
		if replacement.End == len(value) || !unicode.IsSpace(value[replacement.End]) {
			delimiter = []rune(" ")
		}
	} else {
		name = []rune(candidate.Spec.Name)
		if candidate.Source == commandSourceSkill && (replacement.End == len(value) || !unicode.IsSpace(value[replacement.End])) {
			delimiter = []rune(" ")
		}
	}
	next := make([]rune, 0, len(value)-(replacement.End-replacement.Start)+len(name)+len(delimiter))
	next = append(next, value[:replacement.Start]...)
	next = append(next, name...)
	next = append(next, delimiter...)
	next = append(next, value[replacement.End:]...)
	m.setComposerValueAt(string(next), replacement.Start+len(name)+len(delimiter))
	m.completion = nil
	m.reflow()
}

func (m *Model) setComposerValueAt(value string, offset int) {
	runes := []rune(value)
	offset = max(0, min(offset, len(runes)))
	targetRow, targetCol := 0, 0
	for _, r := range runes[:offset] {
		if r == '\n' {
			targetRow++
			targetCol = 0
		} else {
			targetCol++
		}
	}
	m.composer.SetValue(value)
	// History/completion replacements are plain text; drop chips that no
	// longer appear (or clear all when the value is wholly replaced).
	m.pendingPastes = prunePendingPastes(value, m.pendingPastes)
	m.pendingImages = prunePendingImages(value, m.pendingImages)
	for steps := 0; m.composer.Line() > targetRow && steps <= len(runes)+1; steps++ {
		m.composer.CursorUp()
	}
	m.composer.SetCursor(targetCol)
}

func (m *Model) resetComposer() {
	m.composer.Reset()
	m.pendingPastes = nil
	m.pendingImages = nil
	m.completion = nil
	m.resetHistoryBrowsing()
	m.reflow()
}

func (m *Model) handleHistoryKey(msg tea.KeyMsg) bool {
	if m.historyPos >= 0 {
		switch {
		case key.Matches(msg, m.keyMap.HistoryPrev):
			if m.historyPos > 0 {
				m.historyPos--
			}
			m.recallHistory(m.entries[m.historyPos])
			return true
		case key.Matches(msg, m.keyMap.HistoryNext):
			if m.historyPos < len(m.entries)-1 {
				m.historyPos++
				m.recallHistory(m.entries[m.historyPos])
			} else {
				draft := m.historyDraft
				m.resetHistoryBrowsing()
				m.setComposerValueAt(draft, len([]rune(draft)))
				m.recomputeCompletion()
				m.reflow()
			}
			return true
		}
	}
	if !key.Matches(msg, m.keyMap.HistoryPrev) || m.composer.Value() != "" || len(m.entries) == 0 {
		return false
	}
	m.historyDraft = m.composer.Value()
	m.historyPos = len(m.entries) - 1
	m.recallHistory(m.entries[m.historyPos])
	return true
}

func (m *Model) recallHistory(prompt string) {
	m.setComposerValueAt(prompt, len([]rune(prompt)))
	m.recomputeCompletion()
	m.reflow()
}

func (m *Model) resetHistoryBrowsing() {
	m.historyPos = -1
	m.historyDraft = ""
}

func (m *Model) reflow() {
	gutter := m.th.Resolve().Spacing.XS
	leftWidth := m.width
	if m.splitOrientation != orientVertical {
		geometry := computePaneGeometry(m.width, gutter, m.focus)
		leftWidth = geometry.leftCandidateWidth(m.width)
	}
	compact := leftWidth < compactWidth || m.height < compactHeight
	composerWidth := leftWidth
	if !compact {
		composerWidth = ui.PanelInnerWidth(m.th, leftWidth)
	}
	m.composer.SetWidth(max(1, composerWidth))
	contentWidth := max(1, m.composer.Width())
	lineCounter := textarea.New()
	lineCounter.Prompt = ""
	lineCounter.ShowLineNumbers = false
	lineCounter.SetWidth(contentWidth)
	visualLines := 0
	for _, line := range strings.Split(m.composer.Value(), "\n") {
		lineCounter.SetValue(line)
		visualLines += max(1, lineCounter.LineInfo().Height)
		if visualLines >= composerMaxHeight {
			break
		}
	}
	composerRows := min(composerMaxHeight, max(composerMinHeight, visualLines))
	m.composer.SetHeight(composerRows)

	popupHeight := 0
	if m.completion != nil && m.modal == nil {
		m.completion.rows = 0
		if leftWidth > 0 {
			borderRows := 0
			if leftWidth >= 4 {
				borderRows = 2
			}
			available := max(0, m.height-2-composerRows-borderRows)
			m.completion.rows = min(completionMaxRows, min(len(m.completion.Candidates), available))
			if m.completion.rows > 0 {
				popupHeight = m.completion.rows + borderRows
			}
		}
	}

	if m.ready {
		l := computeLayout(leftWidth, m.height, composerRows, popupHeight, m.dangerouslySkipPermissions, m.noticeRowsFor(leftWidth))
		bodyHeight := l.transcript + l.notice + l.popup + l.composer
		rightWidth, rightHeight := m.width, bodyHeight
		rightCompact := m.width < compactWidth || m.height < compactHeight

		if m.splitOrientation == orientVertical {
			geo := computeVerticalPaneGeometry(m.width, bodyHeight, gutter, m.focus)
			if geo.mode == paneSplit {
				l = l.withBodyHeight(geo.leftHeight)
				rightWidth = geo.rightWidth
				rightHeight = geo.rightHeight
				rightCompact = false
			} else if m.focus == focusRight {
				rightWidth = geo.rightWidth
				rightHeight = geo.rightHeight
				if rightHeight == 0 {
					rightHeight = bodyHeight
				}
			} else {
				// Left-only single: keep full body on the left stack.
				rightWidth, rightHeight = 0, 0
			}
		} else {
			geometry := computePaneGeometry(m.width, gutter, m.focus)
			rightWidth = geometry.rightWidth
			if rightWidth == 0 {
				rightWidth = m.width
			}
			rightCompact = geometry.mode == paneSingle && (m.width < compactWidth || m.height < compactHeight)
			rightHeight = bodyHeight
		}

		m.viewport.Width = max(1, l.transcriptInnerWidthFor(m.th, leftWidth))
		m.viewport.Height = max(0, l.transcriptInnerHeight())
		if rightWidth > 0 && rightHeight > 0 {
			if rightCompact {
				m.windows = m.windows.resize(rightWidth, rightHeight)
			} else {
				m.windows = m.windows.resize(max(0, ui.PanelInnerWidth(m.th, rightWidth)), ui.PanelInnerHeight(rightWidth, rightHeight))
			}
		}
	}
}

// toggleOrientation flips horizontal/vertical body split and refreshes layout.
func (m *Model) toggleOrientation() {
	if m.splitOrientation == orientVertical {
		m.splitOrientation = orientHorizontal
	} else {
		m.splitOrientation = orientVertical
	}
	m.keyMap = buildKeyMap(m.keyOverrides, m.splitOrientation)
	m.reflow()
	m.refreshViewport()
}

func cloneKeybindMap(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for id, chords := range in {
		out[id] = append([]string(nil), chords...)
	}
	return out
}

// armPermissionAutoApprove starts the modal countdown when mode is armed and
// the permission name is not excluded.
func (m *Model) armPermissionAutoApprove(pm *permissionModal, permission string) tea.Cmd {
	if pm == nil || m.permissionAutoApproveSeconds <= 0 {
		return nil
	}
	if permissionAutoApproveExcluded(permission, m.permissionAutoApproveExclude) {
		return nil
	}
	return pm.armAutoApprove(m.permissionAutoApproveSeconds)
}

func permissionAutoApproveExcluded(permission string, exclude []string) bool {
	if len(exclude) == 0 {
		return false
	}
	want := strings.ToLower(strings.TrimSpace(permission))
	for _, name := range exclude {
		if strings.EqualFold(strings.TrimSpace(name), want) {
			return true
		}
	}
	return false
}

func (m *Model) applyEvent(ev protocol.Event) tea.Cmd {
	// Defense-in-depth: child-session events should only surface permissions,
	// questions, and child lifecycle (activity pane). Primary filtering is in
	// the engine; ChildStarted/Completed must reach the TUI for subagent UI.
	if corr, ok := eventCorrelation(ev); ok && (corr.ParentSessionID != "" || corr.Depth > 0) {
		switch ev.(type) {
		case protocol.PermissionAsked, protocol.PermissionResolved,
			protocol.QuestionAsked, protocol.QuestionResolved,
			protocol.ChildStarted, protocol.ChildCompleted:
		default:
			return nil
		}
	}
	// Status coloring tracks protocol facts before view-side side effects so
	// agentState never depends on modal type checks.
	m.applyAgentStateEvent(ev)
	var cmd tea.Cmd
	switch ev := ev.(type) {
	case protocol.UserMessage:
		m.completeAssistantCells()
		m.cells = append(m.cells, &userCell{text: userMessageDisplayText(ev.Text, ev.Images)})
		// Fallback for logs without session.titled (pre-auto-title sessions).
		if m.titleTopic == "" {
			if topic := sanitizeTitleTopic(ev.Text); topic != "" {
				m.titleTopic = topic
				cmd = tea.Batch(tea.SetWindowTitle(windowTitle(*m)), m.broadcastContextState())
			}
		}
	case protocol.SessionTitled:
		if topic := sanitizeTitleTopic(ev.Title); topic != "" {
			m.titleTopic = topic
			cmd = tea.Batch(tea.SetWindowTitle(windowTitle(*m)), m.broadcastContextState())
		}
	case protocol.TurnStarted:
		m.turnStartedAt = time.Now()
		m.toolCallsThisTurn = 0
		m.refreshOpenPalette()
		cmd = m.broadcastContextState()
	case protocol.TextDelta:
		if last, ok := lastCell[*assistantCell](m.cells); ok {
			last.text += ev.Text
		} else {
			m.cells = append(m.cells, &assistantCell{text: ev.Text})
		}
	case protocol.ReasoningDelta:
		if ev.Text == "" {
			break
		}
		if last, ok := lastCell[*reasoningCell](m.cells); ok {
			last.text += ev.Text
		} else {
			m.cells = append(m.cells, &reasoningCell{text: ev.Text})
		}
	case protocol.ToolCallBegin:
		if last, ok := lastCell[*assistantCell](m.cells); ok {
			last.complete = true
			last.mdCacheOK = false
		}
		m.toolCallsThisTurn++
		tc := &toolCell{callID: ev.CallID, name: ev.Name, args: ev.Args}
		m.toolByID[ev.CallID] = tc
		if isExploreTool(ev.Name) {
			if exp, ok := lastCell[*exploreCell](m.cells); ok && exp.accepting {
				exp.calls = append(exp.calls, tc)
				break
			}
			// First explore tool stays a normal cell; a second consecutive one
			// promotes the pair into an exploring group.
			if prev, ok := lastCell[*toolCell](m.cells); ok && isExploreTool(prev.name) {
				m.cells[len(m.cells)-1] = &exploreCell{
					calls:     []*toolCell{prev, tc},
					accepting: true,
				}
				break
			}
			m.cells = append(m.cells, tc)
			break
		}
		if exp, ok := lastCell[*exploreCell](m.cells); ok {
			exp.accepting = false
		}
		m.cells = append(m.cells, tc)
	case protocol.ToolCallOutput:
		if tc, ok := m.toolByID[ev.CallID]; ok && !tc.done {
			tc.output += ev.Data
		}
	case protocol.ToolCallEnd:
		if tc, ok := m.toolByID[ev.CallID]; ok {
			applyToolCallEnd(tc, ev.Title, ev.Output, ev.Metadata, ev.IsError)
			if isProjectDataTool(tc.name) {
				m.windows = refreshProjectDataWindows(m.windows)
			}
			if isWorkspaceFSTool(tc.name) {
				m.windows = refreshFilesWindows(m.windows)
			}
		}
	case protocol.PermissionAsked:
		pm := newPermissionModal(ev, m.ops, m.th)
		m.modal = pm
		cmd = m.broadcastContextState()
		if auto := m.armPermissionAutoApprove(pm, ev.Permission); auto != nil {
			cmd = tea.Batch(cmd, auto)
		}
		// Static message only — never include paths, args, or secrets.
		cmd = tea.Batch(cmd, m.desktopNotifyCmd("strike: permission required", true))
	case protocol.PermissionResolved:
		if modal, ok := m.modal.(*permissionModal); ok && modal.req.RequestID == ev.RequestID {
			m.modal = nil
		}
		cmd = m.broadcastContextState()
	case protocol.QuestionAsked:
		m.modal = newQuestionModal(ev, m.ops, m.th)
		cmd = m.broadcastContextState()
		cmd = tea.Batch(cmd, m.desktopNotifyCmd("strike: question required", true))
	case protocol.QuestionResolved:
		if modal, ok := m.modal.(*questionModal); ok && modal.req.RequestID == ev.RequestID {
			m.modal = nil
		}
		cmd = m.broadcastContextState()
	case protocol.TurnCompleted:
		m.completeAssistantCells()
		if exp, ok := lastCell[*exploreCell](m.cells); ok {
			exp.accepting = false
		}
		notify := m.desktopNotifyCmd("strike: turn complete", false)
		m.turnStartedAt = time.Time{}
		m.toolCallsThisTurn = 0
		m.refreshOpenPalette()
		// turnRunning is already false via applyAgentStateEvent; drain next prompt.
		cmd = tea.Batch(m.broadcastContextState(), notify, m.tryDrainInputQueue())
	case protocol.ModelSelected:
		if m.noticeCause == noticeNeedsModel {
			m.clearNotice()
		}
		m.providerName, m.modelName = ev.Provider, ev.Model
		m.clearUsage()
		m.refreshOpenPalette()
		cmd = tea.Batch(m.fetchContextLimitsCmd(), m.broadcastContextState(), m.authExpiryNoticeCmd())
	case protocol.AgentSelected:
		m.agentName = ev.Name
		cmd = m.broadcastContextState()
	case protocol.PhaseChanged:
		m.phaseName = ev.Phase
		m.phaseWorkflow = ev.Workflow
		cmd = m.broadcastContextState()
	case protocol.EffortSelected:
		m.effort = ev.Level
		m.setNotice("effort: "+detailJoin(m.th, string(ev.Level), ev.Level.Describe()), false)
	case protocol.AutonomySelected:
		m.autonomy = ev.Mode.Normalize()
		m.setNotice("autonomy: "+detailJoin(m.th, string(m.autonomy), m.autonomy.Describe()), false)
	case protocol.FastSelected:
		m.fastEnabled = ev.Enabled
		m.setNotice(m.fastNotice(ev.Enabled), false)
	case protocol.FilesInvalidated:
		m.windows = refreshFilesWindows(m.windows)
		if len(ev.Paths) == 0 {
			break
		}
		label := strings.Join(ev.Paths, ", ")
		m.setNotice("files changed — agent will re-read: "+label, false)
	case protocol.UsageReported:
		m.recordUsage(ev)
		cmd = m.broadcastContextState()
	case protocol.EffectivePrompt:
		if m.pendingContextDoctor {
			m.pendingContextDoctor = false
			m.modal = newDoctorModal(ev, m.contextLimit, m.contextLimitKnown)
			m.reflow()
		} else {
			m.cells = append(m.cells, &infoCell{text: formatEffectivePrompt(ev)})
		}
	case protocol.CompactionCompleted:
		strategy := ev.Strategy
		if strategy == "" {
			strategy = protocol.CompactionStrategyTrim
		}
		msg := fmt.Sprintf("history compacted (%s/%s): removed %d, kept %d", ev.Reason, strategy, ev.Removed, ev.Kept)
		if m.turnRunning {
			m.cells = append(m.cells, &errorCell{text: msg})
		} else {
			m.setNotice(msg, false)
		}
		cmd = m.broadcastContextState()
	case protocol.SessionRewound:
		m.cells, m.toolByID = dropLastUserTurnCells(m.cells, m.toolByID)
		m.selectedCell = -1
		m.selectedFileRef = -1
		m.setNotice(formatSessionRewound(ev), false)
		cmd = m.broadcastContextState()
	case protocol.EngineError:
		// Mid-turn failures belong in the transcript; idle-state errors
		// (no model selected, bad /provider, …) show in the notice line.
		// Do not mark assistants complete here: non-terminal errors (e.g.
		// "turn already running") must not freeze a live stream.
		if m.turnRunning {
			m.cells = append(m.cells, &errorCell{text: ev.Message})
		} else {
			if ev.Message == "no model selected — use /provider <anthropic|openai|xai|echo> [model]" {
				m.setNeedsModelNotice(ev.Message, true)
			} else {
				m.setNotice(ev.Message, true)
			}
		}
		cmd = m.broadcastContextState()
	case protocol.ChildStarted:
		m.onChildStarted(ev)
		cmd = m.broadcastAgentsState()
	case protocol.ChildCompleted:
		m.onChildCompleted(ev)
		cmd = m.broadcastAgentsState()
		if m.viewingChild() && (ev.SessionID == m.viewingID || ev.SessionID == "") {
			if refresh := m.refreshViewingTranscript(); refresh != nil {
				cmd = tea.Batch(cmd, refresh)
			}
		}
	}
	return cmd
}

const maxChildActivity = 12

func (m *Model) onChildStarted(ev protocol.ChildStarted) {
	id := ev.SessionID
	if id == "" {
		id = "child"
	}
	parentID := ev.ParentSessionID
	now := time.Now()
	for i := range m.children {
		if m.children[i].sessionID == id {
			m.children[i].agent = ev.Agent
			m.children[i].prompt = ev.Prompt
			m.children[i].status = "running"
			if parentID != "" {
				m.children[i].parentID = parentID
			}
			if m.children[i].startedAt.IsZero() {
				m.children[i].startedAt = now
			}
			m.children[i].endedAt = time.Time{}
			return
		}
	}
	m.children = append(m.children, childActivity{
		sessionID: id,
		parentID:  parentID,
		agent:     ev.Agent,
		prompt:    ev.Prompt,
		status:    "running",
		startedAt: now,
	})
	m.trimChildren()
}

func (m *Model) onChildCompleted(ev protocol.ChildCompleted) {
	id := ev.SessionID
	status := string(ev.Status)
	if status == "" {
		status = string(protocol.ChildStatusCompleted)
	}
	now := time.Now()
	applyChildCompletedToTaskCells(m.toolByID, ev)
	for i := range m.children {
		if m.children[i].sessionID == id || (id == "" && i == len(m.children)-1) {
			m.children[i].status = status
			if ev.ParentSessionID != "" && m.children[i].parentID == "" {
				m.children[i].parentID = ev.ParentSessionID
			}
			m.children[i].endedAt = now
			return
		}
	}
	// Completed without a matching start still surfaces briefly.
	if id == "" {
		id = "child"
	}
	m.children = append(m.children, childActivity{
		sessionID: id,
		parentID:  ev.ParentSessionID,
		status:    status,
		startedAt: now,
		endedAt:   now,
	})
	m.trimChildren()
}

func (m *Model) trimChildren() {
	if len(m.children) <= maxChildActivity {
		return
	}
	// Drop oldest non-running first; if still over, drop oldest overall.
	for len(m.children) > maxChildActivity {
		drop := -1
		for i, ch := range m.children {
			if ch.status != "running" {
				drop = i
				break
			}
		}
		if drop < 0 {
			drop = 0
		}
		m.children = append(m.children[:drop], m.children[drop+1:]...)
	}
}

// eventCorrelation extracts lineage fields when the event embeds Correlation.
func eventCorrelation(ev protocol.Event) (protocol.Correlation, bool) {
	switch e := ev.(type) {
	case protocol.UserMessage:
		return e.Correlation, true
	case protocol.SessionTitled:
		return e.Correlation, true
	case protocol.TurnStarted:
		return e.Correlation, true
	case protocol.TextDelta:
		return e.Correlation, true
	case protocol.ReasoningDelta:
		return e.Correlation, true
	case protocol.ToolCallBegin:
		return e.Correlation, true
	case protocol.ToolCallEnd:
		return e.Correlation, true
	case protocol.ToolCallOutput:
		return e.Correlation, true
	case protocol.ProcessStarted:
		return e.Correlation, true
	case protocol.ProcessOutput:
		return e.Correlation, true
	case protocol.ProcessExited:
		return e.Correlation, true
	case protocol.PermissionAsked:
		return e.Correlation, true
	case protocol.PermissionResolved:
		return e.Correlation, true
	case protocol.QuestionAsked:
		return e.Correlation, true
	case protocol.QuestionResolved:
		return e.Correlation, true
	case protocol.TurnCompleted:
		return e.Correlation, true
	case protocol.ModelSelected:
		return e.Correlation, true
	case protocol.AgentSelected:
		return e.Correlation, true
	case protocol.PhaseChanged:
		return e.Correlation, true
	case protocol.EffortSelected:
		return e.Correlation, true
	case protocol.AutonomySelected:
		return e.Correlation, true
	case protocol.FastSelected:
		return e.Correlation, true
	case protocol.UsageReported:
		return e.Correlation, true
	case protocol.CompactionStarted:
		return e.Correlation, true
	case protocol.CompactionCompleted:
		return e.Correlation, true
	case protocol.EffectivePrompt:
		return e.Correlation, true
	case protocol.EngineError:
		return e.Correlation, true
	case protocol.ChildStarted:
		return e.Correlation, true
	case protocol.ChildCompleted:
		return e.Correlation, true
	default:
		return protocol.Correlation{}, false
	}
}

// formatEffectivePrompt renders a compact layer map for the transcript.
func formatEffectivePrompt(ev protocol.EffectivePrompt) string {
	var b strings.Builder
	scope := "current composition"
	if ev.FromLastStream {
		scope = "last request"
	}
	fmt.Fprintf(&b, "effective prompt (%s) - system %d chars - history %d msgs",
		scope, ev.SystemChars, ev.MessageCount)
	if len(ev.Layers) == 0 {
		b.WriteString("\n  (no layers)")
		return b.String()
	}
	for i, layer := range ev.Layers {
		kind := sanitizeDisplayData(layer.Kind)
		source := sanitizeDisplayData(layer.Source)
		mode := sanitizeDisplayData(layer.Mode)
		fmt.Fprintf(&b, "\n  %d. %s [%s] %s - %d chars",
			i+1, kind, mode, source, layer.Chars)
	}
	return b.String()
}

// clearUsage drops last-request token figures and catalog limits so a model
// switch never shows stale occupancy against a new window size. Session totals
// for /cost are kept — they span the whole session log.
func (m *Model) clearUsage() {
	m.usageInput = protocol.TokenCount{}
	m.usageOutput = protocol.TokenCount{}
	m.usageCacheRead = protocol.TokenCount{}
	m.usageCacheCreation = protocol.TokenCount{}
	m.usageUsed = protocol.TokenCount{}
	m.usageSource = ""
	m.modelInputCost = 0
	m.modelOutputCost = 0
	m.modelHasCost = false
	m.contextLimit = 0
	m.contextLimitKnown = false
	m.outputLimit = 0
	m.outputLimitKnown = false
	m.modelAttachment = false
	m.modelAttachmentKnown = false
}

// recordUsage updates last-request figures and session totals.
func (m *Model) recordUsage(ev protocol.UsageReported) {
	m.usageInput = ev.Input
	m.usageOutput = ev.Output
	m.usageCacheRead = ev.CacheRead
	m.usageCacheCreation = ev.CacheCreation
	m.usageUsed = ev.Used
	m.usageSource = ev.Source
	m.usageSession.add(ev)
}

// fetchContextLimitsCmd looks up context window, output limit, and pricing for
// the current provider/model via the host catalog (may hit network/cache).
func (m Model) fetchContextLimitsCmd() tea.Cmd {
	catalog := m.services.Catalog
	provider, model := m.providerName, m.modelName
	if catalog == nil || provider == "" {
		return nil
	}
	return func() tea.Msg {
		ctx := context.Background()
		ct, cok, _ := catalog.ContextWindow(ctx, provider, model)
		ot, ook, _ := catalog.OutputLimit(ctx, provider, model)
		msg := contextLimitsMsg{
			provider:      provider,
			model:         model,
			contextTokens: ct,
			contextOK:     cok,
			outputTokens:  ot,
			outputOK:      ook,
		}
		if infos, err := catalog.Models(ctx, provider); err == nil {
			for _, info := range infos {
				if info.ID == model {
					msg.attachment = info.Attachment
					msg.attachmentOK = true
					msg.inputCost = info.InputCost
					msg.outputCost = info.OutputCost
					msg.hasCost = info.HasCost
					break
				}
			}
		}
		return msg
	}
}

// contextStateSnapshot copies model-owned context fields for right-pane windows.
func (m Model) contextStateSnapshot() contextStateMsg {
	return contextStateMsg{
		WorkDir:           m.workDir,
		SessionID:         m.sessionID,
		SessionTitle:      m.titleTopic,
		Provider:          m.providerName,
		Model:             m.modelName,
		Agent:             m.agentName,
		AgentState:        m.agentState().Label(),
		Input:             m.usageInput,
		Output:            m.usageOutput,
		Used:              m.usageUsed,
		Source:            m.usageSource,
		ContextLimit:      m.contextLimit,
		ContextLimitKnown: m.contextLimitKnown,
		OutputLimit:       m.outputLimit,
		OutputLimitKnown:  m.outputLimitKnown,
	}
}

// broadcastContextState pushes the current snapshot to every right-pane window.
func (m *Model) broadcastContextState() tea.Cmd {
	var cmd tea.Cmd
	m.windows, cmd = m.windows.broadcast(m.contextStateSnapshot())
	return cmd
}

// agentsStateSnapshot pushes multi-root tree data into the agents window.
func (m Model) agentsStateSnapshot() agentsStateMsg {
	// Ensure active root is visible to the tree builder.
	roots := m.liveRootIDs()
	type rootSnap struct {
		id       string
		title    string
		state    theme.AgentState
		children []childActivity
	}
	snaps := make([]agentsRootSnap, 0, len(roots))
	for _, id := range roots {
		var kids []childActivity
		if id == m.sessionID {
			kids = append([]childActivity(nil), m.children...)
		} else if m.roots != nil {
			if p, ok := m.roots[id]; ok && p != nil {
				kids = append([]childActivity(nil), p.children...)
			}
		}
		snaps = append(snaps, agentsRootSnap{
			ID:       id,
			Title:    m.rootTitleLabel(id),
			State:    m.rootAgentState(id),
			Children: kids,
		})
	}
	viewing := m.viewingID
	if viewing == "" {
		viewing = m.sessionID
	}
	return agentsStateMsg{
		activeID:  m.sessionID,
		viewingID: viewing,
		roots:     snaps,
	}
}

// handleAgentsOpen activates a root or opens a child transcript from the tree.
func (m *Model) handleAgentsOpen(msg agentsOpenMsg) tea.Cmd {
	id := strings.TrimSpace(msg.sessionID)
	if id == "" {
		return nil
	}
	// Root selection.
	for _, live := range m.liveRootIDs() {
		if live == id {
			if msg.interrupt {
				return m.interruptRoot(id)
			}
			cmd := m.activateRoot(id)
			// Close child view when focusing the root itself.
			if m.viewingChild() {
				_ = m.closeSessionView()
			}
			return cmd
		}
	}
	// Child (or nested) transcript.
	if msg.interrupt {
		return m.interruptRoot(id)
	}
	// Ensure parent root is active when opening a child of another root.
	if parent := m.parentOfChild(id); parent != "" && parent != m.sessionID {
		if root := m.findLiveRootAncestor(parent); root != "" && root != m.sessionID {
			if cmd := m.activateRoot(root); cmd != nil {
				return tea.Batch(cmd, m.openSessionView(id))
			}
		}
	}
	return m.openSessionView(id)
}

// broadcastAgentsState pushes current subagent rows to every right-pane window.
func (m *Model) broadcastAgentsState() tea.Cmd {
	var cmd tea.Cmd
	m.windows, cmd = m.windows.broadcast(m.agentsStateSnapshot())
	return cmd
}

// hasContextMeter reports whether the header should show a compact usage chip.
func (m Model) hasContextMeter() bool {
	return m.usageUsed.Known || m.usageInput.Known || m.usageOutput.Known || m.contextLimitKnown
}

func (m Model) currentPaletteAvailability() paletteAvailability {
	return paletteAvailability{
		HasProvider: m.providerName != "",
		TurnRunning: m.turnRunning,
	}
}

func (m *Model) refreshOpenPalette() {
	if palette, ok := m.modal.(*paletteModal); ok {
		palette.refresh(buildPaletteEntries(m.commands, m.agents, m.currentPaletteAvailability()))
	}
}

func (m Model) currentPaletteEntry(action paletteAction) (paletteEntry, bool) {
	for _, entry := range buildPaletteEntries(m.commands, m.agents, m.currentPaletteAvailability()) {
		if entry.Action == action {
			return entry, true
		}
	}
	return paletteEntry{}, false
}

// handleCommand processes slash commands locally; they never reach the model.
func (m Model) handleCommand(text string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(text)
	switch fields[0] {
	case "/provider":
		if len(fields) < 2 {
			// Bare /provider opens the centered picker with auth status.
			m.resetComposer()
			m.modal = newProviderModal(m.services, m.providerName, m.ops, m.th)
			return m, nil
		}
		op := protocol.SelectModel{Provider: fields[1]}
		if len(fields) > 2 {
			op.Model = fields[2]
		}
		return m.sendSelect(op)
	case "/model":
		if m.providerName == "" {
			m.setNeedsModelNotice("select a provider first: /provider <anthropic|openai|xai|echo>", true)
			return m, nil
		}
		if len(fields) < 2 {
			// Bare /model opens the centered picker (models.dev catalog).
			m.resetComposer()
			m.modal = newModelModal(m.providerName, m.modelName, m.ops, m.services.Settings)
			return m, loadModelsCmd(m.services.Catalog, m.providerName)
		}
		return m.sendSelect(protocol.SelectModel{Provider: m.providerName, Model: fields[1]})
	case "/effort":
		if len(fields) < 2 {
			// Bare /effort opens the centered picker.
			m.resetComposer()
			m.modal = newEffortModal(m.effort, m.ops, m.services.Settings)
			return m, nil
		}
		level, ok := protocol.ParseEffort(fields[1])
		if !ok || level == protocol.EffortDefault {
			m.setNotice("unknown effort "+fields[1]+" — want "+effortChoices(), true)
			return m, nil
		}
		m.resetComposer()
		m.clearNotice()
		ops := m.ops
		return m, func() tea.Msg {
			ops <- protocol.SetEffort{Level: level}
			return nil
		}
	case "/autonomy":
		if len(fields) < 2 {
			m.resetComposer()
			m.modal = newAutonomyModal(m.autonomy, m.ops)
			return m, nil
		}
		mode, ok := protocol.ParseAutonomy(fields[1])
		if !ok {
			m.setNotice("unknown autonomy "+fields[1]+" — want "+autonomyChoices(), true)
			return m, nil
		}
		m.resetComposer()
		m.clearNotice()
		ops := m.ops
		return m, func() tea.Msg {
			ops <- protocol.SetAutonomy{Mode: mode}
			return nil
		}
	case "/auth":
		m.resetComposer()
		return m.handleAuth(fields[1:])
	case "/settings":
		m.resetComposer()
		m.clearNotice()
		m.modal = newSettingsModal(m.services, m.ops, m.th)
		return m, nil
	case "/agent":
		if len(fields) < 2 {
			// Bare /agent opens the centered picker (tab still cycles).
			m.resetComposer()
			m.modal = newAgentModal(m.agentName, m.agents, m.ops, m.services.Settings)
			return m, nil
		}
		m.resetComposer()
		ops := m.ops
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), fields[0]))
		return m, func() tea.Msg {
			ops <- protocol.SelectAgent{Name: name}
			return nil
		}
	case "/fast":
		return m.handleFastCommand(fields[1:])
	case "/think":
		return m.handleThinkCommand(fields[1:])
	case "/vim":
		return m.handleVimCommand(fields[1:])
	case "/md-read":
		return m.handleMDRead(text, fields)
	case "/theme":
		return m.handleThemeCommand(fields[1:])
	case "/layout", "/split":
		m.resetComposer()
		m.clearNotice()
		m.toggleOrientation()
		label := "horizontal"
		if m.splitOrientation == orientVertical {
			label = "vertical"
		}
		m.setNotice("layout: "+label+" (ctrl+; or /layout toggles)", false)
		return m, nil
	case "/compact":
		m.resetComposer()
		m.clearNotice()
		ops := m.ops
		return m, func() tea.Msg {
			ops <- protocol.Compact{}
			return nil
		}
	case "/fork":
		return m.handleForkCommand()
	case "/undo", "/rewind":
		return m.handleUndoCommand(fields[1:])
	case "/session":
		return m.handleSessionCommand(fields[1:])
	case "/help":
		m.resetComposer()
		m.clearNotice()
		m.modal = newHelpModal(m.commands)
		m.reflow()
		return m, nil
	case "/keys":
		if len(fields) >= 2 && fields[1] == "reset" {
			m.resetComposer()
			m.clearNotice()
			m.keyOverrides = nil
			m.keyMap = buildKeyMap(nil, m.splitOrientation)
			m.setNotice("keybinds reset to defaults (session only; remove keybinds from config to persist)", false)
			return m, nil
		}
		m.resetComposer()
		m.clearNotice()
		m.modal = newKeysModal(m.keyMap)
		m.reflow()
		return m, nil
	case "/memory":
		return m.handleMemoryCommand(fields[1:])
	case "/issues":
		return m.handleIssuesCommand(fields[1:])
	case "/context", "/effective-prompt":
		m.resetComposer()
		m.clearNotice()
		m.pendingContextDoctor = true
		ops := m.ops
		return m, func() tea.Msg {
			ops <- protocol.InspectEffectivePrompt{}
			return nil
		}
	case "/cost":
		m.resetComposer()
		m.clearNotice()
		m.modal = newCostModal(
			m.usageSession,
			m.usageInput, m.usageOutput, m.usageCacheRead, m.usageCacheCreation, m.usageUsed,
			m.usageSource,
			m.providerName, m.modelName,
			m.modelInputCost, m.modelOutputCost, m.modelHasCost,
			m.contextLimit, m.contextLimitKnown,
		)
		m.reflow()
		return m, nil
	case "/upgrade":
		m.resetComposer()
		m.clearNotice()
		if m.turnRunning {
			m.setNotice("wait for the current turn to finish before upgrading", true)
			return m, nil
		}
		m.pendingUpgrade = true
		m.modal = nil
		return m, tea.Quit
	case "/init":
		return m.handleInitCommand()
	default:
		// Unknown commands fall through to skills: /name args renders the
		// skill template and submits it as the user message.
		name := strings.TrimPrefix(fields[0], "/")
		for _, skill := range m.skills {
			if skill.Name != name {
				continue
			}
			if m.providerName == "" {
				m.setNeedsModelNotice("No model selected — use /provider <anthropic|openai|xai|echo> [model]", true)
				return m, nil
			}
			args := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
			prompt := skill.Render(args)
			return m.submit(protocol.UserInput{Text: prompt}, text)
		}
		m.setNotice("unknown command "+fields[0]+" — try /help", true)
		return m, nil
	}
}

// PendingResume returns the session id selected via /session for durable
// resume. Empty when the user quit without switching.
func (m Model) PendingResume() string {
	return strings.TrimSpace(m.pendingResume)
}

// PendingUpgrade reports whether /upgrade requested a self-update after quit.
func (m Model) PendingUpgrade() bool {
	return m.pendingUpgrade
}

func (m Model) handleUndoCommand(args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	if m.turnRunning {
		m.setNotice("cannot rewind while a turn is running", true)
		return m, nil
	}
	mode := ""
	if len(args) > 0 {
		mode = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch mode {
	case "", "help", "?":
		if mode == "help" || mode == "?" {
			m.setNotice("usage: /undo [chat|files]", false)
			return m, nil
		}
		// Bare /undo opens the choice modal.
		m.modal = newUndoModal(m.ops)
		m.reflow()
		return m, nil
	case "chat", "history":
		ops := m.ops
		return m, func() tea.Msg {
			ops <- protocol.Rewind{RestoreFiles: false}
			return nil
		}
	case "files", "disk", "all":
		ops := m.ops
		return m, func() tea.Msg {
			ops <- protocol.Rewind{RestoreFiles: true}
			return nil
		}
	default:
		m.setNotice("usage: /undo [chat|files]", true)
		return m, nil
	}
}

func formatSessionRewound(ev protocol.SessionRewound) string {
	msg := "rewound last turn"
	if ev.Removed > 0 {
		msg = fmt.Sprintf("rewound last turn (%d messages)", ev.Removed)
	}
	if ev.RestoreFiles {
		switch {
		case ev.FilesRestored > 0 && ev.FilesSkipped > 0:
			msg = fmt.Sprintf("%s; restored %d file(s), skipped %d", msg, ev.FilesRestored, ev.FilesSkipped)
		case ev.FilesRestored > 0:
			msg = fmt.Sprintf("%s; restored %d file(s)", msg, ev.FilesRestored)
		case ev.FilesSkipped > 0:
			msg = fmt.Sprintf("%s; no files restored (%d skipped)", msg, ev.FilesSkipped)
		default:
			msg += "; no file changes to restore"
		}
	}
	return msg
}

func (m Model) handleInitCommand() (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	if m.services.Init == nil {
		m.setNotice("project init is unavailable", true)
		return m, nil
	}
	exists, path, err := m.services.Init.Exists()
	if err != nil {
		m.setNotice("init failed: "+err.Error(), true)
		return m, nil
	}
	if exists {
		m.modal = newInitConfirmModal(path, m.services.Init)
		m.reflow()
		return m, nil
	}
	init := m.services.Init
	return m, func() tea.Msg {
		path, created, err := init.Write(false)
		if err != nil {
			return initResultMsg{err: err.Error()}
		}
		return initResultMsg{path: path, created: created}
	}
}

func (m Model) applyInitResult(msg initResultMsg) (tea.Model, tea.Cmd) {
	m.modal = nil
	if msg.canceled {
		m.setNotice("init canceled", false)
		m.reflow()
		return m, nil
	}
	if msg.err != "" {
		m.setNotice("init failed: "+msg.err, true)
		m.reflow()
		return m, nil
	}
	display := msg.path
	if base := filepath.Base(display); base != "" && base != "." {
		display = base
	}
	switch {
	case msg.replaced:
		m.setNotice("updated "+display, false)
	case msg.created:
		m.setNotice("created "+display, false)
	default:
		m.setNotice("wrote "+display, false)
	}
	m.reflow()
	return m, nil
}

func (m Model) handleForkCommand() (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	if m.turnRunning {
		m.setNotice("wait for the current turn to finish before forking", true)
		return m, nil
	}
	if m.sessionID == "" {
		m.setNotice("no session to fork", true)
		return m, nil
	}
	if m.services.Sessions == nil {
		m.setNotice("session fork is unavailable", true)
		return m, nil
	}
	child, err := m.services.Sessions.Fork(m.sessionID)
	if err != nil {
		m.setNotice("fork failed: "+err.Error(), true)
		return m, nil
	}
	id := strings.TrimSpace(child.ID)
	if id == "" || id == m.sessionID {
		m.setNotice("fork failed: empty child session", true)
		return m, nil
	}
	m.pendingResume = id
	m.setNotice("forked → "+shortSessionID(id)+" (switching…)", false)
	return m, tea.Quit
}

func (m Model) handleSessionCommand(args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	if m.turnRunning {
		m.setNotice("wait for the current turn to finish before switching sessions", true)
		return m, nil
	}
	if len(args) >= 1 {
		id := strings.TrimSpace(args[0])
		if id == "" {
			m.setNotice("usage: /session [id]", true)
			return m, nil
		}
		if id == m.sessionID {
			m.setNotice("already on session "+shortSessionID(id), false)
			return m, nil
		}
		if m.services.Sessions != nil {
			info, ok, err := m.services.Sessions.Get(id)
			if err != nil {
				m.setNotice("session: "+err.Error(), true)
				return m, nil
			}
			if !ok {
				m.setNotice("session "+id+" not found", true)
				return m, nil
			}
			if info.ParentID != "" {
				m.setNotice("session is a subagent transcript; pick a root session", true)
				return m, nil
			}
		}
		return m, func() tea.Msg { return sessionResumeMsg{id: id} }
	}
	if m.services.Sessions == nil {
		m.setNotice("session list unavailable", true)
		return m, nil
	}
	m.modal = newSessionModal(m.services.Sessions, m.sessionID)
	return m, nil
}

func (m Model) handleMemoryCommand(args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	if m.services.Memory == nil {
		m.setNotice("project memory is unavailable", true)
		return m, nil
	}
	usage := "usage: /memory [list [tag]|get <key>|set <key> <value>|rm <key>|export [path]|import <path> [--replace]]"
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list", "ls":
		tag := ""
		if len(args) > 1 {
			tag = args[1]
		}
		if _, err := m.services.Memory.List(tag); err != nil {
			m.setNotice("memory: "+err.Error(), true)
			return m, nil
		}
		return m.openMemoryBrowser(tag)
	case "get":
		if len(args) < 2 {
			m.setNotice(usage, true)
			return m, nil
		}
		entry, ok, err := m.services.Memory.Get(args[1])
		if err != nil {
			m.setNotice("memory: "+err.Error(), true)
			return m, nil
		}
		if !ok {
			m.setNotice("memory: no entry for "+args[1], true)
			return m, nil
		}
		msg := entry.Key + "=" + entry.Value
		if len(entry.Tags) > 0 {
			msg += " [" + strings.Join(entry.Tags, ", ") + "]"
		}
		m.setNotice("memory: "+msg, false)
		return m, nil
	case "set", "add", "put":
		if len(args) < 3 {
			m.setNotice(usage, true)
			return m, nil
		}
		key := args[1]
		value := strings.Join(args[2:], " ")
		if err := m.services.Memory.Put(key, value, nil); err != nil {
			m.setNotice("memory: "+err.Error(), true)
			return m, nil
		}
		m.windows = refreshProjectDataWindows(m.windows)
		m.setNotice("memory: set "+key, false)
		return m, nil
	case "rm", "delete", "del", "remove":
		if len(args) < 2 {
			m.setNotice(usage, true)
			return m, nil
		}
		if err := m.services.Memory.Delete(args[1]); err != nil {
			m.setNotice("memory: "+err.Error(), true)
			return m, nil
		}
		m.windows = refreshProjectDataWindows(m.windows)
		m.setNotice("memory: deleted "+args[1], false)
		return m, nil
	case "export":
		path := "strike-memory.json"
		if len(args) > 1 {
			path = args[1]
		}
		if err := m.services.Memory.Export(path); err != nil {
			m.setNotice("memory: "+err.Error(), true)
			return m, nil
		}
		m.setNotice("memory: exported to "+path, false)
		return m, nil
	case "import":
		path, replace, ok := parseImportArgs(args[1:])
		if !ok {
			m.setNotice(usage, true)
			return m, nil
		}
		n, err := m.services.Memory.Import(path, replace)
		if err != nil {
			m.setNotice("memory: "+err.Error(), true)
			return m, nil
		}
		m.windows = refreshProjectDataWindows(m.windows)
		mode := "merged"
		if replace {
			mode = "replaced"
		}
		m.setNotice(fmt.Sprintf("memory: imported %d entries (%s)", n, mode), false)
		return m, nil
	default:
		m.setNotice(usage, true)
		return m, nil
	}
}

func (m Model) handleIssuesCommand(args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	if m.services.Issues == nil {
		m.setNotice("project issues are unavailable", true)
		return m, nil
	}
	usage := "usage: /issues [list [open|closed]|add <title>|get <id>|close <id>|export [path]|import <path> [--replace]]"
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list", "ls":
		status := ""
		if len(args) > 1 {
			status = args[1]
		}
		if _, err := m.services.Issues.List(status); err != nil {
			m.setNotice("issues: "+err.Error(), true)
			return m, nil
		}
		return m.openIssuesBrowser(status)
	case "add", "create", "new":
		if len(args) < 2 {
			m.setNotice(usage, true)
			return m, nil
		}
		title := strings.Join(args[1:], " ")
		iss, err := m.services.Issues.Create(title, "")
		if err != nil {
			m.setNotice("issues: "+err.Error(), true)
			return m, nil
		}
		m.windows = refreshProjectDataWindows(m.windows)
		m.setNotice(fmt.Sprintf("issues: opened #%d %s", iss.ID, iss.Title), false)
		return m, nil
	case "get", "show":
		if len(args) < 2 {
			m.setNotice(usage, true)
			return m, nil
		}
		id, err := strconv.Atoi(args[1])
		if err != nil || id < 1 {
			m.setNotice("issues: id must be a positive integer", true)
			return m, nil
		}
		iss, ok, err := m.services.Issues.Get(id)
		if err != nil {
			m.setNotice("issues: "+err.Error(), true)
			return m, nil
		}
		if !ok {
			m.setNotice(fmt.Sprintf("issues: no issue #%d", id), true)
			return m, nil
		}
		msg := fmt.Sprintf("#%d [%s] %s", iss.ID, iss.Status, iss.Title)
		if iss.Body != "" {
			msg += ": " + iss.Body
		}
		m.setNotice("issues: "+msg, false)
		return m, nil
	case "close":
		if len(args) < 2 {
			m.setNotice(usage, true)
			return m, nil
		}
		id, err := strconv.Atoi(args[1])
		if err != nil || id < 1 {
			m.setNotice("issues: id must be a positive integer", true)
			return m, nil
		}
		iss, err := m.services.Issues.Close(id)
		if err != nil {
			m.setNotice("issues: "+err.Error(), true)
			return m, nil
		}
		m.windows = refreshProjectDataWindows(m.windows)
		m.setNotice(fmt.Sprintf("issues: closed #%d %s", iss.ID, iss.Title), false)
		return m, nil
	case "export":
		path := "strike-issues.json"
		if len(args) > 1 {
			path = args[1]
		}
		if err := m.services.Issues.Export(path); err != nil {
			m.setNotice("issues: "+err.Error(), true)
			return m, nil
		}
		m.setNotice("issues: exported to "+path, false)
		return m, nil
	case "import":
		path, replace, ok := parseImportArgs(args[1:])
		if !ok {
			m.setNotice(usage, true)
			return m, nil
		}
		n, err := m.services.Issues.Import(path, replace)
		if err != nil {
			m.setNotice("issues: "+err.Error(), true)
			return m, nil
		}
		m.windows = refreshProjectDataWindows(m.windows)
		mode := "merged"
		if replace {
			mode = "replaced"
		}
		m.setNotice(fmt.Sprintf("issues: imported %d issues (%s)", n, mode), false)
		return m, nil
	default:
		m.setNotice(usage, true)
		return m, nil
	}
}

// parseImportArgs accepts: <path> | <path> --replace|--merge | --replace|--merge <path>
func parseImportArgs(args []string) (path string, replace bool, ok bool) {
	if len(args) == 0 || len(args) > 2 {
		return "", false, false
	}
	replace = false
	var paths []string
	for _, a := range args {
		switch a {
		case "--replace", "replace":
			replace = true
		case "--merge", "merge":
			replace = false
		default:
			if strings.HasPrefix(a, "-") {
				return "", false, false
			}
			paths = append(paths, a)
		}
	}
	if len(paths) != 1 || paths[0] == "" {
		return "", false, false
	}
	return paths[0], replace, true
}

func (m Model) handleMDRead(text string, fields []string) (tea.Model, tea.Cmd) {
	pathArg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), fields[0]))
	m.resetComposer()
	if pathArg == "" {
		m.setNotice("usage: /md-read <path>", true)
		return m, nil
	}
	if m.services.Files == nil {
		m.setNotice("file reading is unavailable", true)
		return m, nil
	}
	data, err := m.services.Files.ReadFile(pathArg)
	if err != nil {
		m.setNotice("md-read: "+err.Error(), true)
		return m, nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		m.setNotice("md-read: binary files are not supported", true)
		return m, nil
	}

	loaded := newMarkdownWindow().load(pathArg, string(data))
	var ok bool
	m.windows, ok = m.windows.replace(markdownWindowID, loaded, true)
	if !ok {
		m.setNotice("md-read: markdown window missing", true)
		return m, nil
	}
	m.clearNotice()
	cmd := m.setPaneFocus(focusRight)
	m.reflow() // apply pane dimensions so glamour runs at real width
	return m, cmd
}

func (m Model) submit(op protocol.UserInput, displayPrompt string) (tea.Model, tea.Cmd) {
	// Child transcript view is display-only; engine UserInput always targets root.
	if m.viewingChild() {
		m.setNotice("viewing subagent — return to parent to send (esc or ctrl+x up)", true)
		return m, nil
	}
	// Policy: enqueue while a turn runs (do not reject/wipe). Drain FIFO on
	// TurnCompleted. Queue survives Interrupt until pop/clear.
	if m.turnRunning {
		return m.enqueueUserInput(op, displayPrompt)
	}
	return m.dispatchUserInput(op, displayPrompt)
}

func (m Model) sendSelect(op protocol.SelectModel) (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	ops := m.ops
	return m, func() tea.Msg {
		ops <- op
		return nil
	}
}

// handleThemeCommand opens the theme picker, applies a named JSON theme, or
// sets session-local appearance (dark|light|auto).
func (m Model) handleThemeCommand(args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	if len(args) == 0 {
		m.clearNotice()
		m.modal = newThemeModal(theme.Catalog(m.workDir), m.themeID, m.services.Settings)
		m.reflow()
		return m, nil
	}
	arg := strings.ToLower(strings.TrimSpace(args[0]))
	if mode, ok := parseAppearance(arg); ok {
		m.appearance = mode
		applyAppearance(m.appearance)
		m.restyleWidgets()
		m.setNotice("appearance: "+string(m.appearance), false)
		m.reflow()
		m.refreshViewport()
		return m, nil
	}
	entry, ok := theme.Lookup(theme.Catalog(m.workDir), arg)
	if !ok {
		// Also try the raw (case-preserving) id from args[0].
		entry, ok = theme.Lookup(theme.Catalog(m.workDir), strings.TrimSpace(args[0]))
	}
	if !ok {
		m.setNotice("unknown theme "+args[0]+" — try /theme", true)
		return m, nil
	}
	m.applyThemeEntry(entry)
	m.setNotice("theme: "+entry.ID, false)
	return m, nil
}

// applyThemeEntry swaps the active palette and restyles widgets/viewport.
func (m *Model) applyThemeEntry(entry theme.Entry) {
	m.th = entry.Theme.Resolve()
	m.themeID = entry.ID
	m.restyleWidgets()
	m.reflow()
	m.refreshViewport()
}

// handleFastCommand toggles or sets the session priority-tier preference.
// Bare /fast flips the current value; on/off/true/false/1/0 set it explicitly.
func (m Model) handleFastCommand(args []string) (tea.Model, tea.Cmd) {
	enabled := !m.fastEnabled
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "on", "true", "1", "yes":
			enabled = true
		case "off", "false", "0", "no":
			enabled = false
		default:
			m.setNotice("usage: /fast [on|off]", true)
			return m, nil
		}
	}
	m.resetComposer()
	m.clearNotice()
	ops := m.ops
	return m, func() tea.Msg {
		ops <- protocol.SetFast{Enabled: enabled}
		return nil
	}
}

// handleThinkCommand toggles reasoning/CoT visibility in the transcript.
// Pure UI preference — no engine op. Bare /think flips; on/off set explicitly.
func (m Model) handleThinkCommand(args []string) (tea.Model, tea.Cmd) {
	enabled := !m.showThinking
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "on", "true", "1", "yes":
			enabled = true
		case "off", "false", "0", "no":
			enabled = false
		default:
			m.setNotice("usage: /think [on|off]", true)
			return m, nil
		}
	}
	m.resetComposer()
	m.showThinking = enabled
	if enabled {
		m.setNotice("thinking visible", false)
	} else {
		m.setNotice("thinking hidden", false)
	}
	m.refreshViewport()
	return m, nil
}

// fastNotice explains what the toggle means. Kept free of host I/O so it is
// safe to call from the Bubble Tea update path (no catalog/network).
func (m Model) fastNotice(enabled bool) string {
	if !enabled {
		return "fast off"
	}
	return "fast on — OpenAI platform priority tier (~2×); ignored by other providers and ChatGPT subscription"
}

// saveDefaultsCmd persists provider/model/agent/effort defaults through the
// host settings service, reporting the outcome as a defaultsSavedMsg.
func (m Model) saveDefaultsCmd(provider, model, agent, effort, text string) tea.Cmd {
	return saveDefaultsThroughCmd(m.services.Settings, provider, model, agent, effort, text)
}

// effortChoices renders the selectable levels for error and help text.
func effortChoices() string {
	names := make([]string, 0, len(protocol.Efforts()))
	for _, level := range protocol.Efforts() {
		names = append(names, string(level))
	}
	return strings.Join(names, "|")
}

func autonomyChoices() string {
	names := make([]string, 0, len(protocol.Autonomies()))
	for _, mode := range protocol.Autonomies() {
		names = append(names, string(mode))
	}
	return strings.Join(names, "|")
}

func (m *Model) setNotice(text string, isErr bool) {
	m.notice, m.noticeErr = text, isErr
	m.noticeCause = noticeGeneral
}

func (m *Model) setNeedsModelNotice(text string, isErr bool) {
	m.notice, m.noticeErr = text, isErr
	m.noticeCause = noticeNeedsModel
}

func (m *Model) clearNotice() {
	m.notice, m.noticeErr = "", false
	m.noticeCause = noticeGeneral
}

// lastCell returns the trailing cell if it has type T; a new message part
// starts a new cell otherwise.
func lastCell[T cell](cells []cell) (T, bool) {
	var zero T
	if len(cells) == 0 {
		return zero, false
	}
	last, ok := cells[len(cells)-1].(T)
	return last, ok
}

// completeAssistantCells marks every assistant transcript cell complete so
// markdown rendering runs for finished replies (including those no longer trailing).
func (m *Model) completeAssistantCells() {
	for _, c := range m.cells {
		if a, ok := c.(*assistantCell); ok {
			a.complete = true
			a.mdCacheOK = false
		}
	}
}

func (m *Model) refreshViewport() {
	if !m.ready {
		return
	}
	width := max(1, m.viewport.Width)
	cells := m.displayCells()
	if len(cells) == 0 {
		m.viewport.SetContent("")
		m.viewport.GotoTop()
		m.transcriptPlainLines = nil
		m.selectedFileRef = -1
		return
	}
	m.syncToolSelectionFlags()
	// Stick to bottom only when already anchored; otherwise preserve scroll
	// so users reading history are not yanked down on each event.
	atBottom := m.viewport.AtBottom()
	yOff := m.viewport.YOffset
	blocks := make([]string, 0, len(cells))
	for _, c := range cells {
		if _, ok := c.(*reasoningCell); ok && !m.showThinking {
			continue
		}
		blocks = append(blocks, m.renderCell(c, width))
	}
	// Live working chrome in the transcript when a turn is running and no
	// assistant/tool content has arrived yet (providers with no CoT stream).
	if m.turnRunning && !m.viewingChild() {
		if thinkingPlaceholderVisible(cells, m.showThinking) {
			blocks = append(blocks, renderThinkingPlaceholder(width, m.th, m.turnStartedAt))
		}
	}
	content := strings.Join(blocks, "\n\n")
	m.transcriptPlainLines = strings.Split(ansi.Strip(content), "\n")
	if m.selectedFileRef >= len(m.collectFileRefs()) {
		m.selectedFileRef = -1
	}
	linked := postLinkifyRendered(content, m.th, m.workDir)
	m.viewport.SetContent(linked)
	if atBottom {
		m.viewport.GotoBottom()
	} else {
		m.viewport.SetYOffset(yOff)
	}
}

// renderCell paints one transcript cell, attaching OSC 8 file links using the
// session work directory as the relative-path base.
func (m *Model) renderCell(c cell, width int) string {
	switch tc := c.(type) {
	case *toolCell:
		return tc.renderLinked(width, m.th, m.workDir)
	case *exploreCell:
		return tc.renderLinked(width, m.th, m.workDir)
	default:
		return c.render(width, m.th)
	}
}

// handleToolCellKeys handles tool selection (alt+[/]), expand/open-at-line
// (enter), copy (y), and post-edit review (v) when the composer is empty.
// handled is true when the key was consumed; cmd may launch the editor or clear
// a copied flash.
func (m *Model) handleToolCellKeys(msg tea.KeyMsg) (handled bool, cmd tea.Cmd) {
	if m.focus != focusLeft || m.modal != nil || m.completion != nil {
		return false, nil
	}
	if strings.TrimSpace(m.composer.Value()) != "" {
		return false, nil
	}
	switch {
	case key.Matches(msg, m.keyMap.ToolPrev):
		m.moveToolSelection(-1)
		return true, nil
	case key.Matches(msg, m.keyMap.ToolNext):
		m.moveToolSelection(1)
		return true, nil
	case key.Matches(msg, m.keyMap.ToolExpand), key.Matches(msg, m.keyMap.Send):
		if m.toggleSelectedTool() {
			return true, nil
		}
		if ref, ok := m.fileRefForEnter(); ok {
			next, c := m.openFileRef(ref)
			*m = next.(Model)
			return true, c
		}
		return false, nil
	case key.Matches(msg, m.keyMap.ToolCopy):
		return m.copySelectedCell()
	case key.Matches(msg, m.keyMap.ToolReview):
		return m.reviewSelectedTool()
	}
	return false, nil
}

func (m *Model) selectableCellIndexes() []int {
	var idx []int
	for i, c := range m.displayCells() {
		switch tc := c.(type) {
		case *toolCell:
			if tc.collapsible() || tc.reviewable() {
				idx = append(idx, i)
			}
		case *exploreCell:
			if tc.collapsible() {
				idx = append(idx, i)
			}
		}
	}
	return idx
}

func (m *Model) moveToolSelection(delta int) {
	idxs := m.selectableCellIndexes()
	if len(idxs) == 0 {
		m.selectedCell = -1
		return
	}
	cur := -1
	for i, cellIdx := range idxs {
		if cellIdx == m.selectedCell {
			cur = i
			break
		}
	}
	if cur < 0 {
		if delta < 0 {
			m.selectedCell = idxs[len(idxs)-1]
		} else {
			m.selectedCell = idxs[0]
		}
		return
	}
	next := cur + delta
	if next < 0 {
		next = 0
	}
	if next >= len(idxs) {
		next = len(idxs) - 1
	}
	m.selectedCell = idxs[next]
}

func (m *Model) toggleSelectedTool() bool {
	cells := m.displayCells()
	// Expand only applies to collapsible cells; keep selection among those.
	var idxs []int
	for i, c := range cells {
		switch tc := c.(type) {
		case *toolCell:
			if tc.collapsible() {
				idxs = append(idxs, i)
			}
		case *exploreCell:
			if tc.collapsible() {
				idxs = append(idxs, i)
			}
		}
	}
	if len(idxs) == 0 {
		return false
	}
	if m.selectedCell < 0 || m.selectedCell >= len(cells) {
		m.selectedCell = idxs[len(idxs)-1]
	} else {
		// Ensure current selection is still collapsible; else jump to last.
		ok := false
		for _, i := range idxs {
			if i == m.selectedCell {
				ok = true
				break
			}
		}
		if !ok {
			m.selectedCell = idxs[len(idxs)-1]
		}
	}
	switch c := cells[m.selectedCell].(type) {
	case *toolCell:
		return c.toggleExpanded()
	case *exploreCell:
		return c.toggleExpanded()
	}
	return false
}

// reviewSelectedTool opens the selected file-mutating tool's path at the first
// changed hunk. Does not consume "v" when nothing is selected so typing still
// reaches the empty composer.
func (m *Model) reviewSelectedTool() (bool, tea.Cmd) {
	cells := m.displayCells()
	if m.selectedCell < 0 || m.selectedCell >= len(cells) {
		return false, nil
	}
	tc, ok := cells[m.selectedCell].(*toolCell)
	if !ok {
		m.setNotice("select an edit tool cell to review", true)
		return true, nil
	}
	path, line, ok := tc.reviewTarget(m.workDir)
	if !ok {
		m.setNotice("no file to review on this tool", true)
		return true, nil
	}
	if line < 1 {
		line = 1
	}
	updated, cmd := (*m).openFileRef(fileRef{Path: path, Line: line})
	*m = updated.(Model)
	return true, cmd
}

func (m *Model) syncToolSelectionFlags() {
	for i, c := range m.displayCells() {
		sel := i == m.selectedCell
		switch tc := c.(type) {
		case *toolCell:
			tc.selected = sel
		case *exploreCell:
			tc.selected = sel
		}
	}
}

// collectFileRefs returns path:line citations in transcript order.
func (m Model) collectFileRefs() []fileRef {
	var refs []fileRef
	for _, line := range m.transcriptPlainLines {
		for _, sp := range findFileRefSpans(line) {
			refs = append(refs, sp.fileRef)
		}
	}
	return refs
}

// fileRefForEnter picks the click-selected citation, else the most recent one.
func (m Model) fileRefForEnter() (fileRef, bool) {
	refs := m.collectFileRefs()
	if len(refs) == 0 {
		return fileRef{}, false
	}
	if m.selectedFileRef >= 0 && m.selectedFileRef < len(refs) {
		return refs[m.selectedFileRef], true
	}
	return refs[len(refs)-1], true
}

// openFileRef launches the configured editor at path:line via /vim plumbing.
func (m Model) openFileRef(ref fileRef) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(ref.Path) == "" {
		return m, nil
	}
	args := []string{ref.Path}
	if ref.Line > 0 {
		args = []string{ref.Path + ":" + itoa(ref.Line)}
	}
	// Remember selection so a subsequent empty enter re-opens the same ref.
	refs := m.collectFileRefs()
	m.selectedFileRef = -1
	for i, r := range refs {
		if r.Path == ref.Path && r.Line == ref.Line {
			m.selectedFileRef = i
			break
		}
	}
	return m.handleVimCommand(args)
}

// fileRefAtMouse maps a left-click in the transcript viewport to a path:line.
func (m Model) fileRefAtMouse(msg tea.MouseMsg) (fileRef, bool) {
	if m.modal != nil || len(m.transcriptPlainLines) == 0 {
		return fileRef{}, false
	}
	ox, oy, ok := m.transcriptContentOrigin()
	if !ok {
		return fileRef{}, false
	}
	relY := msg.Y - oy
	relX := msg.X - ox
	if relY < 0 || relX < 0 || relY >= m.viewport.Height {
		return fileRef{}, false
	}
	lineIdx := m.viewport.YOffset + relY
	if lineIdx < 0 || lineIdx >= len(m.transcriptPlainLines) {
		return fileRef{}, false
	}
	return fileRefAtColumn(m.transcriptPlainLines[lineIdx], relX)
}

// transcriptContentOrigin is the top-left cell of the transcript viewport body
// in screen coordinates (after header and panel chrome).
func (m Model) transcriptContentOrigin() (x, y int, ok bool) {
	if !m.ready || len(m.displayCells()) == 0 || m.viewport.Height <= 0 {
		return 0, 0, false
	}
	th := m.th.Resolve()
	gutter := th.Spacing.XS
	leftWidth := m.width
	showLeft := true
	if m.splitOrientation != orientVertical {
		geo := computePaneGeometry(m.width, gutter, m.focus)
		if geo.mode == paneSingle && m.focus == focusRight {
			return 0, 0, false
		}
		leftWidth = geo.leftCandidateWidth(m.width)
	} else {
		l0 := computeLayout(m.width, m.height, m.composer.Height(), m.completionPopupHeightFor(m.width), m.dangerouslySkipPermissions, m.noticeRowsFor(m.width))
		bodyHeight := l0.transcript + l0.notice + l0.popup + l0.composer
		geo := computeVerticalPaneGeometry(m.width, bodyHeight, gutter, m.focus)
		if geo.mode == paneSingle && m.focus == focusRight {
			return 0, 0, false
		}
		leftWidth = m.width
		showLeft = !(geo.mode == paneSingle && m.focus == focusRight)
	}
	if !showLeft {
		return 0, 0, false
	}
	l := computeLayout(leftWidth, m.height, m.composer.Height(), m.completionPopupHeightFor(leftWidth), m.dangerouslySkipPermissions, m.noticeRowsFor(leftWidth))
	if m.splitOrientation == orientVertical {
		bodyHeight := l.transcript + l.notice + l.popup + l.composer
		geo := computeVerticalPaneGeometry(m.width, bodyHeight, gutter, m.focus)
		if geo.mode == paneSplit {
			l = l.withBodyHeight(geo.leftHeight)
		}
	}
	if l.transcript <= 0 {
		return 0, 0, false
	}
	y = l.header
	x = 0
	compact := leftWidth < compactWidth || m.height < compactHeight
	if !compact {
		// Panel top border + left border + horizontal padding.
		y++
		if leftWidth >= 3 {
			x = 1
			if leftWidth >= 6 {
				padX := th.Spacing.XS
				if padX < 0 {
					padX = 0
				}
				if maxPad := (leftWidth - 3) / 2; padX > maxPad {
					padX = maxPad
				}
				x += padX
			}
		}
	}
	return x, y, true
}

// copySelectedCell stages OSC52 for the selected (or latest copyable)
// transcript cell and starts a brief "copied" flash. Returns false when
// nothing was copyable so bare y can fall through to the composer.
func (m *Model) copySelectedCell() (bool, tea.Cmd) {
	cells := m.displayCells()
	idx := m.resolveCopyCellIndex()
	if idx < 0 {
		return false, nil
	}
	text := cellCopyText(cells[idx])
	if text == "" {
		return false, nil
	}
	// Only collapsible/reviewable tool rows keep a sticky selection; chat
	// cells are copy targets without changing tool-nav selection.
	switch cells[idx].(type) {
	case *toolCell, *exploreCell:
		m.selectedCell = idx
	}
	m.cellClip.stage(text)
	m.copyFlashGen++
	gen := m.copyFlashGen
	setCellCopiedFlash(cells[idx], true)
	// Clear any other cell flashes so only the copied row shows feedback.
	for i, c := range cells {
		if i == idx {
			continue
		}
		clearCellCopiedFlash(c)
	}
	return true, tea.Tick(cellCopiedFlash, func(time.Time) tea.Msg {
		return clearCellCopiedFlashMsg{idx: idx, gen: gen}
	})
}

// cellCopyText returns the y-to-copy payload for a transcript cell, or empty.
func cellCopyText(c cell) string {
	switch tc := c.(type) {
	case *toolCell:
		return tc.copyText()
	case *exploreCell:
		return tc.copyText()
	case *assistantCell:
		return tc.copyText()
	case *reasoningCell:
		return tc.copyText()
	case *userCell:
		return tc.copyText()
	default:
		return ""
	}
}

func setCellCopiedFlash(c cell, on bool) {
	switch tc := c.(type) {
	case *toolCell:
		tc.copiedFlash = on
	case *exploreCell:
		tc.copiedFlash = on
	case *assistantCell:
		tc.copiedFlash = on
	case *userCell:
		tc.copiedFlash = on
	}
}

func clearCellCopiedFlash(c cell) {
	setCellCopiedFlash(c, false)
}

// resolveCopyCellIndex prefers the current tool/explore selection when it has
// copyable content; otherwise the latest tool/explore, then assistant, then
// user cell with a non-empty payload.
func (m *Model) resolveCopyCellIndex() int {
	cells := m.displayCells()
	if m.selectedCell >= 0 && m.selectedCell < len(cells) {
		if cellCopyText(cells[m.selectedCell]) != "" {
			switch cells[m.selectedCell].(type) {
			case *toolCell, *exploreCell:
				return m.selectedCell
			}
		}
	}
	latestTool, latestAsst, latestUser := -1, -1, -1
	for i := len(cells) - 1; i >= 0; i-- {
		if cellCopyText(cells[i]) == "" {
			continue
		}
		switch cells[i].(type) {
		case *toolCell, *exploreCell:
			if latestTool < 0 {
				latestTool = i
			}
		case *assistantCell:
			if latestAsst < 0 {
				latestAsst = i
			}
		case *userCell:
			if latestUser < 0 {
				latestUser = i
			}
		}
		if latestTool >= 0 && latestAsst >= 0 && latestUser >= 0 {
			break
		}
	}
	if latestTool >= 0 {
		return latestTool
	}
	if latestAsst >= 0 {
		return latestAsst
	}
	return latestUser
}

func (m Model) View() string {
	if !m.ready {
		if warning := m.dangerView(0); warning != "" {
			return warning + "\nstarting…"
		}
		return "starting…"
	}

	gutter := m.th.Resolve().Spacing.XS
	leftWidth := m.width
	var hGeometry paneGeometry
	if m.splitOrientation != orientVertical {
		hGeometry = computePaneGeometry(m.width, gutter, m.focus)
		leftWidth = hGeometry.leftCandidateWidth(m.width)
	}
	l := computeLayout(leftWidth, m.height, m.composer.Height(), m.completionPopupHeightFor(leftWidth), m.dangerouslySkipPermissions, m.noticeRowsFor(leftWidth))
	bodyHeight := l.transcript + l.notice + l.popup + l.composer
	rightWidth, rightHeight := 0, bodyHeight
	showLeft, showRight := true, false
	vGutter := 0
	splitVertical := false

	if m.splitOrientation == orientVertical {
		geo := computeVerticalPaneGeometry(m.width, bodyHeight, gutter, m.focus)
		if geo.mode == paneSplit {
			splitVertical = true
			l = l.withBodyHeight(geo.leftHeight)
			bodyHeight = l.transcript + l.notice + l.popup + l.composer
			rightWidth, rightHeight = geo.rightWidth, geo.rightHeight
			vGutter = geo.gutter
			showLeft, showRight = true, true
		} else if m.focus == focusRight {
			showLeft, showRight = false, true
			rightWidth = m.width
			if geo.rightHeight > 0 {
				rightHeight = geo.rightHeight
			}
		} else {
			showLeft, showRight = true, false
		}
	} else if hGeometry.mode == paneSplit {
		showLeft, showRight = true, true
		rightWidth, rightHeight = hGeometry.rightWidth, bodyHeight
	} else if m.focus == focusRight {
		showLeft, showRight = false, true
		rightWidth = m.width
	}

	compact := leftWidth < compactWidth || m.height < compactHeight
	var leftBody string
	if showLeft {
		left := make([]string, 0, 4)
		if l.transcript > 0 {
			left = append(left, m.transcriptView(compact, leftWidth, l.transcript))
		}
		if l.notice > 0 {
			left = append(left, m.noticeView(leftWidth, l.notice))
		}
		if m.modal == nil && l.popup > 0 {
			if popup := m.completion.view(leftWidth, l.popup, m.th); popup != "" {
				left = append(left, popup)
			}
		}
		if l.composer > 0 {
			left = append(left, m.composerView(compact, leftWidth, l.composer))
		}
		leftBody = lipgloss.JoinVertical(lipgloss.Left, left...)
	}

	var body string
	switch {
	case showLeft && showRight && splitVertical:
		rightCompact := m.width < compactWidth || m.height < compactHeight
		right := m.rightPaneView(rightWidth, rightHeight, rightCompact)
		body = lipgloss.JoinVertical(lipgloss.Left, leftBody, paneGutter(m.th, m.width, vGutter), right)
	case showLeft && showRight:
		body = lipgloss.JoinHorizontal(lipgloss.Top, leftBody, paneGutter(m.th, hGeometry.gutter, bodyHeight), m.rightPaneView(rightWidth, rightHeight, false))
	case showRight:
		rightCompact := m.width < compactWidth || m.height < compactHeight
		body = m.rightPaneView(rightWidth, rightHeight, rightCompact)
	default:
		body = leftBody
	}

	// Full body band height for overlay centering (left stack and/or right).
	bandHeight := bodyHeight
	if splitVertical && showLeft && showRight {
		bandHeight = bodyHeight + vGutter + rightHeight
	}

	contentParts := make([]string, 0, 2)
	if l.header > 0 {
		contentParts = append(contentParts, m.headerView(m.width))
	}
	if bandHeight > 0 && body != "" {
		contentParts = append(contentParts, body)
	}
	content := strings.Join(contentParts, "\n")
	contentHeight := l.header + bandHeight

	footer := make([]string, 0, 2)
	if l.hints > 0 {
		footer = append(footer, m.hintsView(m.width))
	}
	if l.danger > 0 {
		warning := m.dangerView(m.width)
		if warning != "" {
			footer = append(footer, warning)
		}
	}

	if m.modal != nil {
		var overlay string
		if tm, ok := m.modal.(*terminalModal); ok {
			tm.setHostSize(m.width, contentHeight)
			overlay = tm.view(max(40, m.width-4), m.th)
		} else {
			overlay = m.modal.view(max(8, ui.ModalWidth(m.width)), m.th)
		}
		content = ui.OverlayCenter(m.th, content, overlay, m.width, contentHeight)
	}
	parts := make([]string, 0, 1+len(footer))
	if content != "" {
		parts = append(parts, content)
	}
	parts = append(parts, footer...)
	frame := ui.Canvas(m.th, m.width, m.height, strings.Join(parts, "\n"))
	// Prepend OSC52 after Canvas so overlay/ansi.Cut cannot strip it.
	if wm, ok := m.modal.(*authWaitModal); ok {
		if osc := wm.TakeCopyOSC(); osc != "" {
			return osc + frame
		}
	}
	if osc := m.cellClip.take(); osc != "" {
		return osc + frame
	}
	return frame
}

// paletteResultFocus reveals a newly produced left-side notice when the right
// pane is the only visible pane. Existing notices do not move focus: only the
// result of the selected palette action does.
func (m Model) paletteResultFocus(priorNotice string, priorNoticeErr bool, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	gutter := m.th.Resolve().Spacing.XS
	singleRight := m.focus == focusRight
	if m.splitOrientation == orientVertical {
		bodyGuess := max(0, m.height-2)
		geo := computeVerticalPaneGeometry(m.width, bodyGuess, gutter, m.focus)
		singleRight = singleRight && geo.mode == paneSingle
	} else {
		geometry := computePaneGeometry(m.width, gutter, m.focus)
		singleRight = singleRight && geometry.mode == paneSingle
	}
	producedNotice := m.modal == nil && m.notice != "" && (m.notice != priorNotice || m.noticeErr != priorNoticeErr)
	if !singleRight || !producedNotice {
		return m, cmd
	}
	focusCmd := m.setPaneFocus(focusLeft)
	m.reflow()
	return m, tea.Batch(cmd, focusCmd)
}

// completionPopupHeight returns the reserved height of the completion popup
// for the current View, mirroring what reflow computed.
func (m Model) completionPopupHeight() int {
	leftWidth := m.width
	if m.splitOrientation != orientVertical {
		geometry := computePaneGeometry(m.width, m.th.Resolve().Spacing.XS, m.focus)
		leftWidth = geometry.leftCandidateWidth(m.width)
	}
	return m.completionPopupHeightFor(leftWidth)
}

func (m Model) completionPopupHeightFor(width int) int {
	if m.modal != nil || m.completion == nil || m.completion.rows <= 0 {
		return 0
	}
	borderRows := 0
	if width >= 4 {
		borderRows = 2
	}
	return m.completion.rows + borderRows
}

// compact reports whether the screen is below the breakpoints for bordered
// chrome; below it, panels degrade to plain viewport+composer.
func (m Model) compact() bool {
	leftWidth := m.width
	if m.splitOrientation != orientVertical {
		geometry := computePaneGeometry(m.width, m.th.Resolve().Spacing.XS, m.focus)
		leftWidth = geometry.leftCandidateWidth(m.width)
	}
	return leftWidth < compactWidth || m.height < compactHeight
}
