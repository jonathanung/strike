// Package tui is the BubbleTea frontend. It depends only on
// internal/protocol (plus theme): engine events arrive on a channel via a
// listening tea.Cmd, ops go back on the ops channel — the Go analogue of
// codex's multi-source select loop.
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/history"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

const (
	composerMinHeight = 2
	composerMaxHeight = 8
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

type Options struct {
	History *history.Store
}

type Model struct {
	ops    chan<- protocol.Op
	events <-chan protocol.Event
	// authStore is host-side credential state, managed via /auth. It is
	// deliberately not part of the engine protocol: credentials are a
	// frontend/host concern, not agent-loop state.
	authStore *auth.Store

	th       theme.Theme
	cells    []cell
	toolByID map[string]*toolCell
	modal    modal

	viewport     viewport.Model
	composer     textarea.Model
	completion   *completionState
	commands     []commandSpec
	spin         spinner.Model
	history      *history.Store
	entries      []string
	historyPos   int
	historyDraft string

	providerName string
	modelName    string
	agentName    string
	agents       []string // cycled with tab
	skills       []config.Skill
	notice       string
	noticeErr    bool
	turnRunning  bool
	width        int
	height       int
	ready        bool
}

func New(ops chan<- protocol.Op, events <-chan protocol.Event, authStore *auth.Store, agents []string, skills []config.Skill, options ...Options) Model {
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
		authStore:  authStore,
		agents:     agents,
		skills:     skills,
		commands:   commandCatalog(skills),
		th:         theme.Default(),
		toolByID:   map[string]*toolCell{},
		composer:   ta,
		spin:       sp,
		historyPos: -1,
	}
	for _, option := range options {
		if option.History != nil {
			m.history = option.History
			m.entries = option.History.Entries()
		}
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
		} else if m.history != nil {
			m.entries = m.history.Entries()
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
			prov, model, agent := m.providerName, m.modelName, m.agentName
			return m, func() tea.Msg {
				err := config.SetGlobalDefaults(prov, model, agent)
				return defaultsSavedMsg{text: prov + "/" + model + " · " + agent, err: err}
			}
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
	m.composer.SetWidth(max(1, m.width-2))
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
		m.viewport.Width = max(1, m.width)
		m.viewport.Height = max(0, m.height-2-composerRows-popupHeight)
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
			m.modal = newProviderModal(m.authStore, m.providerName, m.ops, m.th)
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
			m.modal = newModelModal(m.providerName, m.modelName, m.ops)
			return m, loadModelsCmd(m.providerName)
		}
		return m.sendSelect(protocol.SelectModel{Provider: m.providerName, Model: fields[1]})
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
	case "/help":
		m.setNotice("commands: /provider [name [model]] · /model <model> · /agent [name] · /auth · skills as /<name> · tab cycles agents", false)
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
	if m.history == nil {
		return m, send
	}
	done := m.history.Enqueue(displayPrompt)
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
	blocks := make([]string, 0, len(m.cells))
	for _, c := range m.cells {
		blocks = append(blocks, c.render(max(1, m.viewport.Width), m.th))
	}
	m.viewport.SetContent(strings.Join(blocks, "\n\n"))
	m.viewport.GotoBottom()
}

func (m Model) View() string {
	if !m.ready {
		return "starting…"
	}
	sections := []string{m.viewport.View(), m.statusLine(), m.noticeLine()}
	if popup := m.completion.view(m.width, m.th); popup != "" {
		sections = append(sections, popup)
	}
	sections = append(sections, m.composer.View())
	base := strings.Join(sections, "\n")
	if m.modal != nil {
		return overlayCenter(base, m.modal.view(max(8, modalWidth(m.width)), m.th), m.width, m.height)
	}
	return base
}

func (m Model) statusLine() string {
	muted := lipgloss.NewStyle().Foreground(m.th.TextMuted)
	agent := ""
	if m.agentName != "" {
		agent = " · " + m.agentName
	}
	var left string
	if m.providerName == "" {
		left = muted.Render("strike" + agent + " · no model — /provider <name> to select")
	} else {
		left = muted.Render("strike" + agent + " · " + m.providerName + "/" + m.modelName)
	}
	if m.turnRunning {
		left += " " + m.spin.View() + muted.Render(" working (esc to interrupt)")
	}
	return lipgloss.NewStyle().MaxWidth(max(1, m.width)).Render(left)
}

// noticeLine is a reserved row above the composer for transient feedback:
// errors in red (no model selected, failed /provider), confirmations muted.
func (m Model) noticeLine() string {
	if m.notice == "" {
		return ""
	}
	style := lipgloss.NewStyle().Foreground(m.th.TextMuted)
	if m.noticeErr {
		style = lipgloss.NewStyle().Foreground(m.th.Error)
	}
	return style.MaxWidth(max(1, m.width)).Render(m.notice)
}
