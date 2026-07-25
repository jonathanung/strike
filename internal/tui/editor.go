package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/term"
)

// VimMode selects how /vim presents the editor.
type VimMode string

const (
	// VimModePane embeds the editor in the right-pane terminal window (default).
	VimModePane VimMode = "pane"
	// VimModeOverlay embeds the editor in a centered full-screen overlay.
	VimModeOverlay VimMode = "overlay"
	// VimModeTakeover hands the whole terminal to the editor via tea.ExecProcess.
	VimModeTakeover VimMode = "takeover"
)

// ParseVimMode resolves a config/flag value. Empty yields pane (default).
func ParseVimMode(value string) (VimMode, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(VimModePane):
		return VimModePane, true
	case string(VimModeOverlay):
		return VimModeOverlay, true
	case string(VimModeTakeover):
		return VimModeTakeover, true
	default:
		return "", false
	}
}

// editorReason is the FilesChanged reason stamped when /vim exits after a
// real on-disk change.
const editorReasonExternal = "external_editor"

// editorFinishedMsg is delivered after tea.ExecProcess restores the TUI.
type editorFinishedMsg struct {
	path      string // absolute path opened, or empty when bare /vim
	display   string // workdir-relative path for notices / Ops
	err       error
	before    fileMeta
	hadPath   bool
	launchErr string // non-empty when the editor never launched
}

// fileMeta is a pre-launch snapshot used to detect post-editor changes.
type fileMeta struct {
	exists  bool
	modTime time.Time
	size    int64
}

// editorWaitFlags maps known GUI editor basenames to flags that keep the
// process open until the buffer is closed (required for change detection).
var editorWaitFlags = map[string][]string{
	"code":              {"-w"},
	"code-insiders":     {"-w"},
	"codium":            {"-w"},
	"subl":              {"-w"},
	"sublime_text":      {"-w"},
	"gedit":             {"-w"},
	"gnome-text-editor": {"-w"},
}

// resolveEditor picks VISUAL, then EDITOR, then nvim/vim/vi on PATH.
// value may be a multi-word command (e.g. "code -w"); the first token is the
// binary and the rest are base args. lookPath defaults to exec.LookPath when nil.
func resolveEditor(getenv func(string) string, lookPath func(string) (string, error)) (bin string, baseArgs []string, err error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	for _, key := range []string{"VISUAL", "EDITOR"} {
		raw := strings.TrimSpace(getenv(key))
		if raw == "" {
			continue
		}
		fields := strings.Fields(raw)
		if len(fields) == 0 {
			continue
		}
		resolved, lookErr := lookPath(fields[0])
		if lookErr != nil {
			// Prefer an explicit EDITOR even when LookPath fails — exec will
			// surface a clearer error when the process actually starts.
			return fields[0], append([]string(nil), fields[1:]...), nil
		}
		return resolved, append([]string(nil), fields[1:]...), nil
	}
	for _, candidate := range []string{"nvim", "vim", "vi"} {
		resolved, lookErr := lookPath(candidate)
		if lookErr == nil {
			return resolved, nil, nil
		}
	}
	return "", nil, fmt.Errorf("no editor found - set $VISUAL or $EDITOR, or install nvim/vim")
}

// parseVimArgs interprets `/vim` arguments: optional path, optional +line or
// path:line form. Returns display path (as typed / cleaned relative) and line.
func parseVimArgs(args []string) (path string, line int, err error) {
	if len(args) == 0 {
		return "", 0, nil
	}
	if len(args) > 2 {
		return "", 0, fmt.Errorf("usage: /vim [path[:line]]")
	}
	if len(args) == 2 {
		path = args[0]
		lineArg := args[1]
		if !strings.HasPrefix(lineArg, "+") {
			return "", 0, fmt.Errorf("usage: /vim [path[:line]]")
		}
		n, convErr := strconv.Atoi(strings.TrimPrefix(lineArg, "+"))
		if convErr != nil || n < 1 {
			return "", 0, fmt.Errorf("usage: /vim [path[:line]]")
		}
		return path, n, nil
	}
	raw := args[0]
	if strings.HasPrefix(raw, "+") && !strings.Contains(raw, string(filepath.Separator)) {
		// Bare +line without a path is not useful for project files.
		return "", 0, fmt.Errorf("usage: /vim [path[:line]]")
	}
	// path:line — only split on the last colon when the suffix is a positive int
	// and the path is not a Windows drive letter alone.
	if i := strings.LastIndex(raw, ":"); i > 0 {
		suffix := raw[i+1:]
		if n, convErr := strconv.Atoi(suffix); convErr == nil && n >= 1 {
			// Avoid treating "C:" as path:line on Windows-style inputs.
			if !(len(raw) >= 2 && i == 1 && raw[1] == ':') {
				return raw[:i], n, nil
			}
		}
	}
	return raw, 0, nil
}

