package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// pluginPaneWindow is a right-pane adapter for one pane/1 contribution.
// Static panes resolve view bindings against host data feeds. Process panes
// supervise a JSONL subprocess. The private window interface is never exposed
// to plugins (docs/plugin-panes.md).
type pluginPaneWindow struct {
	info   host.PaneInfo
	def    paneDef
	width  int
	height int

	// Static / shared view state.
	data      paneDataStore
	viewRoot  paneViewNode
	hasView   bool
	titleText string
	status    string
	errState  string
	focused   bool

	// Process runtime (shared pointer across COW copies).
	rt *pluginPaneRuntime

	// Rate-limit host data pushes (100ms coalesce per feed).
	lastPush map[string]time.Time

	// preferred outer height hint for stack flex.
	prefHeight int
}

func newPluginPaneWindow(info host.PaneInfo) pluginPaneWindow {
	w := pluginPaneWindow{
		info:      info,
		titleText: info.Title,
		data:      paneDataStore{},
		lastPush:  map[string]time.Time{},
	}
	if len(info.DefinitionJSON) > 0 {
		if def, err := parsePaneDef(info.DefinitionJSON); err == nil {
			w.def = def
			w.titleText = clampPaneTitle(def.Title)
			w.prefHeight = def.Sizing.PreferredHeight
			if def.Mode == paneModeStatic && len(def.View) > 0 {
				if root, err := parsePaneView(def.View); err == nil {
					w.viewRoot = root
					w.hasView = true
				} else {
					w.errState = "invalid static view: " + err.Error()
				}
			}
		} else {
			w.errState = "invalid definition: " + err.Error()
		}
	}
	if info.LoadError != "" {
		w.errState = info.LoadError
	}
	if info.Mode == host.PaneModeProcess || w.def.Mode == paneModeProcess {
		if !info.Trusted || info.LoadError != "" {
			if w.errState == "" {
				w.errState = info.LoadError
			}
			if w.errState == "" {
				w.errState = "process pane blocked until plugin trust is granted"
			}
		} else {
			w.rt = newPluginPaneRuntime(info.ID, info.PluginID, info.PluginVersion, info.PluginRoot, w.def)
			// Export plugin root into process env.
			if w.rt.def.Env == nil {
				w.rt.def.Env = map[string]string{}
			}
			w.rt.def.Env["STRIKE_PLUGIN_ROOT"] = info.PluginRoot
		}
	}
	return w
}

func (w pluginPaneWindow) id() string { return w.info.ID }

func (w pluginPaneWindow) title() string {
	t := w.titleText
	if t == "" {
		t = w.info.Title
	}
	if t == "" {
		t = w.info.ID
	}
	if w.status != "" {
		dot := theme.Default().Resolve().Icons.Dot
		return t + " " + dot + " " + w.status
	}
	return t
}

func (w pluginPaneWindow) init() tea.Cmd {
	// Process panes start on first mount/focus (ABI §12), not at registry init.
	return nil
}

func (w pluginPaneWindow) update(msg tea.Msg) (window, tea.Cmd) {
	switch msg := msg.(type) {
	case contextStateMsg:
		w.applyContext(msg)
		return w, w.maybePushFeeds()
	case agentsStateMsg:
		w.applyAgents(msg)
		return w, w.maybePushFeeds()
	case pluginPaneFocusMsg:
		if msg.paneID != w.info.ID {
			return w, nil
		}
		w = w.setFocused(msg.focused)
		var cmd tea.Cmd
		if msg.focused && w.rt != nil && w.rt.mounted {
			cmd = w.rt.listenCmd()
		}
		return w, cmd
	case pluginPaneWakeMsg:
		if msg.paneID != w.info.ID || w.rt == nil {
			return w, nil
		}
		return w.drainRuntime()
	case pluginPaneMsg:
		if msg.paneID != w.info.ID {
			return w, nil
		}
		return w.applyPaneMsg(msg)
	case tea.KeyPressMsg:
		return w.handleKey(msg)
	}
	return w, nil
}

