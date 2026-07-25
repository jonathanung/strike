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
)

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
	return "", nil, fmt.Errorf("no editor found — set $VISUAL or $EDITOR, or install nvim/vim")
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

func (m Model) handleVimCommand(args []string) (tea.Model, tea.Cmd) {
	path, line, err := parseVimArgs(args)
	if err != nil {
		m.setNotice(err.Error(), true)
		return m, nil
	}
	m.resetComposer()
	m.clearNotice()
	return m, launchEditorCmd(m.workDir, path, line)
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
	if !msg.hadPath {
		m.setNotice("editor closed", false)
		return m, nil
	}
	if !fileChangedSince(msg.path, msg.before) {
		m.setNotice("editor closed — "+msg.display+" unchanged", false)
		return m, nil
	}
	display := msg.display
	if display == "" {
		display = msg.path
	}
	ops := m.ops
	path := display
	return m, func() tea.Msg {
		ops <- protocol.FilesChanged{
			Paths:  []string{path},
			Reason: editorReasonExternal,
		}
		return nil
	}
}
