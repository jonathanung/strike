package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// keybindSlashPrimary maps each keybind catalog ID to the primary slash command
// that performs the same action (leading /). Empty means intentional exception —
// the ID must appear in keybindNoSlashReason.
//
// Prefer reusing existing multi-purpose commands (/keys, /exit, /layout, …)
// over inventing parallel names. New action mirrors live here and in
// keybindBackedCommandSpecs.
var keybindSlashPrimary = map[string]string{
	"nav.focus-left":     "/focus-left",
	"nav.focus-right":    "/focus-right",
	"nav.window-next":    "/window-next",
	"nav.window-prev":    "/window-prev",
	"nav.scroll-up":      "/scroll-up",
	"nav.scroll-down":    "/scroll-down",
	"nav.jump-bottom":    "/jump-bottom",
	"nav.toggle-orient":  "/layout",
	"nav.tool-prev":      "/tool-prev",
	"nav.tool-next":      "/tool-next",
	"nav.tool-expand":    "/tool-expand",
	"nav.tool-copy":      "/tool-copy",
	"nav.tool-review":    "/tool-review",
	"nav.tool-apply":     "/tool-apply",
	"nav.session-child":  "/subagent",
	"nav.session-parent": "/parent",
	"nav.session-next":   "/subagent-next",
	"nav.session-prev":   "/subagent-prev",

	"global.palette":       "/palette",
	"global.keyhelp":       "/keys",
	"global.interrupt":     "/interrupt",
	"global.quit":          "/exit",
	"global.save-defaults": "/save-defaults",
	"global.copy-last":     "/copy",

	"editor.leave": "/leave-editor",

	"composer.external-editor": "/edit-prompt",
	"composer.agent":           "/agent-next",
	"composer.permission-mode": "/mode-next",

	"agents.open":      "/root-open",
	"agents.spawn":     "/root-new",
	"agents.interrupt": "/root-interrupt",
	"agents.hide":      "/root-hide",
	"agents.filter":    "/root-filter",
	"agents.rename":    "/rename",
}

// keybindSlashAliases lists extra slash names that perform the same action as
// the primary (already shipped as separate builtins).
var keybindSlashAliases = map[string][]string{
	"global.quit":       {"/quit"},
	"nav.toggle-orient": {"/split"},
}

// keybindNoSlashReason documents catalog IDs that are intentionally not
// slash-invocable (composer editing, completion, modal list conventions, etc.).
var keybindNoSlashReason = map[string]string{
	"nav.leader": "prefix chord only; use /subagent and siblings for the actions",

	"composer.send":            "enter sends the composer; not a discrete command",
	"composer.newline":         "input editing",
	"composer.history-prev":    "composer history browse",
	"composer.history-next":    "composer history browse",
	"composer.kill-word":       "composer readline editing",
	"composer.word-back":       "composer readline editing",
	"composer.word-fwd":        "composer readline editing",
	"composer.kill-line-start": "composer readline editing",
	"composer.kill-line-end":   "composer readline editing",
	"composer.yank":            "composer readline editing",

	"completion.prev":    "completion UI only",
	"completion.next":    "completion UI only",
	"completion.accept":  "completion UI only",
	"completion.dismiss": "completion UI only",

	"agents.move": "list navigation inside the agents pane",

	"lists.move":    "modal/list convention",
	"lists.move-jk": "modal/list convention",
	"lists.select":  "modal/list convention",
	"lists.filter":  "modal/list convention",
	"lists.logout":  "modal/list convention",
	"lists.close":   "modal/list convention",
	"lists.default": "modal/list convention",

	"perm.choice":  "permission modal convention",
	"perm.once":    "permission modal convention",
	"perm.session": "permission modal convention",
	"perm.project": "permission modal convention",
	"perm.reject":  "permission modal convention",
}

