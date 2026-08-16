package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
)

// pluginWindowGroupID is the stack group for all plugin panes (§9.3).
const pluginWindowGroupID = "plugin"

// syncPluginPanes reconciles the window registry with host.Panes.List().
// Adds new contributions, updates metadata, and removes panes whose plugins
// were disabled/removed — shutting down process runtimes first.
// Active index is preserved when possible; if the active pane is removed,
// focus falls back to context.
func syncPluginPanes(r windowRegistry, panes host.Panes) (windowRegistry, tea.Cmd) {
	if panes == nil {
		return removeAllPluginPanes(r, "disable"), nil
	}
	list, err := panes.List()
	if err != nil || list == nil {
		// Keep existing panes on transient list errors.
		return r, nil
	}

	want := map[string]host.PaneInfo{}
	order := make([]string, 0, len(list))
	for _, p := range list {
		id := strings.TrimSpace(p.ID)
		if id == "" {
			continue
		}
		// Skip unmountable process panes that aren't even trusted? Still show
		// error state so users can recover via /plugin.
		want[id] = p
		order = append(order, id)
	}

	// Index existing plugin panes.
	existing := map[string]int{}
	for i, w := range r.windows {
		if pw, ok := w.(pluginPaneWindow); ok {
			existing[pw.info.ID] = i
		}
	}

	var cmds []tea.Cmd
	windows := append([]window(nil), r.windows...)

	// Remove gone panes (reverse order to keep indices stable while deleting).
	for id, idx := range existing {
		if _, ok := want[id]; ok {
			continue
		}
		if pw, ok := windows[idx].(pluginPaneWindow); ok {
			pw = pw.shutdown("disable")
			windows[idx] = pw
		}
		// Mark for deletion.
		windows[idx] = nil
	}
	compacted := make([]window, 0, len(windows))
	for _, w := range windows {
		if w != nil {
			compacted = append(compacted, w)
		}
	}
	windows = compacted

	// Rebuild existing index after compact.
	existing = map[string]int{}
	for i, w := range windows {
		if pw, ok := w.(pluginPaneWindow); ok {
			existing[pw.info.ID] = i
		}
	}

	// Add missing panes in stable order.
	for _, id := range order {
		if _, ok := existing[id]; ok {
			// Refresh info/definition on existing (e.g. after trust grant).
			info := want[id]
			idx := existing[id]
			old := windows[idx].(pluginPaneWindow)
			// If mode/trust/root changed materially, rebuild.
			if old.info.PluginID != info.PluginID ||
				old.info.Mode != info.Mode ||
				old.info.Trusted != info.Trusted ||
				old.info.LoadError != info.LoadError ||
				string(old.info.DefinitionJSON) != string(info.DefinitionJSON) {
				old = old.shutdown("unmount")
				nw := newPluginPaneWindow(info)
				nw.width, nw.height = old.width, old.height
				windows[idx] = nw
				if cmd := nw.init(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
			continue
		}
		nw := newPluginPaneWindow(want[id])
		windows = append(windows, nw)
		if cmd := nw.init(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	// Preserve active id when possible.
	activeID := ""
	if len(r.windows) > 0 {
		cur := r.windows[r.index%len(r.windows)]
		if cur != nil {
			activeID = cur.id()
		}
	}

	r.windows = windows
	r.groups = defaultWindowGroups(r.windows)
	if activeID != "" {
		if next, ok := r.activate(activeID); ok {
			r = next
		} else {
			r, _ = r.activate("context")
		}
	}
	return r, tea.Batch(cmds...)
}

func removeAllPluginPanes(r windowRegistry, reason string) windowRegistry {
	windows := make([]window, 0, len(r.windows))
	activeID := ""
	if len(r.windows) > 0 {
		activeID = r.windows[r.index%len(r.windows)].id()
	}
	for _, w := range r.windows {
		if pw, ok := w.(pluginPaneWindow); ok {
			_ = pw.shutdown(reason)
			continue
		}
		windows = append(windows, w)
	}
	r.windows = windows
	r.groups = defaultWindowGroups(r.windows)
	if activeID != "" {
		if next, ok := r.activate(activeID); ok {
			return next
		}
	}
	r, _ = r.activate("context")
	return r
}

// pluginPaneIDs returns ids of registered plugin panes (for tests/slash).
func pluginPaneIDs(r windowRegistry) []string {
	var out []string
	for _, w := range r.windows {
		if pw, ok := w.(pluginPaneWindow); ok {
			out = append(out, pw.info.ID)
		}
	}
	return out
}

// notifyPluginPaneFocus marks the active plugin pane focused and others unfocused,
// starting process panes on first focus (ABI mount).
func notifyPluginPaneFocus(r windowRegistry) (windowRegistry, tea.Cmd) {
	activeID := ""
	if a := r.active(); a != nil {
		activeID = a.id()
	}
	windows := append([]window(nil), r.windows...)
	var cmds []tea.Cmd
	changed := false
	for i, w := range windows {
		pw, ok := w.(pluginPaneWindow)
		if !ok {
			continue
		}
		want := pw.info.ID == activeID
		if pw.focused == want && !(want && pw.rt != nil && !pw.rt.mounted && pw.errState == "") {
			continue
		}
		next := pw.setFocused(want)
		windows[i] = next
		changed = true
		if want && next.rt != nil && next.rt.mounted {
			cmds = append(cmds, next.rt.listenCmd())
		}
	}
	if !changed {
		return r, nil
	}
	r.windows = windows
	return r, tea.Batch(cmds...)
}
