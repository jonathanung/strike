package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const permissionCmdTimeout = 2 * time.Second

func TestPermissionModalRejectChoicesOpenFeedbackWithoutReply(t *testing.T) {
	tests := []struct {
		name    string
		prepare []tea.KeyMsg
		key     tea.KeyMsg
	}{
		{name: "option 3", key: permissionKey("3")},
		{name: "n", key: permissionKey("n")},
		{
			name:    "enter on selected reject",
			prepare: []tea.KeyMsg{permissionKey("right"), permissionKey("right")},
			key:     permissionKey("enter"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ops := newTestPermissionModal(t.Name())
			for _, key := range tt.prepare {
				next, cmd := m.update(key)
				if next == nil {
					t.Fatalf("modal closed while selecting reject with %q", key.String())
				}
				runPermissionCmd(t, cmd)
			}

			next, cmd := m.update(tt.key)
			if next == nil {
				t.Fatal("reject choice closed modal instead of requesting feedback")
			}
			runPermissionCmd(t, cmd)
			if !strings.Contains(strings.ToLower(next.view(70, theme.Default())), "optional feedback") {
				t.Fatal("reject choice did not show the feedback view")
			}
			assertNoPermissionReply(t, ops)
		})
	}
}

func TestPermissionModalFeedbackEnterEmitsTrimmedRejection(t *testing.T) {
	m, ops := newTestPermissionModal("feedback-request")
	enterPermissionFeedback(t, m)

	next, cmd := m.update(permissionKey("  use a safer command  "))
	if next == nil {
		t.Fatal("typing feedback closed modal")
	}
	runPermissionCmd(t, cmd)

	next, cmd = m.update(permissionKey("enter"))
	if next != nil {
		t.Fatal("enter after feedback did not close modal")
	}
	reply := receiveSinglePermissionReply(t, ops, cmd)
	assertPermissionReply(t, reply, "feedback-request", protocol.DecisionReject, "use a safer command")
}

func TestPermissionModalEscapeRejectsWithoutFeedback(t *testing.T) {
	tests := []struct {
		name     string
		feedback bool
	}{
		{name: "from initial choice"},
		{name: "from feedback", feedback: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ops := newTestPermissionModal(t.Name())
			if tt.feedback {
				enterPermissionFeedback(t, m)
				next, cmd := m.update(permissionKey("ignored feedback"))
				if next == nil {
					t.Fatal("typing feedback closed modal")
				}
				runPermissionCmd(t, cmd)
			}

			next, cmd := m.update(permissionKey("esc"))
			if next != nil {
				t.Fatal("escape did not close modal")
			}
			reply := receiveSinglePermissionReply(t, ops, cmd)
			assertPermissionReply(t, reply, t.Name(), protocol.DecisionReject, "")
		})
	}
}

func TestPermissionModalAllowsRemainImmediateAndHaveNoFeedback(t *testing.T) {
	tests := []struct {
		name     string
		keys     []tea.KeyMsg
		decision protocol.Decision
	}{
		{name: "option 1", keys: []tea.KeyMsg{permissionKey("1")}, decision: protocol.DecisionOnce},
		{name: "y", keys: []tea.KeyMsg{permissionKey("y")}, decision: protocol.DecisionOnce},
		{name: "enter on allow once", keys: []tea.KeyMsg{permissionKey("enter")}, decision: protocol.DecisionOnce},
		{name: "option 2", keys: []tea.KeyMsg{permissionKey("2")}, decision: protocol.DecisionAlways},
		{name: "a", keys: []tea.KeyMsg{permissionKey("a")}, decision: protocol.DecisionAlways},
		{
			name:     "enter on allow always",
			keys:     []tea.KeyMsg{permissionKey("right"), permissionKey("enter")},
			decision: protocol.DecisionAlways,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, ops := newTestPermissionModal(t.Name())
			for i, key := range tt.keys {
				next, cmd := m.update(key)
				if i < len(tt.keys)-1 {
					if next == nil {
						t.Fatalf("modal closed before final key %q", key.String())
					}
					runPermissionCmd(t, cmd)
					continue
				}
				if next != nil {
					t.Fatalf("allow key %q did not close modal", key.String())
				}
				reply := receiveSinglePermissionReply(t, ops, cmd)
				assertPermissionReply(t, reply, t.Name(), tt.decision, "")
			}
		})
	}
}