// keybindBackedCommandSpecs are slash commands whose sole purpose is to mirror
// a keybind action. Multi-purpose builtins (/keys, /exit, /layout, …) stay in
// builtinCommandSpecs and are only referenced from keybindSlashPrimary.
var keybindBackedCommandSpecs = []commandSpec{
	{ID: commandFocusLeft, Name: "/focus-left", Description: "focus left pane", Source: commandSourceBuiltin},
	{ID: commandFocusRight, Name: "/focus-right", Description: "focus right pane", Source: commandSourceBuiltin},
	{ID: commandWindowNext, Name: "/window-next", Description: "cycle to next right-pane window", Source: commandSourceBuiltin},
	{ID: commandWindowPrev, Name: "/window-prev", Description: "cycle to previous right-pane window", Source: commandSourceBuiltin},
	{ID: commandScrollUp, Name: "/scroll-up", Description: "scroll transcript up", Source: commandSourceBuiltin},
	{ID: commandScrollDown, Name: "/scroll-down", Description: "scroll transcript down", Source: commandSourceBuiltin},
	{ID: commandJumpBottom, Name: "/jump-bottom", Description: "jump transcript to latest output", Source: commandSourceBuiltin},
	{ID: commandPalette, Name: "/palette", Description: "open command palette", Source: commandSourceBuiltin},
	{ID: commandInterrupt, Name: "/interrupt", Description: "interrupt the running turn", Source: commandSourceBuiltin},
	{ID: commandSaveDefaults, Name: "/save-defaults", Description: "save provider/model/agent/effort/mode defaults", Source: commandSourceBuiltin},
	{ID: commandLeaveEditor, Name: "/leave-editor", Description: "leave embedded editor pane", Source: commandSourceBuiltin},
	{ID: commandEditPrompt, Name: "/edit-prompt", Description: "edit prompt in external editor", Source: commandSourceBuiltin},
	{ID: commandAgentNext, Name: "/agent-next", Description: "cycle agent persona", Source: commandSourceBuiltin},
	{ID: commandModeNext, Name: "/mode-next", Description: "cycle permission mode", Source: commandSourceBuiltin},
	{ID: commandToolPrev, Name: "/tool-prev", Description: "select previous tool cell", Source: commandSourceBuiltin},
	{ID: commandToolNext, Name: "/tool-next", Description: "select next tool cell", Source: commandSourceBuiltin},
	{ID: commandToolExpand, Name: "/tool-expand", Description: "expand tool cell or open file:line", Source: commandSourceBuiltin},
	{ID: commandToolCopy, Name: "/tool-copy", Description: "copy selected transcript cell", Source: commandSourceBuiltin},
	{ID: commandToolReview, Name: "/tool-review", Description: "review selected edit in editor", Source: commandSourceBuiltin},
	{ID: commandToolApply, Name: "/tool-apply", Description: "apply selected patch to worktree", Source: commandSourceBuiltin},
	{ID: commandSubagent, Name: "/subagent", Description: "enter first subagent transcript", Source: commandSourceBuiltin},
	{ID: commandParent, Name: "/parent", Description: "return to parent session transcript", Source: commandSourceBuiltin},
	{ID: commandSubagentNext, Name: "/subagent-next", Description: "next sibling subagent", Source: commandSourceBuiltin},
	{ID: commandSubagentPrev, Name: "/subagent-prev", Description: "previous sibling subagent", Source: commandSourceBuiltin},
	{ID: commandRootNew, Name: "/root-new", Description: "spawn a new concurrent root session", Source: commandSourceBuiltin},
	{ID: commandRootOpen, Name: "/root-open", Description: "activate selected agents-pane root", Source: commandSourceBuiltin},
	{ID: commandRootInterrupt, Name: "/root-interrupt", Description: "interrupt selected agents-pane root", Source: commandSourceBuiltin},
	{ID: commandRootHide, Name: "/root-hide", Description: "hide selected root from agents pane", Source: commandSourceBuiltin},
	{ID: commandRootFilter, Name: "/root-filter", Description: "cycle agents pane view filter", Source: commandSourceBuiltin},
}

