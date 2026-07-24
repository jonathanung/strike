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

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

type historyAddedMsg struct {
	err error
}

// Options carries frontend-only construction flags. Host capabilities
// (credentials, catalog, settings, history, agents, skills) arrive through
// host.Services instead.
type Options struct {
	DangerouslySkipPermissions bool
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
	cells    []cell
	toolByID map[string]*toolCell
	modal    modal

	viewport                   viewport.Model
	composer                   textarea.Model
	completion                 *completionState
	commands                   []commandSpec
	spin                       spinner.Model
	entries                    []string
	historyPos                 int
	historyDraft               string
	dangerouslySkipPermissions bool

	providerName string
	modelName    string
	agentName    string
	effort       protocol.Effort
	// fastEnabled is the session priority-tier preference from /fast.
	fastEnabled bool
	agents      []string     // cycled with tab
	skills      []host.Skill // slash-command templates, pre-filtered by the host
	notice      string
	noticeErr   bool
	turnRunning bool
	width       int
	height      int
	ready       bool
}

// New builds the frontend model. services supplies every host capability; any
// field of it may be nil/empty and the UI degrades gracefully. Options is
// variadic for backward-compatible call sites.
func New(ops chan<- protocol.Op, events <-chan protocol.Event, services host.Services, options ...Options) Model {
	ta := textarea.New()
	ta.Placeholder = "Ask strike anything… (/provider to pick a model, enter to send)"
	ta.Prompt = "┃ "
	ta.MaxHeight = composerMaxHeight
	ta.SetHeight(composerMinHeight)
	ta.ShowLineNumbers = false
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot

	m := Model{
		ops:        ops,
		events:     events,
		services:   services,
		agents:     services.Agents,
		skills:     services.Skills,
		commands:   commandCatalog(services.Skills),
		th:         theme.Default(),
		toolByID:   map[string]*toolCell{},
		composer:   ta,
		spin:       sp,
		historyPos: -1,
	}
	for _, option := range options {
		m.dangerouslySkipPermissions = option.DangerouslySkipPermissions
	}
	if services.History != nil {
		m.entries = services.History.Entries()
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.listen(), m.spin.Tick)
}

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
		if !m.ready {
			m.viewport = viewport.New(max(1, m.width), 0)
			m.ready = true
		}
		m.reflow()
		m.refreshViewport()
		return m, nil

	case engineClosedMsg:
		return m, tea.Quit

	case engineEventMsg:
		m.applyEvent(msg.ev)
		m.reflow()
		m.refreshViewport()
		return m, m.listen()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case authStartedMsg, authDeviceMsg, authDoneMsg:
		cmd, _ := m.applyAuthMsg(msg)
		m.reflow()
		return m, cmd

	case modelsLoadedMsg:
		if mm, ok := m.modal.(*modelModal); ok && mm.provider == msg.provider {
			mm.loading = false
			if msg.err != nil {
				mm.loadErr = msg.err.Error()
			} else {
				mm.all = msg.ids
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

	case historyAddedMsg:
		if msg.err != nil {
			m.setNotice("saving prompt history failed: "+msg.err.Error(), true)
		} else if m.services.History != nil {
			m.entries = m.services.History.Entries()
		}
		return m, nil

	case paletteInvokeMsg:
		entry, ok := m.currentPaletteEntry(msg.Action)
		if !ok {
			m.refreshOpenPalette()
			m.setNotice("palette action is no longer available", true)
			return m, nil
		}
		if entry.DisabledReason != "" {
			m.refreshOpenPalette()
			m.setNotice(entry.DisabledReason, true)
			return m, nil
		}
		if _, ok := m.modal.(*paletteModal); ok {
			m.modal = nil
		}
		switch msg.Action.Kind {
		case paletteActionBuiltin:
			return m.handleCommand(msg.Action.Value)
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
			return m, m.composer.Focus()
		}
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.modal != nil {
			var cmd tea.Cmd
			m.modal, cmd = m.modal.update(msg)
			m.reflow()
			return m, cmd
		}
		if msg.String() == "ctrl+k" {
			m.completion = nil
			m.modal = newPaletteModal(m.commands, m.agents, m.currentPaletteAvailability())
			m.reflow()
			return m, nil
		}
		if m.completion != nil {
			switch msg.String() {
			case "esc":
				m.completion = nil
				m.reflow()
				return m, nil
			case "tab", "enter":
				m.applyCompletion()
				return m, nil
			case "up", "ctrl+p":
				m.completion.move(-1)
				m.reflow()
				return m, nil
			case "down", "ctrl+n":
				m.completion.move(1)
				m.reflow()
				return m, nil
			case "alt+enter":
				m.composer.InsertString("\n")
				m.recomputeCompletion()
				m.reflow()
				return m, nil
			default:
				return m.updateComposer(msg)
			}
		}
		if m.handleHistoryKey(msg.String()) {
			return m, nil
		}
		switch msg.String() {
		case "alt+enter":
			m.resetHistoryBrowsing()
			m.composer.InsertString("\n")
			m.recomputeCompletion()
			m.reflow()
			return m, nil
		case "enter":
			text := strings.TrimSpace(m.composer.Value())
			if text == "" {
				return m, nil
			}
			if strings.HasPrefix(text, "/") {
				return m.handleCommand(text)
			}
			if m.providerName == "" {
				m.setNotice("No model selected — use /provider <anthropic|openai|xai|echo> [model]", true)
				return m, nil // keep the typed prompt in the composer
			}
			return m.submit(protocol.UserInput{Text: text}, text)
		case "ctrl+d":
			// Persist the current provider/model/agent as global defaults.
			if m.providerName == "" {
				m.setNotice("nothing to save — select a provider first", true)
				return m, nil
			}
			return m, m.saveDefaultsCmd(m.providerName, m.modelName, m.agentName, string(m.effort), m.providerName+"/"+m.modelName+" · "+m.agentName)
		case "tab":
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
		case "esc":
			if m.turnRunning {
				ops := m.ops
				return m, func() tea.Msg {
					ops <- protocol.Interrupt{}
					return nil
				}
			}
		case "pgup":
			m.viewport.HalfViewUp()
			return m, nil
		case "pgdown":
			m.viewport.HalfViewDown()
			return m, nil
		}
		return m.updateComposer(msg)
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
	if m.historyPos >= 0 && m.composer.Value() != before {
		m.resetHistoryBrowsing()
	}
	m.recomputeCompletion()
	m.reflow()
	return m, cmd
}

func (m *Model) recomputeCompletion() {
	if m.historyPos >= 0 {
		m.completion = nil
		return
	}
	line := m.composer.Line()
	info := m.composer.LineInfo()
	col := info.StartColumn + info.ColumnOffset
	m.completion = leadingSlashCompletion(m.composer.Value(), line, col, m.commands)
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
	name := []rune(candidate.Spec.Name)
	next := make([]rune, 0, len(value)-(replacement.End-replacement.Start)+len(name))
	next = append(next, value[:replacement.Start]...)
	next = append(next, name...)
	next = append(next, value[replacement.End:]...)
	m.setComposerValueAt(string(next), replacement.Start+len(name))
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
	for steps := 0; m.composer.Line() > targetRow && steps <= len(runes)+1; steps++ {
		m.composer.CursorUp()
	}
	m.composer.SetCursor(targetCol)
}

func (m *Model) resetComposer() {
	m.composer.Reset()
	m.completion = nil
	m.resetHistoryBrowsing()
	m.reflow()
}

func (m *Model) handleHistoryKey(key string) bool {
	if m.historyPos >= 0 {
		switch key {
		case "up":
			if m.historyPos > 0 {
				m.historyPos--
			}
			m.recallHistory(m.entries[m.historyPos])
			return true
		case "down":
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
	if key != "up" || m.composer.Value() != "" || len(m.entries) == 0 {
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
	compact := m.compact()
	composerWidth := m.width - 2
	if !compact {
		composerWidth = ui.InnerWidth(m.width)
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
	if m.completion != nil {
		m.completion.rows = 0
		if m.width > 0 {
			borderRows := 0
			if min(m.width, completionMaxWidth) >= 4 {
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
		l := computeLayout(m.width, m.height, composerRows, popupHeight, m.dangerouslySkipPermissions)
		m.viewport.Width = max(1, l.transcriptInnerWidth(m.width))
		m.viewport.Height = max(0, l.transcriptInnerHeight())
	}
}

func (m *Model) applyEvent(ev protocol.Event) {
	switch ev := ev.(type) {
	case protocol.UserMessage:
		m.cells = append(m.cells, &userCell{text: ev.Text})
	case protocol.TurnStarted:
		m.turnRunning = true
		m.refreshOpenPalette()
	case protocol.TextDelta:
		if last, ok := lastCell[*assistantCell](m.cells); ok {
			last.text += ev.Text
		} else {
			m.cells = append(m.cells, &assistantCell{text: ev.Text})
		}
	case protocol.ToolCallBegin:
		tc := &toolCell{callID: ev.CallID, name: ev.Name, args: ev.Args}
		m.toolByID[ev.CallID] = tc
		m.cells = append(m.cells, tc)
	case protocol.ToolCallEnd:
		if tc, ok := m.toolByID[ev.CallID]; ok {
			tc.title, tc.output, tc.done, tc.isError = ev.Title, ev.Output, true, ev.IsError
		}
	case protocol.PermissionAsked:
		m.modal = newPermissionModal(ev, m.ops)
	case protocol.PermissionResolved:
		if modal, ok := m.modal.(*permissionModal); ok && modal.req.RequestID == ev.RequestID {
			m.modal = nil
		}
	case protocol.TurnCompleted:
		m.turnRunning = false
		m.refreshOpenPalette()
	case protocol.ModelSelected:
		m.providerName, m.modelName = ev.Provider, ev.Model
		m.setNotice("model: "+ev.Provider+"/"+ev.Model, false)
		m.refreshOpenPalette()
	case protocol.AgentSelected:
		m.agentName = ev.Name
	case protocol.EffortSelected:
		m.effort = ev.Level
		m.setNotice("effort: "+string(ev.Level)+" — "+ev.Level.Describe(), false)
	case protocol.FastSelected:
		m.fastEnabled = ev.Enabled
		m.setNotice(m.fastNotice(ev.Enabled), false)
	case protocol.EngineError:
		// Mid-turn failures belong in the transcript; idle-state errors
		// (no model selected, bad /provider, …) show in the notice line.
		if m.turnRunning {
			m.cells = append(m.cells, &errorCell{text: ev.Message})
		} else {
			m.setNotice(ev.Message, true)
		}
	}
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
			m.setNotice("select a provider first: /provider <anthropic|openai|xai|echo>", true)
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
	case "/auth":
		m.resetComposer()
		return m.handleAuth(fields[1:])
	case "/agent":
		if len(fields) < 2 {
			m.setNotice("agents: "+strings.Join(m.agents, " · ")+" (tab cycles)", false)
			m.resetComposer()
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
	case "/help":
		m.setNotice("commands: /provider [name [model]] · /model <model> · /effort <"+effortChoices()+"> · /fast [on|off] · /agent [name] · /auth · skills as /<name> · tab cycles agents", false)
		m.resetComposer()
		return m, nil
	default:
		// Unknown commands fall through to skills: /name args renders the
		// skill template and submits it as the user message.
		name := strings.TrimPrefix(fields[0], "/")
		for _, skill := range m.skills {
			if skill.Name != name {
				continue
			}
			if m.providerName == "" {
				m.setNotice("No model selected — use /provider <anthropic|openai|xai|echo> [model]", true)
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

func (m Model) submit(op protocol.UserInput, displayPrompt string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	ops := m.ops
	send := func() tea.Msg {
		ops <- op
		return nil
	}
	if m.services.History == nil {
		return m, send
	}
	done := m.services.History.Enqueue(displayPrompt)
	persist := func() tea.Msg {
		err := <-done
		return historyAddedMsg{err: err}
	}
	return m, tea.Batch(send, persist)
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

func (m *Model) setNotice(text string, isErr bool) {
	m.notice, m.noticeErr = text, isErr
}

func (m *Model) clearNotice() {
	m.notice, m.noticeErr = "", false
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

func (m *Model) refreshViewport() {
	if !m.ready {
		return
	}
	width := max(1, m.viewport.Width)
	if len(m.cells) == 0 {
		// Empty transcript: show the welcome dashboard from the top.
		m.viewport.SetContent(m.welcomeView(width))
		m.viewport.GotoTop()
		return
	}
	blocks := make([]string, 0, len(m.cells))
	for _, c := range m.cells {
		blocks = append(blocks, c.render(width, m.th))
	}
	m.viewport.SetContent(strings.Join(blocks, "\n\n"))
	m.viewport.GotoBottom()
}

func (m Model) View() string {
	if !m.ready {
		if warning := m.dangerView(0); warning != "" {
			return warning + "\nstarting…"
		}
		return "starting…"
	}

	compact := m.compact()
	l := computeLayout(m.width, m.height, m.composer.Height(), m.completionPopupHeight(), m.dangerouslySkipPermissions)

	// content is the top band (header + transcript); footer is the reserved
	// notice row, composer, keyhints, and danger banner. A modal floats over
	// the content band only, so the notice, composer, and danger banner stay
	// visible beneath it.
	content := m.headerView(m.width) + "\n" + m.transcriptView(compact, l.transcript)
	contentHeight := l.header + l.transcript

	footer := []string{m.noticeView(m.width)}
	if popup := m.completion.view(m.width, m.th); popup != "" {
		footer = append(footer, popup)
	}
	footer = append(footer, m.composerView(compact))
	footer = append(footer, m.hintsView(m.width))
	if warning := m.dangerView(m.width); warning != "" {
		footer = append(footer, warning)
	}

	if m.modal != nil {
		overlay := m.modal.view(max(8, ui.ModalWidth(m.width)), m.th)
		if warning := m.dangerView(m.width); warning != "" {
			overlay = lipgloss.JoinVertical(lipgloss.Center, warning, overlay)
		}
		content = ui.OverlayCenter(content, overlay, m.width, contentHeight)
	}
	return content + "\n" + strings.Join(footer, "\n")
}

// completionPopupHeight returns the reserved height of the completion popup
// for the current View, mirroring what reflow computed.
func (m Model) completionPopupHeight() int {
	if m.completion == nil || m.completion.rows <= 0 {
		return 0
	}
	borderRows := 0
	if min(m.width, completionMaxWidth) >= 4 {
		borderRows = 2
	}
	return m.completion.rows + borderRows
}

// compact reports whether the screen is below the breakpoints for bordered
// chrome; below it, panels degrade to plain viewport+composer.
func (m Model) compact() bool {
	return m.width < compactWidth || m.height < compactHeight
}
