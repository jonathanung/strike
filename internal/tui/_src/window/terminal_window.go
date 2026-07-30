package tui

import (
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/term"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

const terminalWindowID = "editor"

// terminalOutputMsg is posted when the embedded PTY screen changes.
type terminalOutputMsg struct{}

// terminalExitMsg is posted when the embedded editor process exits.
type terminalExitMsg struct {
	path    string
	display string
	before  fileMeta
	hadPath bool
	err     error
}

// terminalWindow hosts an embedded PTY editor in the right pane.
// The session pointer is shared across value copies (window is COW).
type terminalWindow struct {
	sess    *term.Session
	width   int
	height  int
	path    string // absolute path opened, if any
	display string
	label   string // short editor name for chrome (vim, nano, nvim, …)
	before  fileMeta
	hadPath bool
	// idle is true when no session is running (placeholder).
	idle bool
}

func newTerminalWindow() terminalWindow {
	return terminalWindow{idle: true}
}

func (w terminalWindow) id() string { return terminalWindowID }

func (w terminalWindow) editorLabel() string {
	if w.label != "" {
		return w.label
	}
	return "editor"
}

func (w terminalWindow) title() string {
	dot := theme.Default().Resolve().Icons.Dot
	label := w.editorLabel()
	if w.display != "" {
		return label + " " + dot + " " + filepath.Base(w.display)
	}
	if w.path != "" {
		return label + " " + dot + " " + filepath.Base(w.path)
	}
	if w.idle {
		return "editor"
	}
	return label
}

func (w terminalWindow) init() tea.Cmd { return nil }

func (w terminalWindow) update(msg tea.Msg) (window, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if w.sess == nil || w.idle {
			return w, nil
		}
		if b := term.EncodeKey(msg); len(b) > 0 {
			_, _ = w.sess.Write(b)
		}
		return w, nil
	case terminalOutputMsg:
		return w, w.listenCmd()
	}
	return w, nil
}

func (w terminalWindow) resize(width, height int) window {
	width, height = max(0, width), max(0, height)
	w.width, w.height = width, height
	if w.sess != nil && !w.idle && width > 0 && height > 0 {
		_ = w.sess.Resize(width, height)
	}
	return w
}

func (w terminalWindow) view(th theme.Theme) string {
	if w.width <= 0 || w.height <= 0 {
		return ""
	}
	if w.sess == nil || w.idle {
		st := th.Resolve().S()
		msg := "No editor open - /vim or /nano [path]"
		return lipgloss.NewStyle().Width(max(1, w.width)).Render(
			st.Muted.Render(msg),
		)
	}
	return term.Render(w.sess, w.width, w.height)
}

// attach binds a live session and starts listening for redraw/exit.
func (w terminalWindow) attach(sess *term.Session, path, display string, before fileMeta, hadPath bool, label string) (terminalWindow, tea.Cmd) {
	// Tear down any previous session.
	if w.sess != nil && !w.idle {
		_ = w.sess.Close()
	}
	w.sess = sess
	w.path = path
	w.display = display
	w.label = label
	w.before = before
	w.hadPath = hadPath
	w.idle = false
	if w.width > 0 && w.height > 0 {
		_ = sess.Resize(w.width, w.height)
	}
	return w, w.listenCmd()
}

// listenCmd waits for either a screen update or process exit.
func (w terminalWindow) listenCmd() tea.Cmd {
	if w.sess == nil || w.idle {
		return nil
	}
	sess := w.sess
	path, display, before, hadPath := w.path, w.display, w.before, w.hadPath
	return func() tea.Msg {
		select {
		case <-sess.Notify():
			// Prefer exit if already done so we don't spin on leftover notify.
			select {
			case <-sess.Done():
				return terminalExitMsg{
					path: path, display: display, before: before, hadPath: hadPath,
					err: sess.WaitErr(),
				}
			default:
				return terminalOutputMsg{}
			}
		case <-sess.Done():
			return terminalExitMsg{
				path: path, display: display, before: before, hadPath: hadPath,
				err: sess.WaitErr(),
			}
		}
	}
}

// markIdle clears the running session after exit (session already done).
func (w terminalWindow) markIdle() terminalWindow {
	w.idle = true
	w.sess = nil
	w.path = ""
	w.display = ""
	w.label = ""
	w.hadPath = false
	w.before = fileMeta{}
	return w
}

// closeSession stops a running editor if any.
func (w terminalWindow) closeSession() terminalWindow {
	if w.sess != nil && !w.idle {
		_ = w.sess.Close()
	}
	return w.markIdle()
}

func (w terminalWindow) isRunning() bool {
	return w.sess != nil && !w.idle
}

// findTerminalWindow returns the terminal window from the registry if present.
func findTerminalWindow(r windowRegistry) (terminalWindow, int, bool) {
	for i, w := range r.windows {
		if tw, ok := w.(terminalWindow); ok {
			return tw, i, true
		}
	}
	return terminalWindow{}, -1, false
}

func replaceTerminalWindow(r windowRegistry, tw terminalWindow, activate bool) windowRegistry {
	r, ok := r.replace(terminalWindowID, tw, activate)
	if ok {
		return r
	}
	// Not registered yet — append.
	windows := append([]window(nil), r.windows...)
	windows = append(windows, tw)
	r.windows = windows
	if activate {
		r.index = len(windows) - 1
	}
	return r
}

func terminalCapturesKeys(r windowRegistry, focus paneFocus) bool {
	if focus != focusRight {
		return false
	}
	tw, _, ok := findTerminalWindow(r)
	if !ok {
		return false
	}
	active := r.active()
	if active == nil || active.id() != terminalWindowID {
		return false
	}
	return tw.isRunning()
}

// clampBody ensures the rendered terminal body does not exceed height lines.
func clampBody(s string, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}
