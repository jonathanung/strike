package tui

import (
	"encoding/json"
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
		{name: "option 4", key: permissionKey("4")},
		{name: "n", key: permissionKey("n")},
		{
			name:    "enter on selected reject",
			prepare: []tea.KeyMsg{permissionKey("right"), permissionKey("right"), permissionKey("right")},
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
		{name: "s", keys: []tea.KeyMsg{permissionKey("s")}, decision: protocol.DecisionAlways},
		{
			name:     "enter on allow session",
			keys:     []tea.KeyMsg{permissionKey("right"), permissionKey("enter")},
			decision: protocol.DecisionAlways,
		},
		{name: "option 3", keys: []tea.KeyMsg{permissionKey("3")}, decision: protocol.DecisionProject},
		{name: "p", keys: []tea.KeyMsg{permissionKey("p")}, decision: protocol.DecisionProject},
		{
			name:     "enter on allow project",
			keys:     []tea.KeyMsg{permissionKey("right"), permissionKey("right"), permissionKey("enter")},
			decision: protocol.DecisionProject,
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

func TestPermissionModalEditMetadataShowsDiffInChoiceAndFeedback(t *testing.T) {
	meta := json.RawMessage(`{"oldString":"foo","newString":"bar","count":1}`)
	req := protocol.PermissionAsked{
		RequestID:  "edit-diff-req",
		Permission: "edit",
		Patterns:   []string{"file.go"},
		Metadata:   meta,
	}
	m, _ := newTestPermissionModalFrom(req)

	const width = 70
	th := theme.Default()
	choiceView := ansi.Strip(m.view(width, th))
	for _, want := range []string{"-foo", "+bar", "permission", "file.go", "+1", "-1"} {
		if !strings.Contains(choiceView, want) {
			t.Errorf("choice view missing %q:\n%s", want, choiceView)
		}
	}
	// geometry: every dialog row exact outer width
	for i, row := range strings.Split(m.view(width, th), "\n") {
		if got := lipgloss.Width(row); got != width {
			t.Errorf("choice row %d width = %d, want %d: %q", i, got, width, ansi.Strip(row))
		}
	}

	enterPermissionFeedback(t, m)
	feedbackView := ansi.Strip(m.view(width, th))
	for _, want := range []string{"-foo", "+bar", "optional feedback", "+1", "-1"} {
		if !strings.Contains(feedbackView, want) {
			t.Errorf("feedback view missing %q:\n%s", want, feedbackView)
		}
	}
	for i, row := range strings.Split(m.view(width, th), "\n") {
		if got := lipgloss.Width(row); got != width {
			t.Errorf("feedback row %d width = %d, want %d: %q", i, got, width, ansi.Strip(row))
		}
	}
}

func TestPermissionModalWithoutEditMetadataHasNoFalseDiff(t *testing.T) {
	// bash-like ask: no metadata → permission chrome only, no +/- hunk body
	m, _ := newTestPermissionModal("bash-no-meta")
	plain := ansi.Strip(m.view(70, theme.Default()))
	if !strings.Contains(strings.ToLower(plain), "permission") {
		t.Errorf("missing permission chrome:\n%s", plain)
	}
	if !strings.Contains(plain, "rm important.txt") {
		t.Errorf("missing pattern:\n%s", plain)
	}
	// write-shaped or absent meta must not invent a unified diff body
	for _, line := range strings.Split(plain, "\n") {
		trimmed := strings.TrimSpace(line)
		// skip choice labels like "1) allow once"
		if strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "─") &&
			!strings.Contains(trimmed, "select") && len(trimmed) > 1 {
			// a real diff delete line would look like "-content"; dialog borders use box chars
			if isLikelyDiffMarkerLine(trimmed) {
				t.Errorf("unexpected diff-like line without edit metadata: %q\nfull:\n%s", trimmed, plain)
			}
		}
	}
}

func TestPermissionModalTallEditDiffKeepsExactWidth(t *testing.T) {
	var oldB, newB strings.Builder
	for i := 0; i < 20; i++ {
		oldB.WriteString("old line content that is fairly long ")
		oldB.WriteByte(byte('a' + i%26))
		oldB.WriteByte('\n')
		newB.WriteString("new line content that is fairly long ")
		newB.WriteByte(byte('A' + i%26))
		newB.WriteByte('\n')
	}
	meta, err := json.Marshal(map[string]any{
		"oldString": strings.TrimSuffix(oldB.String(), "\n"),
		"newString": strings.TrimSuffix(newB.String(), "\n"),
		"count":     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := protocol.PermissionAsked{
		RequestID:  "tall-edit",
		Permission: "edit",
		Patterns:   []string{"big.go"},
		Metadata:   meta,
	}
	m, _ := newTestPermissionModalFrom(req)
	const width = 46
	th := theme.Default()
	view := m.view(width, th)
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "more lines") && !strings.Contains(plain, "-old") {
		t.Errorf("expected tall edit diff content:\n%s", plain)
	}
	for i, row := range strings.Split(view, "\n") {
		if got := lipgloss.Width(row); got != width {
			t.Errorf("row %d width = %d, want %d: %q", i, got, width, ansi.Strip(row))
		}
	}
}

// isLikelyDiffMarkerLine reports whether s looks like a unified-diff body line
// (leading +/- followed by content), as opposed to UI chrome.
func isLikelyDiffMarkerLine(s string) bool {
	if len(s) < 2 {
		return false
	}
	if s[0] != '+' && s[0] != '-' {
		return false
	}
	// stats like "+0" / choice chrome shouldn't appear without edit meta either,
	// but "+1)" style choice numbers are not diff lines.
	if s[1] >= '0' && s[1] <= '9' {
		return false
	}
	return true
}

func newTestPermissionModal(requestID string) (*permissionModal, chan protocol.Op) {
	req := protocol.PermissionAsked{
		RequestID:  requestID,
		Permission: "bash",
		Patterns:   []string{"rm important.txt"},
	}
	return newTestPermissionModalFrom(req)
}

func newTestPermissionModalFrom(req protocol.PermissionAsked) (*permissionModal, chan protocol.Op) {
	ops := make(chan protocol.Op, 4)
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