// keybindSlashCommandNames is the set of slash names (with /) backed by
// keybind actions, including aliases of multi-purpose builtins.
func keybindSlashCommandNames() map[string]struct{} {
	out := make(map[string]struct{}, len(keybindSlashPrimary)+8)
	for _, slash := range keybindSlashPrimary {
		if slash != "" {
			out[slash] = struct{}{}
		}
	}
	for _, aliases := range keybindSlashAliases {
		for _, a := range aliases {
			out[a] = struct{}{}
		}
	}
	return out
}

// isKeybindBackedSlash reports whether name (with leading /) is handled by
// handleKeybindSlashCommand.
func isKeybindBackedSlash(name string) bool {
	switch name {
	case "/focus-left", "/focus-right",
		"/window-next", "/window-prev",
		"/scroll-up", "/scroll-down", "/jump-bottom",
		"/palette", "/interrupt", "/save-defaults",
		"/leave-editor", "/edit-prompt",
		"/agent-next", "/mode-next",
		"/tool-prev", "/tool-next", "/tool-expand", "/tool-copy", "/tool-review", "/tool-apply",
		"/subagent", "/parent", "/subagent-next", "/subagent-prev",
		"/root-new", "/root-open", "/root-interrupt", "/root-hide", "/root-filter":
		return true
	default:
		return false
	}
}

// handleKeybindSlashCommand runs the action mirrored by a keybind-backed slash.
// Multi-purpose commands (/keys, /exit, /layout, …) keep their own cases.
func (m Model) handleKeybindSlashCommand(name string) (tea.Model, tea.Cmd) {
	m.resetComposer()
	m.clearNotice()
	m.completion = nil

	switch name {
	case "/focus-left":
		cmd := m.focusPane(focusLeft)
		m.reflow()
		return m, cmd
	case "/focus-right":
		cmd := m.focusPane(focusRight)
		m.reflow()
		return m, cmd
	case "/window-next":
		m.windows = m.windows.cycleBy(1)
		m.windows = refreshProjectDataWindows(m.windows)
		m.reflow()
		return m, filesPollCmd(m.windows)
	case "/window-prev":
		m.windows = m.windows.cycleBy(-1)
		m.windows = refreshProjectDataWindows(m.windows)
		m.reflow()
		return m, filesPollCmd(m.windows)
	case "/scroll-up":
		m.viewport.HalfViewUp()
		return m, nil
	case "/scroll-down":
		m.viewport.HalfViewDown()
		return m, nil
	case "/jump-bottom":
		m.viewport.GotoBottom()
		return m, nil
	case "/palette":
		m.modal = newPaletteModal(m.commands, m.agents, m.currentPaletteAvailability())
		m.reflow()
		return m, nil
	case "/interrupt":
		if handled, cmd := m.handleInterruptKey(); handled {
			return m, cmd
		}
		m.setNotice("nothing to interrupt", false)
		return m, nil
	case "/save-defaults":
		if m.providerName == "" {
			m.setNeedsModelNotice("nothing to save — select a provider first", true)
			return m, nil
		}
		return m, m.saveDefaultsCmd(m.providerName, m.modelName, m.agentName, string(m.effort), string(m.permMode.Normalize()), dotJoin(m.th, m.providerName+"/"+m.modelName, m.agentName))
	case "/leave-editor":
		return m.leaveEmbeddedEditor()
	case "/edit-prompt":
		return m.openComposerExternalEditor()
	case "/agent-next":
		return m.cycleAgentPersona()
	case "/mode-next":
		return m.cyclePermissionMode()
	case "/tool-prev":
		m.moveToolSelection(-1)
		m.reflow()
		m.refreshViewport()
		return m, nil
	case "/tool-next":
		m.moveToolSelection(1)
		m.reflow()
		m.refreshViewport()
		return m, nil
	case "/tool-expand":
		if m.toggleSelectedTool() {
			m.reflow()
			m.refreshViewport()
			return m, nil
		}
		if ref, ok := m.fileRefForEnter(); ok {
			return m.openFileRef(ref)
		}
		m.setNotice("no tool cell to expand", false)
		return m, nil
	case "/tool-copy":
		handled, cmd := m.copySelectedCell()
		if !handled {
			m.setNotice("nothing to copy", false)
			return m, nil
		}
		m.reflow()
		m.refreshViewport()
		return m, cmd
	case "/tool-review":
		handled, cmd := m.reviewSelectedTool()
		if !handled {
			m.setNotice("no edit to review", false)
			return m, nil
		}
		m.reflow()
		m.refreshViewport()
		return m, cmd
	case "/tool-apply":
		handled, cmd := m.applySelectedTool()
		if !handled {
			m.setNotice("no patch to apply", false)
			return m, nil
		}
		m.reflow()
		m.refreshViewport()
		return m, cmd
	case "/subagent":
		cmd := m.navChildFirst()
		m.reflow()
		m.refreshViewport()
		return m, cmd
	case "/parent":
		cmd := m.navParent()
		m.reflow()
		m.refreshViewport()
		return m, cmd
	case "/subagent-next":
		cmd := m.navSibling(1)
		m.reflow()
		m.refreshViewport()
		return m, cmd
	case "/subagent-prev":
		cmd := m.navSibling(-1)
		m.reflow()
		m.refreshViewport()
		return m, cmd
	case "/root-new":
		cmd := m.spawnRoot()
		m.reflow()
		return m, cmd
	case "/root-open", "/root-interrupt", "/root-hide", "/root-filter":
		return m.dispatchAgentsSlash(name)
	default:
		m.setNotice("unknown command "+name+" — try /help", true)
		return m, nil
	}
}

