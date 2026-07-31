package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func withPaintClock(m Model, now *time.Time) Model {
	m.ensurePaint().nowFn = func() time.Time { return *now }
	return m
}

func frameBuilds(m Model) int {
	if m.paint == nil {
		return 0
	}
	return m.paint.renderFrameCalls
}

func TestTextDeltaPaintsAreFPSCapped(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	now := time.Unix(1_700_000_000, 0)
	m = withPaintClock(m, &now)
	_ = viewString(m)
	base := frameBuilds(m)

	m = updateApp(t, m, engineEventMsg{ev: protocol.TurnStarted{}})
	_ = viewString(m)

	const n = 80
	for i := 0; i < n; i++ {
		m = updateApp(t, m, engineEventMsg{ev: protocol.TextDelta{Text: "x"}})
		_ = viewString(m)
	}
	// All deltas in one FPS window → O(1) full builds, not O(n).
	deltaBuilds := frameBuilds(m) - base
	if deltaBuilds > 4 {
		t.Fatalf("TextDelta frame builds = %d over %d deltas, want O(FPS) (<=4 incl. TurnStarted)", deltaBuilds, n)
	}
	if !m.paint.pending && !m.paint.suppress {
		t.Fatal("expected soft coalesce to suppress or pend paints within the FPS window")
	}
	if !m.paint.armed && m.paint.pending {
		t.Fatal("pending soft paint must arm a paintFlushMsg tick")
	}

	// Flush after the interval surfaces the batched text (do not run listen cmds).
	now = now.Add(paintFPSInterval)
	m = updateApp(t, m, paintFlushMsg{})
	if got := ansi.Strip(m.viewport.View()); !strings.Contains(got, "x") {
		t.Fatalf("flushed viewport missing streamed text: %q", got)
	}
	_ = viewString(m)
	if m.paint.suppress {
		t.Fatal("paint still suppressed after flush rebuild")
	}
}

func TestImmediateFlushBypassesFPSCap(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	now := time.Unix(1_700_000_000, 0)
	m = withPaintClock(m, &now)
	m = updateApp(t, m, engineEventMsg{ev: protocol.TurnStarted{}})
	_ = viewString(m)

	for i := 0; i < 20; i++ {
		m = updateApp(t, m, engineEventMsg{ev: protocol.TextDelta{Text: "a"}})
		_ = viewString(m)
	}
	before := frameBuilds(m)

	// Tool lifecycle must paint on the next Update cycle (no multi-second stall).
	m = updateApp(t, m, engineEventMsg{ev: protocol.ToolCallBegin{
		CallID: "c1", Name: "bash", Args: []byte(`{"command":"echo hi"}`),
	}})
	_ = viewString(m)
	if frameBuilds(m) <= before {
		t.Fatal("ToolCallBegin did not force an immediate full-frame paint")
	}
	if m.paint.suppress {
		t.Fatal("ToolCallBegin left paint suppressed")
	}
	if plain := ansi.Strip(m.viewport.View()); !strings.Contains(plain, "bash") {
		t.Fatalf("immediate tool paint missing bash cell: %q", plain)
	}

	before = frameBuilds(m)
	m = updateApp(t, m, engineEventMsg{ev: protocol.TurnCompleted{StopReason: "end_turn"}})
	_ = viewString(m)
	if frameBuilds(m) <= before {
		t.Fatal("TurnCompleted did not force an immediate full-frame paint")
	}

	// User key input also flushes immediately.
	m = updateApp(t, m, engineEventMsg{ev: protocol.TurnStarted{}})
	for i := 0; i < 10; i++ {
		m = updateApp(t, m, engineEventMsg{ev: protocol.TextDelta{Text: "b"}})
		_ = viewString(m)
	}
	before = frameBuilds(m)
	m = typeAppText(t, m, "hi")
	_ = viewString(m)
	if frameBuilds(m) <= before {
		t.Fatal("key input did not force an immediate full-frame paint")
	}
}

