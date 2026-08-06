package tui

import (
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// handleConfigCommand implements /config [nano] [global|project] [slot].
func (m Model) handleConfigCommand(args []string) (tea.Model, tea.Cmd) {
	forceNano, scope, slot, err := parseConfigCommandArgs(args)
	if err != nil {
		m.setNotice(err.Error(), true)
		return m, nil
	}
	m.resetComposer()
	m.clearNotice()

	if slot == "" {
		// Bare /config (optional nano/scope without slot) → picker.
		// Scope-only without slot still opens the full picker (filterable).
		_ = scope
		m.modal = newConfigModal(m.services, m.ops, m.th, m.workDir, forceNano, false)
		return m, nil
	}

	if m.services.ConfigFiles == nil {
		m.setNotice("config file listing is unavailable", true)
		return m, nil
	}
	refs := m.services.ConfigFiles.List(m.workDir)
	ref, ok := findConfigRef(refs, scope, slot)
	if !ok {
		label := slot
		if scope != "" {
			label = string(scope) + " " + slot
		}
		m.setNotice("unknown or unavailable config slot: "+label, true)
		return m, nil
	}
	return m.openConfigFileRef(ref, forceNano)
}

// openConfigFileRef ensures the path exists (create stub when allowed) and
// launches the embedded editor forced to overlay (TUI) or takeover (GUI).
func (m Model) openConfigFileRef(ref host.ConfigFileRef, forceNano bool) (tea.Model, tea.Cmd) {
	if m.services.ConfigFiles == nil {
		m.setNotice("config file listing is unavailable", true)
		return m, nil
	}
	path, created, err := m.services.ConfigFiles.Ensure(ref)
	if err != nil {
		m.setNotice(err.Error(), true)
		return m, nil
	}
	display := ref.Display
	if display == "" {
		display = path
	}
	m.modal = nil
	next, cmd := m.launchConfigEditor(path, display, forceNano)
	if created {
		// Preserve launch notice (overlay chrome) after create confirmation.
		if nm, ok := next.(Model); ok {
			suffix := strings.TrimSpace(nm.notice)
			if suffix != "" {
				nm.setNotice("created "+display+" - "+suffix, false)
			} else {
				nm.setNotice("created "+display, false)
			}
			return nm, cmd
		}
	}
	return next, cmd
}

// launchConfigEditor opens path via the existing editor stack. TUI editors use
// overlay without mutating session vimMode/nanoMode; GUI editors take over.
func (m Model) launchConfigEditor(path, display string, forceNano bool) (tea.Model, tea.Cmd) {
	var bin string
	var baseArgs []string
	var label string
	if forceNano {
		var err error
		bin, err = resolveNano(nil)
		if err != nil {
			m.setNotice(err.Error(), true)
			return m, nil
		}
		label = "nano"
	} else {
		var err error
		bin, baseArgs, err = resolveEditor(nil, nil)
		if err != nil {
			m.setNotice(err.Error(), true)
			return m, nil
		}
		label = editorLabel(bin, "vim")
	}

	mode := VimModeOverlay
	if prefersTakeover(bin) {
		mode = VimModeTakeover
	}

	// Prefer absolute path so workDir joining does not rewrite ~/.strike paths.
	abs := path
	if abs != "" && !filepath.IsAbs(abs) {
		abs = absPathInWorkDir(m.workDir, abs)
	}
	if display == "" {
		display = displayPath(m.workDir, abs)
	}

	if mode == VimModeTakeover {
		return m, launchEditorBinCmd("", abs, 0, bin, baseArgs)
	}
	// launchEmbeddedEditor joins relative paths against workDir; pass abs.
	return m.launchEmbeddedEditor(bin, baseArgs, abs, 0, mode, label, "vimMode")
}

// applyConfigFileOpen handles picker selection.
func (m Model) applyConfigFileOpen(msg configFileOpenMsg) (tea.Model, tea.Cmd) {
	return m.openConfigFileRef(msg.ref, msg.forceNano)
}

// reloadAfterConfigEdit applies best-effort live reloads after a config file
// change under a .strike root. Returns an extra notice suffix (may be empty).
// Non-.strike paths (ordinary /vim targets) are ignored even when basenames
// collide (e.g. a project file named "config").
func (m *Model) reloadAfterConfigEdit(absPath string) string {
	if absPath == "" || !pathUnderStrikeRoot(absPath, m.workDir) {
		return ""
	}
	base := strings.ToLower(filepath.Base(absPath))
	slash := filepath.ToSlash(absPath)

	switch base {
	case "keybinds.jsonc", "keybinds.json":
		if m.services.ConfigFiles == nil {
			return ""
		}
		overrides, err := m.services.ConfigFiles.LoadKeybinds(m.workDir)
		if err != nil {
			return " - keybinds reload failed: " + err.Error()
		}
		m.keyOverrides = cloneKeybindMap(overrides)
		m.keyMap = buildKeyMap(m.keyOverrides, m.splitOrientation)
		return " - keybinds reloaded"
	case "config":
		return m.reloadPresentationFromSettings()
	case "mcp.jsonc", "mcp.json":
		return " - MCP reconnect may need /mcp or restart"
	case "providers.jsonc", "providers.json":
		return " - provider catalog refresh may need restart"
	default:
		// Theme JSON under .strike/themes: re-apply current theme id if it
		// matches the edited stem (catalog reload picks up file bytes).
		if strings.Contains(slash, "/themes/") && strings.HasSuffix(base, ".json") {
			return m.reloadPresentationFromSettings()
		}
		return ""
	}
}

// pathUnderStrikeRoot reports whether abs is under ~/.strike or <workDir>/.strike.
func pathUnderStrikeRoot(abs, workDir string) bool {
	abs = filepath.Clean(abs)
	sep := string(os.PathSeparator)
	marker := sep + ".strike" + sep
	if strings.Contains(abs, marker) || strings.HasSuffix(abs, sep+".strike") {
		return true
	}
	// Also accept exact file under a resolved .strike when marker was symlink-collapsed.
	if workDir != "" {
		root := filepath.Clean(filepath.Join(workDir, ".strike"))
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		if abs == root || strings.HasPrefix(abs, root+sep) {
			return true
		}
	}
	return false
}

func (m *Model) reloadPresentationFromSettings() string {
	if m.services.Settings == nil {
		return " - engine dials apply on new session"
	}
	d := m.services.Settings.Defaults()
	// Theme
	if id := strings.TrimSpace(d.Theme); id != "" {
		if entry, ok := theme.Lookup(theme.Catalog(m.workDir), id); ok {
			m.applyThemeEntry(entry)
		}
	}
	if d.VimMode != "" {
		if mode, ok := ParseVimMode(d.VimMode); ok {
			m.vimMode = mode
		}
	}
	if d.NanoMode != "" {
		if mode, ok := ParseNanoMode(d.NanoMode); ok {
			m.nanoMode = mode
		}
	}
	if d.MdReadMode != "" {
		if mode, ok := ParseSurfacePresentation(d.MdReadMode); ok {
			m.mdReadMode = mode
		}
	}
	if d.Notify != "" {
		if mode, ok := ParseNotifyMode(d.Notify); ok {
			m.notifyMode = mode
		}
	}
	if d.PermissionAutoApproveSeconds >= 0 {
		m.permissionAutoApproveSeconds = d.PermissionAutoApproveSeconds
	}
	if d.PermissionAutoApproveExclude != nil {
		m.permissionAutoApproveExclude = append([]string(nil), d.PermissionAutoApproveExclude...)
	}
	return " - engine dials (model, sandbox, permissions, compaction, ...) apply on new session"
}
