package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/term"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

// terminalModal is a near-full-screen overlay hosting an embedded PTY editor.
// Screen updates are handled by Model (terminalOutputMsg); this modal only
// receives keys, matching the modal interface.
type terminalModal struct {
	sess    *term.Session
	path    string
	display string
	label   string // short editor name for chrome (vim, nano, nvim, …)
	before  fileMeta
	hadPath bool
	// hostW/hostH are set by Model before each view so the overlay can size
	// itself against the full terminal rather than the dialog width alone.
	hostW, hostH int
}

func newTerminalModal(sess *term.Session, path, display string, before fileMeta, hadPath bool, label string) *terminalModal {
	if label == "" {
		label = "editor"
	}
	return &terminalModal{
		sess:    sess,
		path:    path,
		display: display,
		label:   label,
		before:  before,
		hadPath: hadPath,
	}
}

func (m *terminalModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	// ctrl+g closes the overlay editor.
	if msg.String() == "ctrl+g" {
		if m.sess != nil {
			_ = m.sess.Close()
		}
		exit := terminalExitMsg{
			path: m.path, display: m.display, before: m.before, hadPath: m.hadPath,
		}
		return nil, func() tea.Msg { return exit }
	}
	if m.sess != nil {
		if b := term.EncodeKey(msg); len(b) > 0 {
			_, _ = m.sess.Write(b)
		}
	}
	return m, nil
}

func (m *terminalModal) view(width int, th theme.Theme) string {
	// Prefer host dimensions when Model stamped them; fall back to width.
	outerW := m.hostW
	outerH := m.hostH
	if outerW < 1 {
		outerW = width
	}
	if outerH < 1 {
		outerH = 20
	}
	innerW := max(20, min(width, outerW-2))
	innerH := max(8, outerH-2)
	title := m.label
	if title == "" {
		title = "editor"
	}
	if m.display != "" {
		// Title is plain text; separators come from theme at render via Panel.
		title = title + " " + m.display
	}
	body := ""
	if m.sess != nil {
		contentW := ui.PanelInnerWidth(th, innerW)
		contentH := ui.PanelInnerHeight(innerW, innerH)
		if contentW < 1 {
			contentW = max(1, innerW-2)
		}
		if contentH < 1 {
			contentH = max(1, innerH-2)
		}
		_ = m.sess.Resize(contentW, contentH)
		body = term.Render(m.sess, contentW, contentH)
	}
	return ui.Panel(th, ui.PanelOpts{
		Title:   title,
		Width:   innerW,
		Height:  innerH,
		Focused: true,
		Footer:  "ctrl+g close",
	}, body)
}

func (m *terminalModal) listenCmd() tea.Cmd {
	if m.sess == nil {
		return nil
	}
	sess := m.sess
	path, display, before, hadPath := m.path, m.display, m.before, m.hadPath
	return func() tea.Msg {
		select {
		case <-sess.Notify():
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

func (m *terminalModal) setHostSize(w, h int) {
	m.hostW, m.hostH = w, h
}
