package tui

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

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
			m.setNeedsModelNotice("select a provider first: /provider <anthropic|openai|xai|google|kimi|deepseek|echo>", true)
			return m, nil
		}
		if len(fields) < 2 {
			// Bare /model opens the centered picker (all authenticated providers).
			m.resetComposer()
			m.modal = newModelModal(m.providerName, m.modelName, m.ops, m.services.Settings)
			providers := authenticatedModelProviders(m.services.Auth, m.providerName)
			return m, loadModelsCmd(m.services.Catalog, providers, m.providerName)
		}
		provider, model := parseModelArg(fields[1], m.providerName)
		return m.sendSelect(protocol.SelectModel{Provider: provider, Model: model})
	case "/effort":
		if len(fields) < 2 {
			// Bare /effort opens the centered picker (variants + ladder).
			m.resetComposer()
			m.modal = newEffortModal(m.effort, m.ops, m.services.Settings)
			return m, loadEffortChoicesCmd(m.services.Catalog, m.providerName, m.modelName, m.effort, m.ops, m.services.Settings)
		}
		level, ok := protocol.ParseEffort(fields[1])
		if (!ok || level == protocol.EffortDefault) && m.services.Catalog != nil {
			// Config model variant id → effort (e.g. /effort high from variants.high).
			if effort, vok, err := m.services.Catalog.ResolveVariant(context.Background(), m.providerName, m.modelName, fields[1]); err == nil && vok {
				if parsed, pok := protocol.ParseEffort(effort); pok && parsed != protocol.EffortDefault {
					level, ok = parsed, true
				}
			}
		}
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
	case "/mode":
		if len(fields) < 2 {
			m.resetComposer()
			m.modal = newPermissionModeModal(m.permMode, m.ops, m.services.Settings)
			return m, nil
		}
		mode, ok := protocol.ParsePermissionMode(fields[1])
		if !ok {
			m.setNotice("unknown mode "+fields[1]+" — want "+permissionModeChoices(), true)
			return m, nil
		}
		m.resetComposer()
		m.clearNotice()
		ops := m.ops
		return m, func() tea.Msg {
			ops <- protocol.SetPermissionMode{Mode: mode}
			return nil
		}
	case "/sandbox":
		m.resetComposer()
		m.clearNotice()
		m.setNotice(m.sandboxStatusNotice(), false)
		return m, nil
	case "/auth":
		m.resetComposer()
		return m.handleAuth(fields[1:])
	case "/settings":
		m.resetComposer()
		m.clearNotice()
		m.modal = newSettingsModal(m.services, m.ops, m.th, m.workDir)
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
	case "/agents":
		return m.focusRightWindow(agentsWindowID)
	case "/activity":
		return m.focusRightWindow("activity")
	case "/files":
		return m.focusRightWindow(filesWindowID)
	case "/visualizer":
		return m.focusRightWindow(visualizerWindowID)
	case "/system":
		if !telemetryEnabled(m.windows) {
			m.resetComposer()
			m.setNotice("system telemetry off — /telemetry on to enable", true)
			return m, nil
		}
		return m.focusRightWindow(telemetryWindowID)
	case "/telemetry":
		return m.handleTelemetryCommand(fields[1:])
	case "/fast":
		return m.handleFastCommand(fields[1:])
	case "/think":
		return m.handleThinkCommand(fields[1:])
	case "/vim":
		return m.handleVimCommand(fields[1:])
	case "/nano":
		return m.handleNanoCommand(fields[1:])
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
	case "/undo":
		return m.handleUndoCommand(fields[1:])
	case "/rewind":
		return m.handleRewindCommand(fields[1:])
	case "/session":
		return m.handleSessionCommand(fields[1:])
	case "/rename":
		return m.handleRenameCommand(fields[1:], text)
	case "/export":
		return m.handleExportCommand(fields[1:])
	case "/copy":
		m.resetComposer()
		m.clearNotice()
		cmd := m.copyLastAssistantResponse()
		m.reflow()
		m.refreshViewport()
		return m, cmd
	case "/help":
		m.resetComposer()
		m.clearNotice()
		m.modal = newHelpModal(m.commands)
		m.reflow()
		return m, nil
	case "/keys":
		if len(fields) >= 2 && fields[1] == "bind" {
			m.resetComposer()
			m.clearNotice()
			m.modal = newKeybindEditor(m.keyMap, m.keyOverrides, m.services.Settings)
			m.reflow()
			return m, nil
		}
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
		m.modal = m.newKeysModal()
		m.reflow()
		return m, nil
	case "/legend":
		m.resetComposer()
		m.clearNotice()
		m.modal = newLegendModal(m.th)
		m.reflow()
		return m, nil
	case "/memory":
		return m.handleMemoryCommand(fields[1:])
	case "/issues":
		return m.handleIssuesCommand(fields[1:])
	case "/goal":
		return m.handleGoalCommand(fields[1:])
	case "/loop":
		return m.handleLoopCommand(text, fields[1:])
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
		m.clearModalStack()
		return m, tea.Quit
	case "/init":
		return m.handleInitCommand()
	case "/ftue":
		return m.handleFTUECommand()
	case "/mcp":
		return m.handleMCPCommand(fields[1:])
	case "/exit", "/quit":
		// Same graceful shutdown as the global quit keybinding (ctrl+c).
		return m, tea.Quit
	default:
		if isKeybindBackedSlash(fields[0]) {
			return m.handleKeybindSlashCommand(fields[0])
		}
		// Unknown commands fall through to skills: /name args renders the
		// skill template and submits it as the user message.
		name := strings.TrimPrefix(fields[0], "/")
		for _, skill := range m.skills {
			if skill.Name != name {
				continue
			}
			if m.providerName == "" {
				m.setNeedsModelNotice("No model selected — use /provider <anthropic|openai|xai|google|kimi|deepseek|echo> [model]", true)
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
		m.setNotice("cannot undo while a turn is running", true)
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

func (m Model) handleRewindCommand(args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	if m.turnRunning {
		m.setNotice("wait for the current turn to finish before rewinding", true)
		return m, nil
	}
	if m.sessionID == "" {
		m.setNotice("no session to rewind", true)
		return m, nil
	}
	if m.services.Sessions == nil {
		m.setNotice("session rewind is unavailable", true)
		return m, nil
	}
	if len(args) > 0 {
		arg := strings.ToLower(strings.TrimSpace(args[0]))
		if arg == "help" || arg == "?" {
			m.setNotice("usage: /rewind [turn] — fork a new session from a completed turn", false)
			return m, nil
		}
		turn, err := strconv.Atoi(arg)
		if err != nil || turn < 1 {
			m.setNotice("usage: /rewind [turn] — turn is a 1-based completed turn number", true)
			return m, nil
		}
		return m.forkRewindAtTurn(turn)
	}
	// Bare /rewind opens the turn picker.
	m.modal = newRewindModal(m.services.Sessions, m.sessionID, m.rewindForkCmd)
	m.reflow()
	return m, nil
}

// rewindForkCmd returns a tea.Cmd that forks at keepEvents and quits for resume.
func (m Model) rewindForkCmd(keepEvents int) tea.Cmd {
	return func() tea.Msg {
		return rewindForkMsg{keepEvents: keepEvents}
	}
}

type rewindForkMsg struct {
	keepEvents int
}

func (m Model) forkRewindAtTurn(turn int) (tea.Model, tea.Cmd) {
	raw, err := m.services.Sessions.ReplayJSONL(m.sessionID)
	if err != nil {
		m.setNotice("rewind failed: "+err.Error(), true)
		return m, nil
	}
	events, err := decodeSessionJSONL(raw)
	if err != nil {
		m.setNotice("rewind failed: "+err.Error(), true)
		return m, nil
	}
	points := protocol.RewindPoints(events)
	var keep int
	found := false
	for _, p := range points {
		if p.Turn == turn {
			keep = p.KeepEvents
			found = true
			break
		}
	}
	if !found {
		m.setNotice(fmt.Sprintf("no completed turn %d", turn), true)
		return m, nil
	}
	return m.applyRewindFork(keep)
}

func (m Model) applyRewindFork(keepEvents int) (tea.Model, tea.Cmd) {
	child, err := m.services.Sessions.ForkAt(m.sessionID, keepEvents)
	if err != nil {
		m.setNotice("rewind failed: "+err.Error(), true)
		return m, nil
	}
	id := strings.TrimSpace(child.ID)
	if id == "" || id == m.sessionID {
		m.setNotice("rewind failed: empty child session", true)
		return m, nil
	}
	m.pendingResume = id
	m.clearModalStack()
	m.setNotice("rewound → "+shortSessionID(id)+" (switching…)", false)
	return m, tea.Quit
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

// handleFTUECommand opens the setup wizard. Opening never writes settings;
// Finish focuses the composer; esc cancels without side effects.
func (m Model) handleFTUECommand() (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	m.modal = newFTUEModal(m.services, m.providerName, m.modelName, m.th)
	m.reflow()
	return m, nil
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
	// Clear only an init confirm still on top. When /ftue parked the wizard and
	// afterModalClosed already promoted it, do not wipe that modal.
	if _, ok := m.modal.(*initConfirmModal); ok {
		m.modal = nil
	}
	promote := m.afterModalClosed()
	if msg.canceled {
		m.setNotice("init canceled", false)
		m.reflow()
		return m, promote
	}
	if msg.err != "" {
		m.setNotice("init failed: "+msg.err, true)
		m.reflow()
		return m, promote
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
	return m, promote
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

func (m Model) handleRenameCommand(args []string, raw string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	id := strings.TrimSpace(m.sessionID)
	if id == "" {
		m.setNotice("no session to rename", true)
		return m, nil
	}
	if m.services.Sessions == nil {
		m.setNotice("session rename unavailable", true)
		return m, nil
	}
	// /rename with no args opens the editor; with args applies immediately.
	if len(args) == 0 {
		cmd := m.openRenameModal(id)
		return m, cmd
	}
	title := strings.TrimSpace(raw)
	if rest, ok := strings.CutPrefix(title, "/rename"); ok {
		title = strings.TrimSpace(rest)
	}
	got, err := m.services.Sessions.Rename(id, title)
	if err != nil {
		m.setNotice("rename: "+err.Error(), true)
		return m, nil
	}
	rid := strings.TrimSpace(got.ID)
	if rid == "" {
		rid = id
	}
	return m, m.applySessionRename(rid, strings.TrimSpace(got.Title))
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

func (m Model) handleGoalCommand(args []string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	if m.services.Goals == nil {
		m.setNotice("project goals are unavailable", true)
		return m, nil
	}
	usage := `usage: /goal set "<desc>" --criterion "cmd: …" [--max-iter N] [--budget-usd X] [--tools a,b]
/goal run|status|pause|resume|abort|log|list [id]`
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "set":
		desc, criteria, opts, err := parseGoalSetArgs(args[1:])
		if err != nil {
			m.setNotice("goal: "+err.Error(), true)
			return m, nil
		}
		g, err := m.services.Goals.Set(desc, criteria, opts)
		if err != nil {
			m.setNotice("goal: "+err.Error(), true)
			return m, nil
		}
		m.setNotice(fmt.Sprintf("goal: set %s (pending) — /goal run %s", g.ID, g.ID), false)
		return m, nil
	case "list", "ls":
		list, err := m.services.Goals.List()
		if err != nil {
			m.setNotice("goal: "+err.Error(), true)
			return m, nil
		}
		if len(list) == 0 {
			m.setNotice("goal: no goals", false)
			return m, nil
		}
		var b strings.Builder
		b.WriteString("goal: ")
		for i, g := range list {
			if i > 0 {
				b.WriteString(" | ")
			}
			fmt.Fprintf(&b, "%s [%s] %s", g.ID, g.Status, truncateRunes(g.Description, 40))
		}
		m.setNotice(b.String(), false)
		return m, nil
	case "status":
		id, err := m.resolveGoalID(args[1:])
		if err != nil {
			m.setNotice("goal: "+err.Error(), true)
			return m, nil
		}
		g, ok, err := m.services.Goals.Get(id)
		if err != nil {
			m.setNotice("goal: "+err.Error(), true)
			return m, nil
		}
		if !ok {
			m.setNotice("goal: not found", true)
			return m, nil
		}
		m.setNotice("goal: "+formatGoalStatus(g), false)
		return m, nil
	case "run":
		id, err := m.resolveGoalID(args[1:])
		if err != nil {
			m.setNotice("goal: "+err.Error(), true)
			return m, nil
		}
		m.setNotice("goal: running "+id+"...", false)
		goals := m.services.Goals
		return m, func() tea.Msg {
			g, err := goals.Run(context.Background(), id)
			return goalFinishedMsg{goal: g, err: err, op: "run"}
		}
	case "pause":
		id, err := m.resolveGoalID(args[1:])
		if err != nil {
			m.setNotice("goal: "+err.Error(), true)
			return m, nil
		}
		g, err := m.services.Goals.Pause(id)
		if err != nil {
			m.setNotice("goal: "+err.Error(), true)
			return m, nil
		}
		m.setNotice(fmt.Sprintf("goal: paused %s", g.ID), false)
		return m, nil
	case "resume":
		id, err := m.resolveGoalID(args[1:])
		if err != nil {
			m.setNotice("goal: "+err.Error(), true)
			return m, nil
		}
		g, err := m.services.Goals.Resume(id)
		if err != nil {
			m.setNotice("goal: "+err.Error(), true)
			return m, nil
		}
		m.setNotice(fmt.Sprintf("goal: resumed %s (active) — /goal run %s", g.ID, g.ID), false)
		return m, nil
	case "abort":
		id, err := m.resolveGoalID(args[1:])
		if err != nil {
			m.setNotice("goal: "+err.Error(), true)
			return m, nil
		}
		g, err := m.services.Goals.Abort(id)
		if err != nil {
			m.setNotice("goal: "+err.Error(), true)
			return m, nil
		}
		m.setNotice(fmt.Sprintf("goal: aborted %s", g.ID), false)
		return m, nil
	case "log":
		id, err := m.resolveGoalID(args[1:])
		if err != nil {
			m.setNotice("goal: "+err.Error(), true)
			return m, nil
		}
		iterN := 0
		if len(args) >= 3 {
			if n, e := strconv.Atoi(args[2]); e == nil && n > 0 {
				iterN = n
			}
		}
		// Also support --iter N anywhere after id.
		for i := 1; i < len(args)-1; i++ {
			if args[i] == "--iter" {
				if n, e := strconv.Atoi(args[i+1]); e == nil && n > 0 {
					iterN = n
				}
			}
		}
		logs, err := m.services.Goals.Log(id, iterN)
		if err != nil {
			m.setNotice("goal: "+err.Error(), true)
			return m, nil
		}
		if len(logs) == 0 {
			m.setNotice("goal: no iterations logged", false)
			return m, nil
		}
		var b strings.Builder
		b.WriteString("goal log: ")
		for i, line := range logs {
			if i > 0 {
				b.WriteString(" || ")
			}
			b.WriteString(line.Summary)
		}
		m.setNotice(b.String(), false)
		return m, nil
	default:
		m.setNotice(usage, true)
		return m, nil
	}
}

func (m Model) resolveGoalID(args []string) (string, error) {
	if len(args) >= 1 && !strings.HasPrefix(args[0], "-") {
		return args[0], nil
	}
	list, err := m.services.Goals.List()
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", fmt.Errorf("no goals; use /goal set first")
	}
	return list[0].ID, nil
}

func formatGoalStatus(g host.Goal) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s] iter=%d/%d cost=%.4f/%0.2f",
		g.ID, g.Status, g.LastIteration, g.MaxIterations, g.CostUSD, g.MaxCostUSD)
	if g.FailReason != "" {
		fmt.Fprintf(&b, " reason=%s", g.FailReason)
	}
	b.WriteString(" | ")
	b.WriteString(truncateRunes(g.Description, 48))
	if len(g.Criteria) > 0 {
		b.WriteString(" |")
		for _, c := range g.Criteria {
			mark := "FAIL"
			if c.Satisfied {
				mark = "OK"
			}
			fmt.Fprintf(&b, " %s %s", mark, truncateRunes(c.Description, 24))
		}
	}
	return b.String()
}

func parseGoalSetArgs(args []string) (desc string, criteria []string, opts host.GoalSetOptions, err error) {
	if len(args) == 0 {
		return "", nil, opts, fmt.Errorf("description required")
	}
	// Description is either a single quoted-joined token sequence until first --flag,
	// or the first arg if it doesn't start with --.
	var descParts []string
	i := 0
	for i < len(args) {
		a := args[i]
		if strings.HasPrefix(a, "--") {
			break
		}
		descParts = append(descParts, a)
		i++
	}
	desc = strings.TrimSpace(strings.Join(descParts, " "))
	// Strip surrounding quotes if the whole desc was one shell-ish token.
	if len(desc) >= 2 {
		if (desc[0] == '"' && desc[len(desc)-1] == '"') || (desc[0] == '\'' && desc[len(desc)-1] == '\'') {
			desc = desc[1 : len(desc)-1]
		}
	}
	if desc == "" {
		return "", nil, opts, fmt.Errorf("description required")
	}
	for i < len(args) {
		a := args[i]
		switch a {
		case "--criterion", "-c":
			// Consume tokens until the next --flag so `cmd: pytest -q` works
			// after strings.Fields splits on spaces.
			i++
			var parts []string
			for i < len(args) && !strings.HasPrefix(args[i], "--") {
				parts = append(parts, args[i])
				i++
			}
			if len(parts) == 0 {
				return "", nil, opts, fmt.Errorf("%s needs a value", a)
			}
			criteria = append(criteria, strings.Join(parts, " "))
		case "--max-iter", "--max-iterations":
			if i+1 >= len(args) {
				return "", nil, opts, fmt.Errorf("%s needs a value", a)
			}
			n, e := strconv.Atoi(args[i+1])
			if e != nil || n < 1 {
				return "", nil, opts, fmt.Errorf("invalid %s", a)
			}
			opts.MaxIterations = n
			i += 2
		case "--budget-usd", "--max-cost":
			if i+1 >= len(args) {
				return "", nil, opts, fmt.Errorf("%s needs a value", a)
			}
			var f float64
			if _, e := fmt.Sscanf(args[i+1], "%f", &f); e != nil || f < 0 {
				return "", nil, opts, fmt.Errorf("invalid %s", a)
			}
			opts.MaxCostUSD = f
			i += 2
		case "--tools":
			if i+1 >= len(args) {
				return "", nil, opts, fmt.Errorf("%s needs a value", a)
			}
			for _, t := range strings.Split(args[i+1], ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					opts.AllowedTools = append(opts.AllowedTools, t)
				}
			}
			i += 2
		case "--max-wall", "--max-wall-s":
			if i+1 >= len(args) {
				return "", nil, opts, fmt.Errorf("%s needs a value", a)
			}
			n, e := strconv.Atoi(args[i+1])
			if e != nil || n < 1 {
				return "", nil, opts, fmt.Errorf("invalid %s", a)
			}
			opts.MaxWallClockS = n
			i += 2
		default:
			return "", nil, opts, fmt.Errorf("unknown flag %q", a)
		}
	}
	if len(criteria) == 0 {
		return "", nil, opts, fmt.Errorf("at least one --criterion is required")
	}
	return desc, criteria, opts, nil
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
		m.setNotice("usage: /md-read <path|@path>", true)
		return m, nil
	}
	resolved, err := resolveCommandPathArg(pathArg)
	if err != nil {
		m.setNotice(err.Error(), true)
		return m, nil
	}
	if resolved == "" {
		m.setNotice("usage: /md-read <path|@path>", true)
		return m, nil
	}
	pathArg = resolved
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

	mode := m.mdReadMode
	if mode == "" {
		mode = PresentationEmbedded
	}
	if mode == PresentationModal {
		modal := newMarkdownModal(pathArg, string(data))
		modal.setHostSize(m.width, m.height)
		m.modal = modal
		m.clearNotice()
		m.setNotice("markdown (modal) - esc closes", false)
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
		m.applyAppearance()
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

// handleTelemetryCommand opts the local system metrics pane in or out.
// Bare /telemetry toggles; on/off set explicitly; status reports without change.
func (m Model) handleTelemetryCommand(args []string) (tea.Model, tea.Cmd) {
	cur := telemetryEnabled(m.windows)
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "status":
			m.resetComposer()
			if cur {
				m.setNotice("system telemetry on", false)
			} else {
				m.setNotice("system telemetry off", false)
			}
			return m, nil
		case "on", "true", "1", "yes":
			// handled below
		case "off", "false", "0", "no":
			// handled below
		default:
			m.setNotice("usage: /telemetry [on|off|status]", true)
			return m, nil
		}
	}
	enabled := !cur
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "on", "true", "1", "yes":
			enabled = true
		case "off", "false", "0", "no":
			enabled = false
		}
	}
	m.resetComposer()
	m.clearNotice()
	var cmd tea.Cmd
	m.windows, cmd = setTelemetryEnabled(m.windows, enabled)
	m.reflow()
	if enabled {
		m.setNotice("system telemetry on", false)
	} else {
		m.setNotice("system telemetry off", false)
	}
	return m, cmd
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