// buildEditorCmd constructs the process to hand to tea.ExecProcess.
func buildEditorCmd(bin string, baseArgs []string, absPath string, line int) *exec.Cmd {
	args := append([]string(nil), baseArgs...)
	base := strings.ToLower(filepath.Base(bin))
	// Strip a trailing .exe for flag lookup on Windows-style basenames.
	base = strings.TrimSuffix(base, ".exe")
	if flags, ok := editorWaitFlags[base]; ok {
		hasWait := false
		for _, a := range args {
			if a == "-w" || a == "--wait" {
				hasWait = true
				break
			}
		}
		if !hasWait {
			args = append(args, flags...)
		}
	}
	if absPath != "" {
		if line > 0 && isViFamily(base) {
			args = append(args, fmt.Sprintf("+%d", line))
		}
		args = append(args, absPath)
	}
	return exec.Command(bin, args...)
}

func isViFamily(base string) bool {
	switch base {
	case "vi", "vim", "nvim", "view", "vimdiff", "nvim-qt":
		return true
	default:
		return false
	}
}

func snapshotFile(path string) fileMeta {
	if path == "" {
		return fileMeta{}
	}
	info, err := os.Stat(path)
	if err != nil {
		return fileMeta{}
	}
	return fileMeta{exists: true, modTime: info.ModTime(), size: info.Size()}
}

func fileChangedSince(path string, before fileMeta) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		// Vanished after existing, or still missing after missing: only the
		// former is a change we care about for "user edited the file".
		return before.exists
	}
	if !before.exists {
		return true
	}
	return !info.ModTime().Equal(before.modTime) || info.Size() != before.size
}

func absPathInWorkDir(workDir, p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if workDir == "" {
		if wd, err := os.Getwd(); err == nil {
			workDir = wd
		}
	}
	return filepath.Join(workDir, p)
}