func TestPermissionModalFeedbackIsOneLineAndViewExplainsControls(t *testing.T) {
	m, ops := newTestPermissionModal("one-line-request")
	enterPermissionFeedback(t, m)

	next, cmd := m.update(permissionKey("first\nsecond\tthird"))
	if next == nil {
		t.Fatal("pasting feedback closed modal")
	}
	runPermissionCmd(t, cmd)
	view := strings.ToLower(next.view(70, theme.Default()))
	for _, want := range []string{
		"optional feedback for the rejection",
		"enter reject with feedback",
		"esc reject without feedback",
		"first second third",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("feedback view does not contain %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "first\nsecond") {
		t.Errorf("feedback rendered on multiple lines:\n%s", view)
	}

	next, cmd = m.update(permissionKey("enter"))
	if next != nil {
		t.Fatal("enter after feedback did not close modal")
	}
	reply := receiveSinglePermissionReply(t, ops, cmd)
	assertPermissionReply(t, reply, "one-line-request", protocol.DecisionReject, "first second third")
}

func TestPermissionModalFeedbackGeometryHonorsThemeSpacingAndInputCursor(t *testing.T) {
	setTUITrueColor(t)
	for _, tt := range []struct {
		name        string
		spacing     theme.Spacing
		inputCursor string
	}{
		{name: "default", spacing: theme.Default().Spacing},
		{name: "zero extra-small spacing", spacing: theme.NewSpacing(0, 2, 3, 4)},
		{name: "wide extra-small spacing and multicell cursor", spacing: theme.NewSpacing(4, 2, 3, 4), inputCursor: ">>"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			th := theme.Default()
			th.Spacing = tt.spacing
			if tt.inputCursor != "" {
				th.Icons.InputCursor = tt.inputCursor
			}
			m, _ := newTestPermissionModal(t.Name())
			m.th = th.Resolve()
			m.feedback = newTextInput(th, "optional feedback")
			enterPermissionFeedback(t, m)
			m.feedback.SetValue(strings.Repeat("feedback ", 20))
			m.feedback.Focus()

			const width = 46
			inner := ui.PanelInnerWidth(th, width)
			_ = m.view(width, th)
			feedback := m.feedback.View()
			if got := lipgloss.Width(feedback); got > inner {
				t.Errorf("feedback width = %d, want <= dialog inner width %d: %q", got, inner, ansi.Strip(feedback))
			}
			if strings.Contains(ansi.Strip(feedback), th.Icons.Ellipsis) {
				t.Errorf("feedback input used an ellipsis instead of retaining its cursor: %q", ansi.Strip(feedback))
			}
			if !hasReverseVideo(feedback) {
				t.Errorf("feedback input lost its static cursor: %q", feedback)
			}

			view := m.view(width, th)
			for i, row := range strings.Split(view, "\n") {
				if got := lipgloss.Width(row); got != width {
					t.Errorf("dialog row %d width = %d, want exact outer width %d: %q", i, got, width, ansi.Strip(row))
				}
			}
		})
	}
}

func newTestPermissionModal(requestID string) (*permissionModal, chan protocol.Op) {
	ops := make(chan protocol.Op, 4)
	req := protocol.PermissionAsked{
		RequestID:  requestID,
		Permission: "bash",
		Patterns:   []string{"rm important.txt"},
	}
	return newPermissionModal(req, ops), ops
}

func enterPermissionFeedback(t *testing.T, m *permissionModal) {
	t.Helper()
	next, cmd := m.update(permissionKey("n"))
	if next == nil {
		t.Fatal("reject choice closed modal instead of requesting feedback")
	}
	runPermissionCmd(t, cmd)
}

func permissionKey(key string) tea.KeyMsg {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}

func runPermissionCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() {
		done <- cmd()
	}()
	select {
	case msg := <-done:
		return msg
	case <-time.After(permissionCmdTimeout):
		t.Fatalf("tea command did not complete within %s", permissionCmdTimeout)
		return nil
	}
}

func receiveSinglePermissionReply(t *testing.T, ops <-chan protocol.Op, cmd tea.Cmd) protocol.PermissionReply {
	t.Helper()
	runPermissionCmd(t, cmd)
	select {
	case op := <-ops:
		reply, ok := op.(protocol.PermissionReply)
		if !ok {
			t.Fatalf("operation type = %T, want protocol.PermissionReply", op)
		}
		assertNoPermissionReply(t, ops)
		return reply
	default:
		t.Fatal("permission command emitted no reply")
		return protocol.PermissionReply{}
	}
}

func assertNoPermissionReply(t *testing.T, ops <-chan protocol.Op) {
	t.Helper()
	select {
	case op := <-ops:
		t.Fatalf("unexpected extra permission operation: %#v", op)
	default:
	}
}

func assertPermissionReply(t *testing.T, got protocol.PermissionReply, requestID string, decision protocol.Decision, message string) {
	t.Helper()
	want := protocol.PermissionReply{RequestID: requestID, Decision: decision, Message: message}
	if got != want {
		t.Errorf("permission reply = %#v, want %#v", got, want)
	}
}
