package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// modal is anything that temporarily takes over input, rendered as a
// centered overlay dialog (opencode's dialog-stack pattern). A nil return
// from update closes it. The width passed to view is the dialog box width.
type modal interface {
	update(msg tea.KeyMsg) (modal, tea.Cmd)
	view(width int, th theme.Theme) string
}

// permissionModal renders a pending permission ask with once/always/reject
// choices. Esc always means reject — dismissal never silently continues.
type permissionModal struct {
	req      protocol.PermissionAsked
	ops      chan<- protocol.Op
	choice   int
	state    permissionModalState
	feedback textinput.Model
	th       theme.Theme
}

type permissionModalState int

const (
	permissionModalChoice permissionModalState = iota
	permissionModalFeedback
)

var permChoices = []struct {
	label    string
	decision protocol.Decision
}{
	{"allow once", protocol.DecisionOnce},
	{"allow always", protocol.DecisionAlways},
	{"reject", protocol.DecisionReject},
}

func newPermissionModal(req protocol.PermissionAsked, ops chan<- protocol.Op, themes ...theme.Theme) *permissionModal {
	th := theme.Default()
	if len(themes) > 0 {
		th = themes[0]
	}
	in := newTextInput(th, "optional feedback")
	return &permissionModal{req: req, ops: ops, feedback: in, th: th.Resolve()}
}

func (m *permissionModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	if m.state == permissionModalFeedback {
		switch msg.String() {
		case "esc":
			return nil, m.replyWithMessage(protocol.DecisionReject, "")
		case "enter":
			return nil, m.replyWithMessage(protocol.DecisionReject, strings.TrimSpace(m.feedback.Value()))
		}
		var cmd tea.Cmd
		m.feedback, cmd = m.feedback.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "left", "h", "shift+tab":
		m.choice = (m.choice + len(permChoices) - 1) % len(permChoices)
	case "right", "l", "tab":
		m.choice = (m.choice + 1) % len(permChoices)
	case "1", "y":
		return nil, m.reply(protocol.DecisionOnce)
	case "2", "a":
		return nil, m.reply(protocol.DecisionAlways)
	case "3", "n":
		m.state = permissionModalFeedback
		return m, m.feedback.Focus()
	case "esc":
		return nil, m.reply(protocol.DecisionReject)
	case "enter":
		if permChoices[m.choice].decision == protocol.DecisionReject {
			m.state = permissionModalFeedback
			return m, m.feedback.Focus()
		}
		return nil, m.reply(permChoices[m.choice].decision)
	}
	return m, nil
}

func (m *permissionModal) reply(d protocol.Decision) tea.Cmd {
	return m.replyWithMessage(d, "")
}

func (m *permissionModal) replyWithMessage(d protocol.Decision, message string) tea.Cmd {
	req := m.req
	ops := m.ops
	return func() tea.Msg {
		ops <- protocol.PermissionReply{RequestID: req.RequestID, Decision: d, Message: message}
		return nil
	}
}

func (m *permissionModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))
	cursorWidth := max(1, ansi.StringWidth(m.feedback.Cursor.View()))
	heading := wrapToWidth(st.WarningStrong.Render("Permission required: "+m.req.Permission), inner)
	detail := wrapToWidth(st.Text.Render(strings.Join(m.req.Patterns, "\n")), inner)

	// Shared edit diff preview for choice and feedback states. Patterns already
	// show the path, so Path is left empty to avoid duplication.
	var diffSection string
	if meta, ok := parseEditMetadata(m.req.Metadata); ok {
		diffBlock := ui.DiffPreview(th, ui.DiffPreviewOpts{
			Path:      "",
			Old:       meta.OldString,
			New:       meta.NewString,
			MaxLines:  diffPreviewMaxLinesModal,
			Width:     inner,
			ShowStats: true,
		})
		if diffBlock != "" {
			diffSection = "\n" + diffBlock
		}
	}

	if m.state == permissionModalFeedback {
		prompt := st.Text.Render("Optional feedback for the rejection:")
		m.feedback.Width = max(1, inner-ansi.StringWidth(m.feedback.Prompt)-cursorWidth)
		m.feedback.SetValue(m.feedback.Value())
		body := heading + "\n" + detail + diffSection + strings.Repeat("\n", max(1, th.Spacing.SM)) + prompt + "\n" + m.feedback.View()
		return ui.Dialog(th, ui.DialogOpts{
			Title: "permission",
			Hint:  dotJoin(th, "enter reject with feedback", "esc reject without feedback"),
			Width: width,
			Tone:  ui.ToneWarning,
		}, body)
	}

	choices := make([]string, len(permChoices))
	plain := 0
	for i, c := range permChoices {
		label := itoa(i+1) + ")" + themedSpace(th.Spacing.Label) + c.label
		style := st.Muted
		if i == m.choice {
			style = st.SelectedUnderline
		}
		choices[i] = style.Render(label)
		plain += lipgloss.Width(label) + lipgloss.Width(themedSpace(th.Spacing.SM))
	}
	sep := themedSpace(th.Spacing.SM)
	if plain > inner {
		sep = "\n" // stack choices when the row would overflow a narrow dialog
	}
	body := heading + "\n" + detail + diffSection + strings.Repeat("\n", max(1, th.Spacing.SM)) + strings.Join(choices, sep)
	return ui.Dialog(th, ui.DialogOpts{
		Title: "permission",
		Hint:  dotJoin(th, "←/→ select", "enter confirm", "esc reject"),
		Width: width,
		Tone:  ui.ToneWarning,
	}, body)
}

// wrapToWidth hard-wraps already-styled text to width display cells, preserving
// ANSI. It is how modals pre-wrap bodies before handing them to ui.Panel/Dialog
// (which truncate, not wrap).
func wrapToWidth(s string, width int) string {
	if width < 1 {
		return s
	}
	return lipgloss.NewStyle().Width(width).Render(s)
}
