package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
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
