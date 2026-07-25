package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestWindowTitle(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*Model)
		check func(t *testing.T, got string)
	}{
		{
			name:  "default empty",
			setup: func(*Model) {},
			check: func(t *testing.T, got string) {
				if got != "strike" {
					t.Errorf("got %q, want %q", got, "strike")
				}
			},
		},
		{
			name: "session id fragment",
			setup: func(m *Model) {
				m.sessionID = "sess-abcdef123456"
			},
			check: func(t *testing.T, got string) {
				if !strings.HasPrefix(got, "strike — ") {
					t.Errorf("got %q, want strike — …", got)
				}
				if !strings.Contains(got, "abcdef") {
					t.Errorf("got %q, want session fragment", got)
				}
			},
		},
		{
			name: "titleTopic set",
			setup: func(m *Model) {
				m.sessionID = "ignored-session"
				m.titleTopic = "fix the auth flow"
			},
			check: func(t *testing.T, got string) {
				if !strings.Contains(got, "fix the auth flow") {
					t.Errorf("got %q, want topic", got)
				}
				if strings.Contains(got, "ignored") {
					t.Errorf("session id should not override topic: %q", got)
				}
			},
		},
		{
			name: "control chars stripped from topic",
			setup: func(m *Model) {
				m.titleTopic = "hello\x1b[2J\x00world"
			},
			check: func(t *testing.T, got string) {
				if strings.Contains(got, "\x1b") || strings.ContainsRune(got, '\x00') {
					t.Errorf("controls retained: %q", got)
				}
				if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
					t.Errorf("sanitized topic lost content: %q", got)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			tt.setup(&m)
			tt.check(t, windowTitle(m))
		})
	}
}

func TestSanitizeNotifyMessage(t *testing.T) {
	got := sanitizeNotifyMessage("  strike:\a turn\x1b complete  ")
	if got != "strike: turn complete" {
		t.Errorf("sanitizeNotifyMessage = %q", got)
	}
	if sanitizeNotifyMessage("\a\x00") != "" {
		t.Error("expected empty after stripping controls")
	}
}

func TestNotifyUnfocusedCmdEmpty(t *testing.T) {
	if cmd := notifyUnfocusedCmd(""); cmd != nil {
		t.Error("empty message should yield nil cmd")
	}
	if cmd := notifyUnfocusedCmd("\a\x00"); cmd != nil {
		t.Error("control-only message should yield nil cmd")
	}
}

func TestFocusAndBlurMsgs(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	if !m.focused {
		t.Fatal("new model should start focused")
	}
	m = updateApp(t, m, tea.BlurMsg{})
	if m.focused {
		t.Fatal("BlurMsg did not clear focused")
	}
	m = updateApp(t, m, tea.FocusMsg{})
	if !m.focused {
		t.Fatal("FocusMsg did not set focused")
	}
}

func TestTurnCompleteNotifyOnlyWhenUnfocusedAndLong(t *testing.T) {
	// Focused: no unfocused notify path required (broadcast may still run).
	focused, _ := newAppTestModel(nil, nil)
	focused.focused = true
	focused.turnStartedAt = time.Now().Add(-notifyAfterTurn - time.Second)
	cmd := focused.applyEvent(protocol.TurnCompleted{})
	for _, msg := range runAllAppCmds(t, cmd) {
		// notifyUnfocusedCmd returns a cmd that yields nil after writing stderr.
		// We only assert focused path does not panic and clears turn chrome.
		_ = msg
	}
	if !focused.turnStartedAt.IsZero() || focused.toolCallsThisTurn != 0 {
		t.Fatal("TurnCompleted should clear turn timers when focused")
	}

	// Unfocused + long turn: applyEvent batches a notify cmd (non-nil batch).
	unfocused, _ := newAppTestModel(nil, nil)
	unfocused.focused = false
	unfocused.turnStartedAt = time.Now().Add(-notifyAfterTurn - time.Second)
	cmd = unfocused.applyEvent(protocol.TurnCompleted{})
	if cmd == nil {
		t.Fatal("unfocused long turn should return a cmd batch including notify")
	}
	// Running nested cmds must not hang; notify writes stderr then returns nil.
	_ = runAllAppCmds(t, cmd)
	if !unfocused.turnStartedAt.IsZero() {
		t.Fatal("TurnCompleted should clear turnStartedAt when unfocused")
	}

	// Unfocused but short turn: still no notify requirement beyond clearing state.
	short, _ := newAppTestModel(nil, nil)
	short.focused = false
	short.turnStartedAt = time.Now()
	_ = short.applyEvent(protocol.TurnCompleted{})
	if !short.turnStartedAt.IsZero() {
		t.Fatal("short unfocused turn should still clear turnStartedAt")
	}
}