func TestPermissionAskedImmediateFlush(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	now := time.Unix(1_700_000_000, 0)
	m = withPaintClock(m, &now)
	m = updateApp(t, m, engineEventMsg{ev: protocol.TurnStarted{}})
	for i := 0; i < 15; i++ {
		m = updateApp(t, m, engineEventMsg{ev: protocol.TextDelta{Text: "p"}})
		_ = viewString(m)
	}
	before := frameBuilds(m)
	m = updateApp(t, m, engineEventMsg{ev: protocol.PermissionAsked{
		RequestID:  "r1",
		Permission: "bash",
		Patterns:   []string{"bash"},
	}})
	view := viewString(m)
	if frameBuilds(m) <= before {
		t.Fatal("PermissionAsked did not force immediate paint")
	}
	if m.modal == nil {
		t.Fatal("PermissionAsked did not open modal")
	}
	_ = view
}

func TestWorkingSpinnerTicksAreFPSCapped(t *testing.T) {
	if staticWorkingChrome() {
		t.Skip("static working chrome disables spinner ticks (#497)")
	}
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	now := time.Unix(1_700_000_000, 0)
	m = withPaintClock(m, &now)
	m.turnRunning = true
	_ = viewString(m)
	base := frameBuilds(m)

	for i := 0; i < 30; i++ {
		updated, cmd := m.Update(m.spin.Tick())
		m = updated.(Model)
		_ = cmd
		_ = viewString(m)
	}
	builds := frameBuilds(m) - base
	if builds > 3 {
		t.Fatalf("working spinner frame builds = %d over 30 ticks, want FPS-capped (<=3)", builds)
	}
}

func TestPaintFlushAfterIntervalRebuilds(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	now := time.Unix(1_700_000_000, 0)
	m = withPaintClock(m, &now)
	m = updateApp(t, m, engineEventMsg{ev: protocol.TurnStarted{}})
	_ = viewString(m)

	for i := 0; i < 10; i++ {
		m = updateApp(t, m, engineEventMsg{ev: protocol.TextDelta{Text: "z"}})
		_ = viewString(m)
	}
	if !m.paint.pending && !m.paint.suppress {
		t.Fatal("expected pending/suppressed soft paints before flush")
	}
	before := frameBuilds(m)
	now = now.Add(paintFPSInterval + time.Millisecond)
	m = updateApp(t, m, paintFlushMsg{})
	_ = viewString(m)
	if frameBuilds(m) <= before {
		t.Fatal("paintFlushMsg did not rebuild a full frame")
	}
	if m.paint.suppress {
		t.Fatal("paint still suppressed after flush")
	}
}

func TestSoftCoalesceEventClassification(t *testing.T) {
	if !softCoalesceEvent(protocol.TextDelta{Text: "x"}) {
		t.Fatal("TextDelta should soft-coalesce")
	}
	if !softCoalesceEvent(protocol.ReasoningDelta{Text: "y"}) {
		t.Fatal("ReasoningDelta should soft-coalesce")
	}
	for _, ev := range []protocol.Event{
		protocol.ToolCallBegin{CallID: "1", Name: "bash"},
		protocol.ToolCallEnd{CallID: "1"},
		protocol.TurnCompleted{},
		protocol.EngineError{Message: "x"},
		protocol.PermissionAsked{RequestID: "r"},
		protocol.PermissionResolved{RequestID: "r"},
		protocol.QuestionAsked{RequestID: "q"},
		protocol.QuestionResolved{RequestID: "q"},
		protocol.TurnStarted{},
		protocol.UserMessage{Text: "u"},
	} {
		if softCoalesceEvent(ev) {
			t.Fatalf("%T must immediate-flush", ev)
		}
	}
	if !softCoalesceMsg(spinner.TickMsg{}) {
		t.Fatal("spinner.TickMsg should soft-coalesce")
	}
	if softCoalesceMsg(tea.KeyPressMsg{Text: "a"}) {
		t.Fatal("key msg must immediate-flush")
	}
}