func (m Model) cycleAgentPersona() (tea.Model, tea.Cmd) {
	if m.turnRunning {
		m.setNotice("cannot cycle agent while a turn is running", true)
		return m, nil
	}
	if len(m.agents) <= 1 {
		m.setNotice("no other agents to cycle", false)
		return m, nil
	}
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

func (m Model) cyclePermissionMode() (tea.Model, tea.Cmd) {
	// Allowed mid-turn: engine applies posture to subsequent tool asks.
	next := m.permMode.Next()
	ops := m.ops
	return m, func() tea.Msg {
		ops <- protocol.SetPermissionMode{Mode: next}
		return nil
	}
}

// dispatchAgentsSlash routes agents-pane keybind mirrors by simulating the
// pane key on the agents window (after activating it).
func (m Model) dispatchAgentsSlash(name string) (tea.Model, tea.Cmd) {
	reg, ok := m.windows.activate(agentsWindowID)
	if !ok {
		m.setNotice("agents pane unavailable", true)
		return m, nil
	}
	m.windows = reg
	_ = m.focusPane(focusRight)
	var msg tea.KeyMsg
	switch name {
	case "/root-open":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "/root-interrupt":
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}
	case "/root-hide":
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}
	case "/root-filter":
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}
	default:
		return m, nil
	}
	var cmd tea.Cmd
	m.windows, cmd = m.windows.update(msg)
	m.reflow()
	return m, cmd
}

// slashForKeybindID returns the primary slash for a catalog id, or "".
func slashForKeybindID(id string) string {
	return keybindSlashPrimary[id]
}

// keybindSlashReservedNames returns skill-reserved bare names derived from
// keybind-backed slash commands (and their aliases).
func keybindSlashReservedNames() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(slash string) {
		name := strings.TrimPrefix(slash, "/")
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	for _, slash := range keybindSlashPrimary {
		add(slash)
	}
	for _, aliases := range keybindSlashAliases {
		for _, a := range aliases {
			add(a)
		}
	}
	for _, spec := range keybindBackedCommandSpecs {
		add(spec.Name)
	}
	return out
}
