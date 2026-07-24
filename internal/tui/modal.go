package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
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

func newPermissionModal(req protocol.PermissionAsked, ops chan<- protocol.Op) *permissionModal {
	in := textinput.New()
	in.Placeholder = "optional feedback"
	in.Prompt = "> "
	return &permissionModal{req: req, ops: ops, feedback: in}
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
	title := lipgloss.NewStyle().Foreground(th.Warning).Bold(true).
		Render("Permission required: " + m.req.Permission)
	detail := lipgloss.NewStyle().Foreground(th.Text).Width(width - 6).
		Render(strings.Join(m.req.Patterns, "\n"))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(th.Warning).
		Padding(0, 1).
		Width(width - 2)

	if m.state == permissionModalFeedback {
		prompt := lipgloss.NewStyle().Foreground(th.Text).
			Render("Optional feedback for the rejection:")
		m.feedback.Width = width - 8
		hint := lipgloss.NewStyle().Foreground(th.TextMuted).
			Render("enter reject with feedback · esc reject without feedback")
		return box.Render(title + "\n" + detail + "\n\n" + prompt + "\n" + m.feedback.View() + "\n" + hint)
	}

	var choices []string
	for i, c := range permChoices {
		label := " " + itoa(i+1) + ") " + c.label + " "
		style := lipgloss.NewStyle().Foreground(th.TextMuted)
		if i == m.choice {
			style = lipgloss.NewStyle().Foreground(th.Accent).Bold(true).Underline(true)
		}
		choices = append(choices, style.Render(label))
	}
	hint := lipgloss.NewStyle().Foreground(th.TextMuted).
		Render("←/→ select · enter confirm · esc reject")

	return box.Render(title + "\n" + detail + "\n\n" + strings.Join(choices, " ") + "\n" + hint)
}
