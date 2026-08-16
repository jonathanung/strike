package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// Redraw/byte budget guards for the TUI performance epic (#452).
// Prefer counters over wall-clock/SSH FPS tests so CI stays deterministic.
// Full-frame paints are FPS-capped by #496; refreshViewport still runs per
// TextDelta until a later coalesce of that path.

// streamRefreshPerDelta is one refreshViewport per TextDelta Update (model
// apply path). Full Canvas rebuilds are separately FPS-capped (#496).
const streamRefreshPerDelta = 1

// streamRenderFrameCap is the max full renderFrame builds for N TextDeltas
// delivered in one FPS window (plus a small slack for TurnStarted priming).
const streamRenderFrameCap = 4

// idleViewByteSlack is how much View payload may grow across idle pumps
// (spinner glyph / clock noise). Zero growth is ideal after #482.
const idleViewByteSlack = 64

func readyBudgetModel(t *testing.T) Model {
	t.Helper()
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 32})
	if !m.ready {
		t.Fatal("model not ready after WindowSizeMsg")
	}
	if m.paint == nil {
		t.Fatal("paint budget counter not allocated")
	}
	m.paint.reset()
	return m
}

func TestIdleRedrawBudget(t *testing.T) {
	// After ready + welcome: no spinner tick arm; idle ticks must not re-arm
	// a chain that would force full-frame redraws over SSH (#481/#482).
	m := readyBudgetModel(t)
	if m.agentState() != theme.AgentStateReady {
		t.Fatalf("agentState = %v, want Ready", m.agentState())
	}
	if cmd := m.spinTickCmd(); cmd != nil {
		t.Fatal("spinTickCmd must be nil while Ready (idle budget)")
	}

	// Pump stray spinner ticks: none may re-arm.
	for i := 0; i < 8; i++ {
		updated, cmd := m.Update(spinner.TickMsg{})
		m = updated.(Model)
		if cmd != nil {
			t.Fatalf("idle spinner.TickMsg #%d re-armed tick chain", i)
		}
	}

	// Timer-driven idle path must not refresh the transcript on its own.
	if got := m.paint.refreshViewportCalls; got != 0 {
		t.Fatalf("idle spinner pump refreshViewport = %d, budget 0", got)
	}

	// Explicit View is allowed; payload should stay stable across idle Views.
	_ = viewString(m)
	firstBytes := m.paint.lastViewBytes
	if firstBytes <= 0 {
		t.Fatal("View produced empty frame")
	}
	viewsBefore := m.paint.viewCalls
	for i := 0; i < 5; i++ {
		_ = viewString(m)
	}
	if got := m.paint.viewCalls - viewsBefore; got != 5 {
		t.Fatalf("viewCalls delta = %d, want 5", got)
	}
	lastBytes := m.paint.lastViewBytes
	if lastBytes > firstBytes+idleViewByteSlack {
		t.Fatalf("idle View bytes grew %d → %d (slack %d)", firstBytes, lastBytes, idleViewByteSlack)
	}
	if got := m.paint.refreshViewportCalls; got != 0 {
		t.Fatalf("View-only idle path refreshViewport = %d, budget 0", got)
	}
}

func TestStreamTextDeltaRefreshBudget(t *testing.T) {
	// N TextDeltas through Update must not refresh more than once each
	// (double-refresh regression). Failures print actual vs budget.
	const n = 40
	m := readyBudgetModel(t)
	m = updateApp(t, m, engineEventMsg{ev: protocol.TurnStarted{}})
	m.paint.reset()

	for i := 0; i < n; i++ {
		m = updateApp(t, m, engineEventMsg{ev: protocol.TextDelta{Text: "x"}})
		// Simulate Bubble Tea painting after each Update (worst case paint rate).
		_ = viewString(m)
	}

	refreshBudget := n * streamRefreshPerDelta
	if got := m.paint.refreshViewportCalls; got == 0 {
		t.Fatal("refreshViewport = 0, want ≥ 1 (stream produced no transcript refresh)")
	}
	if got := m.paint.refreshViewportCalls; got > refreshBudget {
		t.Fatalf("refreshViewport = %d, budget ≤ %d (N=%d × %d)", got, refreshBudget, n, streamRefreshPerDelta)
	}
	viewBudget := n // this test calls View once per delta (including suppressed)
	if got := m.paint.viewCalls; got > viewBudget {
		t.Fatalf("viewCalls = %d, budget ≤ %d (N=%d)", got, viewBudget, n)
	}
	// #496: full Canvas rebuilds are O(FPS), not O(N), within one paint window.
	if got := m.paint.renderFrameCalls; got == 0 {
		t.Fatal("renderFrameCalls = 0, want ≥ 1")
	}
	if got := m.paint.renderFrameCalls; got > streamRenderFrameCap {
		t.Fatalf("renderFrameCalls = %d over %d deltas, budget ≤ %d (FPS coalesce)", got, n, streamRenderFrameCap)
	}
	if m.paint.lastViewBytes <= 0 {
		t.Fatal("stream View produced empty frame")
	}
	// Cell renders must not explode beyond cells × refreshes.
	cells := len(m.displayCells())
	if cells < 1 {
		t.Fatal("expected streaming assistant cell")
	}
	cellBudget := cells * m.paint.refreshViewportCalls
	if got := m.paint.renderCellCalls; got > cellBudget {
		t.Fatalf("renderCellCalls = %d, budget ≤ %d (cells=%d × refreshes=%d)",
			got, cellBudget, cells, m.paint.refreshViewportCalls)
	}
}

