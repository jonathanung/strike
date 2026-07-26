package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	// Caps length so secrets/blobs cannot ride along.
	long := strings.Repeat("x", notifyMessageMaxRunes+40)
	if n := utf8.RuneCountInString(sanitizeNotifyMessage(long)); n != notifyMessageMaxRunes {
		t.Errorf("length = %d, want %d", n, notifyMessageMaxRunes)
	}
	// BEL / OSC inject attempts stripped.
	if strings.ContainsAny(sanitizeNotifyMessage("ok\x07\x1b]9;evil\x07"), "\a\x1b") {
		t.Error("controls survived sanitize")
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

func TestParseNotifyMode(t *testing.T) {
	tests := []struct {
		in   string
		want NotifyMode
		ok   bool
	}{
		{"", NotifyUnfocusedOnly, true},
		{"unfocused-only", NotifyUnfocusedOnly, true},
		{"unfocused", NotifyUnfocusedOnly, true},
		{"on", NotifyOn, true},
		{"ALWAYS", NotifyOn, true},
		{"off", NotifyOff, true},
		{"never", NotifyOff, true},
		{"nope", "", false},
	}
	for _, tt := range tests {
		got, ok := ParseNotifyMode(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseNotifyMode(%q) = %q,%v want %q,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestFocusAndBlurMsgs(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	if !m.focused {
		t.Fatal("new model should start focused")
	}
	if m.focusKnown {
		t.Fatal("focus should be unknown until Focus/Blur")
	}
	m = updateApp(t, m, tea.BlurMsg{})
	if m.focused {
		t.Fatal("BlurMsg did not clear focused")
	}
	if !m.focusKnown {
		t.Fatal("BlurMsg should mark focus known")
	}
	m = updateApp(t, m, tea.FocusMsg{})
	if !m.focused {
		t.Fatal("FocusMsg did not set focused")
	}
	if !m.focusKnown {
		t.Fatal("FocusMsg should keep focus known")
	}
}

func TestShouldDesktopNotifyGating(t *testing.T) {
	longAgo := time.Now().Add(-notifyAfterTurn - time.Second)
	tests := []struct {
		name      string
		mode      NotifyMode
		focused   bool
		known     bool
		started   time.Time
		attention bool
		want      bool
	}{
		{"off attention", NotifyOff, false, true, longAgo, true, false},
		{"off turn", NotifyOff, false, true, longAgo, false, false},
		{"on attention focused", NotifyOn, true, true, time.Time{}, true, true},
		{"on short turn", NotifyOn, true, true, time.Now(), false, false},
		{"on long turn", NotifyOn, true, true, longAgo, false, true},
		{"unfocused attention", NotifyUnfocusedOnly, false, true, time.Time{}, true, true},
		{"focused attention no spam", NotifyUnfocusedOnly, true, true, time.Time{}, true, false},
		{"unfocused long turn", NotifyUnfocusedOnly, false, true, longAgo, false, true},
		{"focused long turn no spam", NotifyUnfocusedOnly, true, true, longAgo, false, false},
		{"unfocused short turn", NotifyUnfocusedOnly, false, true, time.Now(), false, false},
		// Focus unknown: heuristic allows attention + long turns.
		{"unknown focus attention", NotifyUnfocusedOnly, true, false, time.Time{}, true, true},
		{"unknown focus long turn", NotifyUnfocusedOnly, true, false, longAgo, false, true},
		{"unknown focus short turn", NotifyUnfocusedOnly, true, false, time.Now(), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m.notifyMode = tt.mode
			m.focused = tt.focused
			m.focusKnown = tt.known
			m.turnStartedAt = tt.started
			if got := m.shouldDesktopNotify(tt.attention); got != tt.want {
				t.Fatalf("shouldDesktopNotify(%v) = %v, want %v", tt.attention, got, tt.want)
			}
			cmd := m.desktopNotifyCmd("strike: test", tt.attention)
			if tt.want && cmd == nil {
				t.Fatal("want non-nil notify cmd")
			}
			if !tt.want && cmd != nil {
				t.Fatal("want nil notify cmd")
			}
		})
	}
}

func TestTurnCompleteNotifyOnlyWhenUnfocusedAndLong(t *testing.T) {
	// Focused + known: no notify path required (broadcast may still run).
	focused, _ := newAppTestModel(nil, nil)
	focused.focused = true
	focused.focusKnown = true
	focused.turnStartedAt = time.Now().Add(-notifyAfterTurn - time.Second)
	cmd := focused.applyEvent(protocol.TurnCompleted{})
	for _, msg := range runAllAppCmds(t, cmd) {
		_ = msg
	}
	if !focused.turnStartedAt.IsZero() || focused.toolCallsThisTurn != 0 {
		t.Fatal("TurnCompleted should clear turn timers when focused")
	}

	// Unfocused + long turn: applyEvent batches a notify cmd (non-nil batch).
	unfocused, _ := newAppTestModel(nil, nil)
	unfocused.focused = false
	unfocused.focusKnown = true
	unfocused.turnStartedAt = time.Now().Add(-notifyAfterTurn - time.Second)
	cmd = unfocused.applyEvent(protocol.TurnCompleted{})
	if cmd == nil {
		t.Fatal("unfocused long turn should return a cmd batch including notify")
	}
	_ = runAllAppCmds(t, cmd)
	if !unfocused.turnStartedAt.IsZero() {
		t.Fatal("TurnCompleted should clear turnStartedAt when unfocused")
	}

	// Unfocused but short turn: still no notify requirement beyond clearing state.
	short, _ := newAppTestModel(nil, nil)
	short.focused = false
	short.focusKnown = true
	short.turnStartedAt = time.Now()
	_ = short.applyEvent(protocol.TurnCompleted{})
	if !short.turnStartedAt.IsZero() {
		t.Fatal("short unfocused turn should still clear turnStartedAt")
	}
}

func TestPermissionNotifyGating(t *testing.T) {
	// Focused + known: no desktop notify.
	focused, _ := newAppTestModel(nil, nil)
	focused.focused = true
	focused.focusKnown = true
	if focused.desktopNotifyCmd("strike: permission required", true) != nil {
		t.Fatal("focused session must not spam permission notify")
	}

	// Unfocused: notify.
	unfocused, _ := newAppTestModel(nil, nil)
	unfocused.focused = false
	unfocused.focusKnown = true
	if unfocused.desktopNotifyCmd("strike: permission required", true) == nil {
		t.Fatal("unfocused permission should notify")
	}

	// notify=off suppresses even when unfocused.
	off, _ := newAppTestModelWithOptions(Options{NotifyMode: NotifyOff})
	off.focused = false
	off.focusKnown = true
	if off.desktopNotifyCmd("strike: permission required", true) != nil {
		t.Fatal("notify=off must suppress")
	}

	// notify=on fires even when focused.
	on, _ := newAppTestModelWithOptions(Options{NotifyMode: NotifyOn})
	on.focused = true
	on.focusKnown = true
	if on.desktopNotifyCmd("strike: permission required", true) == nil {
		t.Fatal("notify=on should fire when focused")
	}
}

func TestNotifyModeFromOptions(t *testing.T) {
	m, _ := newAppTestModelWithOptions(Options{NotifyMode: NotifyOff})
	if m.notifyMode != NotifyOff {
		t.Fatalf("notifyMode = %q, want off", m.notifyMode)
	}
	m2, _ := newAppTestModel(nil, nil)
	if m2.notifyMode != NotifyUnfocusedOnly {
		t.Fatalf("default notifyMode = %q", m2.notifyMode)
	}
}