func (w pluginPaneWindow) drainRuntime() (window, tea.Cmd) {
	if w.rt == nil {
		return w, nil
	}
	pending := w.rt.takePending()
	var cmds []tea.Cmd
	for _, msg := range pending {
		var cmd tea.Cmd
		w, cmd = w.applyPaneMsg(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	// Re-arm listener while process may still run.
	if !w.rt.dead.Load() {
		cmds = append(cmds, w.rt.listenCmd())
	}
	return w, tea.Batch(cmds...)
}

func (w pluginPaneWindow) applyPaneMsg(msg pluginPaneMsg) (pluginPaneWindow, tea.Cmd) {
	switch msg.kind {
	case "meta":
		if msg.title != "" {
			w.titleText = clampPaneTitle(msg.title)
		}
		w.status = msg.status
	case "render":
		if len(msg.view) > 0 {
			if root, err := parsePaneView(msg.view); err == nil {
				w.viewRoot = root
				w.hasView = true
				w.errState = ""
			}
		}
	case "error", "exit":
		if msg.err != "" {
			w.errState = msg.err
		}
	case "action":
		return w.handleAction(msg)
	}
	return w, nil
}

func (w pluginPaneWindow) handleAction(msg pluginPaneMsg) (pluginPaneWindow, tea.Cmd) {
	act := msg.action
	ok := true
	errMsg := ""
	var cmd tea.Cmd
	switch strings.ToLower(strings.TrimSpace(act.Type)) {
	case "notify":
		level := strings.ToLower(act.Level)
		text := sanitizePaneText(act.Text)
		if text == "" {
			text = "notice"
		}
		// Surface via model notice by returning a small msg.
		cmd = func() tea.Msg {
			return pluginPaneNotifyMsg{text: text, err: level == "error" || level == "warn"}
		}
	case "copy", "open", "command", "pane.emit":
		// v1: acknowledge; open/command need host mediation beyond scope.
		ok = true
	default:
		ok = false
		errMsg = "unknown action"
	}
	if w.rt != nil && msg.actID != "" {
		w.rt.sendActionResult(msg.actID, ok, errMsg)
	}
	return w, cmd
}

func (w pluginPaneWindow) handleKey(msg tea.KeyPressMsg) (window, tea.Cmd) {
	if w.rt == nil || w.errState != "" {
		return w, nil
	}
	key, mods := normalizePaneKey(msg)
	if key == "" {
		return w, nil
	}
	w.rt.sendInput(key, mods)
	return w, nil
}

func normalizePaneKey(msg tea.KeyPressMsg) (key string, mods []string) {
	s := msg.String()
	switch s {
	case "enter":
		return "enter", nil
	case "esc", "escape":
		return "esc", nil
	case "up":
		return "up", nil
	case "down":
		return "down", nil
	case "left":
		return "left", nil
	case "right":
		return "right", nil
	case "tab":
		return "tab", nil
	case "backspace":
		return "backspace", nil
	case " ":
		return "space", nil
	}
	if len(msg.Text) == 1 {
		return msg.Text, nil
	}
	// Drop global chords — host already filtered most; ignore ctrl combos.
	if strings.HasPrefix(s, "ctrl+") || strings.HasPrefix(s, "alt+") || strings.HasPrefix(s, "shift+ctrl") {
		return "", nil
	}
	if len(s) == 1 {
		return s, nil
	}
	return "", nil
}

func (w pluginPaneWindow) resize(width, height int) window {
	w.width, w.height = max(0, width), max(0, height)
	if w.rt != nil && w.rt.mounted && w.width > 0 && w.height > 0 {
		w.rt.sendResize(w.width, w.height)
	}
	return w
}

func (w pluginPaneWindow) view(th theme.Theme) (out string) {
	// Isolation: never let a bad pane take down the TUI event loop.
	defer func() {
		if rec := recover(); rec != nil {
			out = stylePaneRole(th, "error", fmt.Sprintf("pane render panic: %v", rec))
		}
	}()
	if w.width <= 0 {
		return ""
	}
	th = th.Resolve()
	errState := w.errState
	if errState == "" && w.rt != nil {
		_, _, errState, _, _ = w.rt.snapshot()
	}
	if errState != "" {
		w.errState = errState
		return w.errorView(th)
	}
	// Prefer live process view when present.
	if w.rt != nil {
		_, _, _, viewRaw, hello := w.rt.snapshot()
		if len(viewRaw) > 0 {
			if root, err := parsePaneView(viewRaw); err == nil {
				w.viewRoot = root
				w.hasView = true
			}
		} else if !hello && w.def.Mode == paneModeProcess && !w.hasView {
			return stylePaneRole(th, "muted", "starting…")
		}
	}
	if !w.hasView {
		if w.def.Mode == paneModeProcess {
			return stylePaneRole(th, "muted", "starting…")
		}
		return stylePaneRole(th, "muted", "no view")
	}
	return renderPaneView(th, w.width, w.height, w.viewRoot, w.data)
}

func (w pluginPaneWindow) errorView(th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	lines := []string{
		st.Error.Render("pane error"),
		st.Muted.Render(sanitizePaneText(w.errState)),
		"",
		st.Muted.Render(w.info.Provenance()),
		st.Muted.Render("disable via /plugin"),
	}
	if w.height > 0 && len(lines) > w.height {
		lines = lines[:w.height]
	}
	return strings.Join(lines, "\n")
}

func (w *pluginPaneWindow) applyContext(s contextStateMsg) {
	if w.data == nil {
		w.data = paneDataStore{}
	}
	// session.summary
	w.data["session.summary"] = map[string]any{
		"cwd":          s.WorkDir,
		"sessionId":    s.SessionID,
		"sessionTitle": s.SessionTitle,
		"provider":     s.Provider,
		"model":        s.Model,
		"agent":        s.Agent,
		"agentState":   s.AgentState,
	}
	// usage
	used := 0
	if s.Used.Known {
		used = s.Used.N
	}
	inN, outN := 0, 0
	if s.Input.Known {
		inN = s.Input.N
	}
	if s.Output.Known {
		outN = s.Output.N
	}
	w.data["usage"] = map[string]any{
		"input":      inN,
		"output":     outN,
		"used":       used,
		"limit":      s.ContextLimit,
		"limitKnown": s.ContextLimitKnown,
		"source":     s.Source,
	}
	// clock (low frequency — always refresh snapshot; push rate-limited)
	w.data["clock"] = map[string]any{
		"unixMs": time.Now().UnixMilli(),
	}
}

func (w *pluginPaneWindow) applyAgents(s agentsStateMsg) {
	if w.data == nil {
		w.data = paneDataStore{}
	}
	roots := make([]any, 0, len(s.roots))
	for _, r := range s.roots {
		children := make([]any, 0, len(r.Children))
		for _, c := range r.Children {
			title := c.title
			if title == "" {
				title = c.name
			}
			if title == "" {
				title = c.agent
			}
			state := c.status
			if c.rosterState != "" {
				state = c.rosterState
			}
			children = append(children, map[string]any{
				"id":    c.sessionID,
				"title": title,
				"state": state,
			})
		}
		roots = append(roots, map[string]any{
			"id":       r.ID,
			"title":    r.Title,
			"state":    r.State.Label(),
			"children": children,
		})
	}
	w.data["agents.roster"] = map[string]any{
		"activeId": s.activeID,
		"roots":    roots,
	}
}

func (w pluginPaneWindow) maybePushFeeds() tea.Cmd {
	if w.rt == nil || !w.rt.mounted || w.errState != "" {
		return nil
	}
	now := time.Now()
	for _, feed := range w.def.Subscriptions {
		feed = strings.TrimSpace(feed)
		if feed == "" {
			continue
		}
		// Permission: must be in permissions.host
		if !paneHostGranted(w.def.Permissions.Host, feed) {
			continue
		}
		minInterval := 100 * time.Millisecond
		if feed == "clock" {
			minInterval = time.Second
		}
		if t, ok := w.lastPush[feed]; ok && now.Sub(t) < minInterval {
			continue
		}
		snap, ok := w.data[feed]
		if !ok {
			continue
		}
		w.lastPush[feed] = now
		w.rt.sendData(feed, snap)
	}
	return nil
}

func paneHostGranted(hostFeeds []string, feed string) bool {
	for _, h := range hostFeeds {
		if strings.TrimSpace(h) == feed {
			return true
		}
	}
	return false
}

func (w pluginPaneWindow) setFocused(focused bool) pluginPaneWindow {
	was := w.focused
	w.focused = focused
	if w.rt != nil && w.rt.mounted && was != focused {
		w.rt.sendFocus(focused)
	}
	// Mount process on first focus (ABI: start on mount).
	if focused && w.rt != nil && !w.rt.mounted && w.errState == "" {
		if err := w.rt.start(w.width, w.height); err != nil {
			w.rt.mu.Lock()
			w.rt.errState = err.Error()
			w.rt.mu.Unlock()
			w.errState = err.Error()
		} else if was != focused {
			w.rt.sendFocus(true)
		}
	}
	return w
}

func (w pluginPaneWindow) shutdown(reason string) pluginPaneWindow {
	if w.rt != nil {
		w.rt.shutdown(reason)
	}
	return w
}

// pluginPaneNotifyMsg surfaces a pane notify action as a model notice.
type pluginPaneNotifyMsg struct {
	text string
	err  bool
}

// pluginPaneFocusMsg tells one plugin pane whether it is the active right window.
type pluginPaneFocusMsg struct {
	paneID  string
	focused bool
}

// isPluginPane reports whether w is a plugin contribution window.
func isPluginPane(w window) bool {
	_, ok := w.(pluginPaneWindow)
	return ok
}