// saveDefaultsCmd persists provider/model/agent/effort/mode defaults through
// the host settings service, reporting the outcome as a defaultsSavedMsg.
func (m Model) saveDefaultsCmd(provider, model, agent, effort, mode, text string) tea.Cmd {
	return saveDefaultsThroughCmd(m.services.Settings, provider, model, agent, effort, mode, text)
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

func permissionModeChoices() string {
	names := make([]string, 0, len(protocol.PermissionModes()))
	for _, mode := range protocol.PermissionModes() {
		names = append(names, string(mode))
	}
	return strings.Join(names, "|")
}

// sandboxStatusNotice formats the effective OS sandbox policy for /sandbox.
// Two-dial model: sandbox = what is possible; permissionMode = when asked.
func (m Model) sandboxStatusNotice() string {
	mode := strings.TrimSpace(m.sandboxMode)
	if mode == "" {
		mode = "workspace-write"
	}
	ic := m.themeIcons()
	dot := " " + ic.Dot + " "
	var b strings.Builder
	fmt.Fprintf(&b, "sandbox: %s", mode)
	switch {
	case mode == "off":
		b.WriteString(" (OS isolation disabled)")
	case m.sandboxAvailable && m.sandboxBackend != "":
		fmt.Fprintf(&b, "%sbackend %s", dot, m.sandboxBackend)
	case m.sandboxAvailable:
		b.WriteString(dot + "backend available")
	default:
		b.WriteString(dot + "backend unavailable (bash unsandboxed)")
	}
	fmt.Fprintf(&b, "%spermissionMode: %s", dot, m.permMode.Normalize())
	b.WriteString(" " + ic.DetailSeparator + " sandbox=what is possible, permissionMode=when asked")
	return b.String()
}