func TestViewportCompletedCellMarkdownBudget(t *testing.T) {
	// Completed historical assistant cells must stay on mdCache after the first
	// paint; tail TextDeltas must not force markdown re-parse on them.
	// Full per-cell skip is #493; this guards the existing mdCache path.
	m := readyBudgetModel(t)
	m = updateApp(t, m, engineEventMsg{ev: protocol.UserMessage{Text: "hi"}})
	m = updateApp(t, m, engineEventMsg{ev: protocol.TurnStarted{}})
	m = updateApp(t, m, engineEventMsg{ev: protocol.TextDelta{Text: "# Done\n\n**bold** history"}})
	m = updateApp(t, m, engineEventMsg{ev: protocol.ToolCallBegin{CallID: "c1", Name: "bash", Args: []byte(`{"command":"true"}`)}})
	m = updateApp(t, m, engineEventMsg{ev: protocol.ToolCallEnd{CallID: "c1", Title: "bash", Output: "ok"}})

	assts := assistantCellsOf(m.cells)
	if len(assts) != 1 {
		t.Fatalf("assistant cells = %d, want 1 completed before tail stream", len(assts))
	}
	hist := assts[0]
	if !hist.complete {
		t.Fatal("historical assistant not complete after ToolCallBegin")
	}
	// Prime markdown cache via viewport refresh (same path as live paints).
	m.refreshViewport()
	if !hist.mdCacheOK {
		t.Fatal("mdCacheOK false after prime refresh")
	}
	missesAfterPrime := hist.mdMisses
	if missesAfterPrime < 1 {
		t.Fatalf("mdMisses after prime = %d, want ≥ 1", missesAfterPrime)
	}

	m.paint.reset()
	const tailN = 25
	for i := 0; i < tailN; i++ {
		m = updateApp(t, m, engineEventMsg{ev: protocol.TextDelta{Text: "t"}})
	}
	// One more explicit refresh + View to ensure render path ran.
	m.refreshViewport()
	_ = viewString(m)

	if hist.mdMisses != missesAfterPrime {
		t.Fatalf("historical assistant mdMisses = %d, budget %d (tail stream re-parsed completed markdown)",
			hist.mdMisses, missesAfterPrime)
	}
	if !hist.mdCacheOK {
		t.Fatal("historical mdCacheOK cleared during tail stream")
	}
	assts = assistantCellsOf(m.cells)
	if len(assts) != 2 {
		t.Fatalf("assistant cells = %d, want 2 (history + streaming tail)", len(assts))
	}
}

func BenchmarkRefreshViewport(b *testing.B) {
	m := readyBudgetBench(b)
	// Fixed multi-cell fixture: completed history + streaming tail.
	m.cells = []cell{
		&userCell{text: "prompt"},
		&assistantCell{text: strings.Repeat("history line\n", 20), complete: true},
		&toolCell{callID: "c1", name: "bash", title: "bash", output: "ok", done: true},
		&assistantCell{text: strings.Repeat("stream ", 40)},
	}
	m.paint.reset()
	m.refreshViewport() // prime mdCache / plain lines
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.refreshViewport()
	}
}

func BenchmarkRenderFrame(b *testing.B) {
	m := readyBudgetBench(b)
	m.cells = []cell{
		&userCell{text: "prompt"},
		&assistantCell{text: "# Title\n\nbody", complete: true},
		&assistantCell{text: "streaming tail"},
	}
	m.refreshViewport()
	m.paint.reset()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.renderFrame()
	}
}

func readyBudgetBench(b *testing.B) Model {
	b.Helper()
	m, _ := newAppTestModel(nil, nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	m = updated.(Model)
	if !m.ready {
		b.Fatal("model not ready after WindowSizeMsg")
	}
	if m.paint == nil {
		m.paint = &paintBudget{}
	}
	m.paint.reset()
	return m
}