func displayPath(workDir, abs string) string {
	if abs == "" {
		return ""
	}
	if workDir == "" {
		if wd, err := os.Getwd(); err == nil {
			workDir = wd
		}
	}
	if workDir != "" {
		if rel, err := filepath.Rel(workDir, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return abs
}

// launchEditorCmd returns a tea.Cmd that full-screen takes over with the
// resolved editor. path may be empty (bare editor). workDir resolves relative
// paths; line is a 1-indexed jump for vi-family editors (0 = none).
func launchEditorCmd(workDir, path string, line int) tea.Cmd {
	bin, baseArgs, err := resolveEditor(nil, nil)
	if err != nil {
		msg := err.Error()
		return func() tea.Msg {
			return editorFinishedMsg{launchErr: msg}
		}
	}
	abs := absPathInWorkDir(workDir, path)
	display := displayPath(workDir, abs)
	before := snapshotFile(abs)
	cmd := buildEditorCmd(bin, baseArgs, abs, line)
	hadPath := abs != ""
	return tea.ExecProcess(cmd, func(runErr error) tea.Msg {
		return editorFinishedMsg{
			path:    abs,
			display: display,
			err:     runErr,
			before:  before,
			hadPath: hadPath,
		}
	})
}

// prefersTakeover reports editors that need a real GUI/TTY handoff rather
// than a PTY grid (VS Code, Sublime, gedit, …).
func prefersTakeover(bin string) bool {
	base := strings.ToLower(filepath.Base(bin))
	base = strings.TrimSuffix(base, ".exe")
	_, ok := editorWaitFlags[base]
	return ok
}

func (m Model) handleVimCommand(args []string) (tea.Model, tea.Cmd) {
	path, line, err := parseVimArgs(args)
	if err != nil {
		m.setNotice(err.Error(), true)
		return m, nil
	}
	m.resetComposer()
	m.clearNotice()

	mode := m.vimMode
	if mode == "" {
		mode = VimModePane
	}
	bin, baseArgs, resolveErr := resolveEditor(nil, nil)
	if resolveErr != nil {
		m.setNotice(resolveErr.Error(), true)
		return m, nil
	}
	// GUI editors always take over; PTY embedding is for terminal editors.
	if mode != VimModeTakeover && prefersTakeover(bin) {
		mode = VimModeTakeover
	}
	if mode == VimModeTakeover {
		return m, launchEditorCmd(m.workDir, path, line)
	}
	return m.launchEmbeddedEditor(bin, baseArgs, path, line, mode)
}

func (m Model) launchEmbeddedEditor(bin string, baseArgs []string, path string, line int, mode VimMode) (tea.Model, tea.Cmd) {
	abs := absPathInWorkDir(m.workDir, path)
	display := displayPath(m.workDir, abs)
	before := snapshotFile(abs)
	hadPath := abs != ""
	cmd := buildEditorCmd(bin, baseArgs, abs, line)

	cols, rows := 80, 24
	if tw, _, ok := findTerminalWindow(m.windows); ok && tw.width > 0 && tw.height > 0 {
		cols, rows = tw.width, tw.height
	} else if m.width > 10 && m.height > 10 {
		cols = max(20, m.width/2-4)
		rows = max(8, m.height-6)
	}

	sess, err := term.Start(cmd, cols, rows)
	if err != nil {
		m.setNotice("embedded editor failed: "+err.Error()+" - try config vimMode=takeover", true)
		return m, nil
	}

	switch mode {
	case VimModeOverlay:
		modal := newTerminalModal(sess, abs, display, before, hadPath)
		modal.setHostSize(m.width, m.height)
		m.modal = modal
		m.setNotice("embedded vim (overlay) - ctrl+g closes", false)
		return m, modal.listenCmd()
	default: // pane
		tw, _, ok := findTerminalWindow(m.windows)
		if !ok {
			tw = newTerminalWindow()
		}
		var cmd tea.Cmd
		tw, cmd = tw.attach(sess, abs, display, before, hadPath)
		m.windows = replaceTerminalWindow(m.windows, tw, true)
		focusCmd := m.setPaneFocus(focusRight)
		m.reflow()
		m.setNotice("embedded vim - keys pass through; ctrl+g leaves pane", false)
		return m, tea.Batch(cmd, focusCmd)
	}
}

func (m Model) applyEditorFinished(msg editorFinishedMsg) (tea.Model, tea.Cmd) {
	if msg.launchErr != "" {
		m.setNotice(msg.launchErr, true)
		return m, nil
	}
	if msg.err != nil {
		// Non-zero exit is common (vim :cq, user abort). Still check for
		// on-disk changes when a path was opened.
		if !msg.hadPath || !fileChangedSince(msg.path, msg.before) {
			m.setNotice("editor exited: "+msg.err.Error(), true)
			return m, nil
		}
	}
	return m.finishEditorSession(msg.path, msg.display, msg.before, msg.hadPath, msg.err)
}

func (m Model) applyTerminalExit(msg terminalExitMsg) (tea.Model, tea.Cmd) {
	// Clear overlay or idle the pane window.
	if _, ok := m.modal.(*terminalModal); ok {
		m.modal = nil
	}
	if tw, _, ok := findTerminalWindow(m.windows); ok {
		m.windows = replaceTerminalWindow(m.windows, tw.markIdle(), false)
	}
	m.reflow()
	return m.finishEditorSession(msg.path, msg.display, msg.before, msg.hadPath, msg.err)
}

func (m Model) finishEditorSession(path, display string, before fileMeta, hadPath bool, runErr error) (tea.Model, tea.Cmd) {
	if !hadPath {
		if runErr != nil {
			m.setNotice("editor closed: "+runErr.Error(), true)
		} else {
			m.setNotice("editor closed", false)
		}
		return m, nil
	}
	if !fileChangedSince(path, before) {
		m.setNotice("editor closed - "+display+" unchanged", false)
		return m, nil
	}
	if display == "" {
		display = path
	}
	ops := m.ops
	return m, func() tea.Msg {
		ops <- protocol.FilesChanged{
			Paths:  []string{display},
			Reason: editorReasonExternal,
		}
		return nil
	}
}

func (m Model) applyTerminalOutput() (tea.Model, tea.Cmd) {
	// Re-arm listen on whichever host owns the session.
	if tm, ok := m.modal.(*terminalModal); ok {
		return m, tm.listenCmd()
	}
	if tw, _, ok := findTerminalWindow(m.windows); ok && tw.isRunning() {
		// Refresh the window value (no state change) and re-listen.
		m.windows = replaceTerminalWindow(m.windows, tw, false)
		return m, tw.listenCmd()
	}
	return m, nil
}

// leaveEmbeddedEditor focuses the composer without killing a running pane
// editor (ctrl+g). Overlay mode closes via the modal handler.
func (m Model) leaveEmbeddedEditor() (tea.Model, tea.Cmd) {
	if terminalCapturesKeys(m.windows, m.focus) {
		m.completion = nil
		cmd := m.focusPane(focusLeft)
		m.reflow()
		m.setNotice("left editor pane - ctrl+l returns", false)
		return m, cmd
	}
	return m, nil
}

// closeEmbeddedSessions tears down any running PTY editor (app quit path).
func (m *Model) closeEmbeddedSessions() {
	if tm, ok := m.modal.(*terminalModal); ok && tm.sess != nil {
		_ = tm.sess.Close()
		m.modal = nil
	}
	if tw, _, ok := findTerminalWindow(m.windows); ok && tw.isRunning() {
		m.windows = replaceTerminalWindow(m.windows, tw.closeSession(), false)
	}
}
