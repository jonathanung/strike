package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike/internal/protocol"
)

// dragCopyTranscript drags across the first transcript row and returns the
// model plus the cmd from the release (the toast reset tick).
func dragCopyTranscript(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	tr, ok := m.transcriptContentRect()
	if !ok {
		t.Fatal("no transcript rect")
	}
	m = updateApp(t, m, tea.MouseClickMsg{X: tr.X + 2, Y: tr.Y, Button: tea.MouseLeft})
	m = updateApp(t, m, tea.MouseMotionMsg{X: tr.X + 12, Y: tr.Y, Button: tea.MouseLeft})
	next, cmd := m.Update(tea.MouseReleaseMsg{X: tr.X + 12, Y: tr.Y, Button: tea.MouseLeft})
	return next.(Model), cmd
}

func newCopyToastModel(t *testing.T) Model {
	t.Helper()
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.applyEvent(protocol.UserMessage{Text: "alpha bravo charlie delta"})
	m.refreshViewport()
	m.viewport.GotoTop()
	return m
}

func topRowPlain(m Model) string {
	return ansi.Strip(strings.SplitN(viewString(m), "\n", 2)[0])
}

func TestDragCopyShowsTopRightToastThenResets(t *testing.T) {
	m := newCopyToastModel(t)
	if strings.Contains(topRowPlain(m), copyToastText) {
		t.Fatal("toast visible before any copy")
	}
	m, cmd := dragCopyTranscript(t, m)
	if cmd == nil || !m.copyToast {
		t.Fatalf("drag copy should raise toast with reset cmd (toast=%v cmd=%v)", m.copyToast, cmd != nil)
	}
	frame := viewString(m)
	if n := len(osc52Payloads(frame)); n != 1 {
		t.Fatalf("toast must not change OSC52 emission: got %d payloads", n)
	}
	top := ansi.Strip(strings.SplitN(frame, "\n", 2)[0])
	idx := strings.Index(top, copyToastText)
	if idx < 0 {
		t.Fatalf("toast missing from top row: %q", top)
	}
	if end := ansi.StringWidth(top[:idx]) + len(copyToastText); end < m.width-6 {
		t.Fatalf("toast not right-aligned: ends at col %d of %d", end, m.width)
	}
	if w := ansi.StringWidth(top); w != m.width {
		t.Fatalf("top row width = %d, want %d", w, m.width)
	}

	m = updateApp(t, m, clearCopyToastMsg{gen: m.copyToastGen})
	if m.copyToast || strings.Contains(topRowPlain(m), copyToastText) {
		t.Fatal("toast should clear on its reset tick")
	}
}

func TestCopyToastStaleResetKeepsNewerToast(t *testing.T) {
	m := newCopyToastModel(t)
	m, _ = dragCopyTranscript(t, m)
	stale := m.copyToastGen
	m = updateApp(t, m, tea.MouseClickMsg{X: 1, Y: 0, Button: tea.MouseLeft}) // clear selection
	m, _ = dragCopyTranscript(t, m)
	m = updateApp(t, m, clearCopyToastMsg{gen: stale})
	if !m.copyToast || !strings.Contains(topRowPlain(m), copyToastText) {
		t.Fatal("stale reset must not hide a newer toast")
	}
}

func TestCopyToastNotShownWithoutDragSelection(t *testing.T) {
	// A bare click (no drag) is the path terminal-native selection would look
	// like if any event reached us at all: no copy, so no badge.
	m := newCopyToastModel(t)
	tr, ok := m.transcriptContentRect()
	if !ok {
		t.Fatal("no transcript rect")
	}
	m = updateApp(t, m, tea.MouseClickMsg{X: tr.X + 2, Y: tr.Y, Button: tea.MouseLeft})
	m = updateApp(t, m, tea.MouseReleaseMsg{X: tr.X + 2, Y: tr.Y, Button: tea.MouseLeft})
	if m.copyToast || m.cellClip.osc != "" {
		t.Fatalf("bare click must not copy or toast (toast=%v)", m.copyToast)
	}
}

func TestKeyboardCopyKeepsCellFlashWithoutToast(t *testing.T) {
	m := newCopyToastModel(t)
	m.applyEvent(protocol.TextDelta{Text: "assistant reply"})
	m.applyEvent(protocol.TurnCompleted{})
	m.refreshViewport()
	_ = m.copyLastAssistantResponse()
	if m.copyToast {
		t.Fatal("keyboard copy must keep its own feedback, not the drag toast")
	}
	if m.notice != "copied last response" || m.cellClip.osc == "" {
		t.Fatalf("keyboard copy feedback changed: notice=%q", m.notice)
	}
}

func TestApplyCopyToastNarrowWidthIsNoop(t *testing.T) {
	m := newCopyToastModel(t)
	m.copyToast = true
	m.width = 5
	if got := m.applyCopyToast("abcde"); got != "abcde" {
		t.Fatalf("narrow frame should be unchanged, got %q", got)
	}
}
