package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestRefreshViewportIncrementalSkipsCompletedCells(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// Seed completed history: several user + finished assistant turns.
	for i := range 5 {
		m.applyEvent(protocol.UserMessage{Text: "user history " + strings.Repeat("x", 20) + string(rune('a'+i))})
		m.applyEvent(protocol.TurnStarted{})
		m.applyEvent(protocol.TextDelta{Text: "# done " + string(rune('a'+i)) + "\n\n**bold** paragraph"})
		m.applyEvent(protocol.TurnCompleted{StopReason: "end_turn"})
	}
	m.refreshViewport()
	if m.vpCache.cellRenders == 0 {
		t.Fatal("first refresh rendered 0 cells")
	}
	histCells := len(m.vpCache.items)
	if histCells < 10 {
		t.Fatalf("first refresh items=%d, want >= 10 (5 user + 5 assistant)", histCells)
	}

	// New user turn + streaming assistant. Warm cache includes the new user cell.
	m.applyEvent(protocol.UserMessage{Text: "follow-up question"})
	m.applyEvent(protocol.TurnStarted{})
	m.applyEvent(protocol.TextDelta{Text: "stream-1"})
	m.refreshViewport()
	// Second delta: only the open assistant cell is dirty.
	m.applyEvent(protocol.TextDelta{Text: " stream-2"})
	m.refreshViewport()
	if m.vpCache.cellRenders != 1 {
		t.Fatalf("after second delta: renders=%d, want 1 (streaming tail only); hits=%d",
			m.vpCache.cellRenders, m.vpCache.cellHits)
	}
	if m.vpCache.cellHits < histCells {
		t.Fatalf("after second delta: hits=%d want >= %d historical", m.vpCache.cellHits, histCells)
	}
	if !strings.Contains(ansi.Strip(m.viewport.View()), "stream-1 stream-2") {
		t.Fatalf("viewport missing streamed text: %q", ansi.Strip(m.viewport.View()))
	}
}

func TestRefreshViewportWidthChangeInvalidatesCache(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.applyEvent(protocol.UserMessage{Text: strings.Repeat("width-test-word ", 12)})
	m.applyEvent(protocol.TurnStarted{})
	m.applyEvent(protocol.TextDelta{Text: strings.Repeat("assistant-word ", 12)})
	m.applyEvent(protocol.TurnCompleted{StopReason: "end_turn"})
	m.refreshViewport()
	n := len(m.vpCache.items)
	if n == 0 {
		t.Fatal("empty cache after seed")
	}
	m.refreshViewport()
	if m.vpCache.cellRenders != 0 || m.vpCache.cellHits != n {
		t.Fatalf("same width: renders=%d hits=%d want 0/%d", m.vpCache.cellRenders, m.vpCache.cellHits, n)
	}

	m.viewport.SetWidth(40)
	m.refreshViewport()
	if m.vpCache.cellRenders != n || m.vpCache.cellHits != 0 {
		t.Fatalf("width change: renders=%d hits=%d want %d/0", m.vpCache.cellRenders, m.vpCache.cellHits, n)
	}
}

func TestRefreshViewportThemeChangeInvalidatesCache(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.applyEvent(protocol.UserMessage{Text: "theme user"})
	m.applyEvent(protocol.TextDelta{Text: "theme asst"})
	m.applyEvent(protocol.TurnCompleted{StopReason: "end_turn"})
	m.refreshViewport()
	n := len(m.vpCache.items)
	m.themeID = "other-theme-id"
	m.th = theme.Default().Resolve()
	m.refreshViewport()
	if m.vpCache.cellRenders != n || m.vpCache.cellHits != 0 {
		t.Fatalf("theme change: renders=%d hits=%d want %d/0", m.vpCache.cellRenders, m.vpCache.cellHits, n)
	}
}

func TestRefreshViewportAppearanceChangeInvalidatesCache(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.applyEvent(protocol.UserMessage{Text: "appearance user"})
	m.applyEvent(protocol.TextDelta{Text: "appearance asst"})
	m.applyEvent(protocol.TurnCompleted{StopReason: "end_turn"})
	m.appearance = appearanceDark
	m.refreshViewport()
	n := len(m.vpCache.items)
	if n == 0 {
		t.Fatal("empty cache after seed")
	}
	m.refreshViewport()
	if m.vpCache.cellRenders != 0 || m.vpCache.cellHits != n {
		t.Fatalf("same appearance: renders=%d hits=%d want 0/%d", m.vpCache.cellRenders, m.vpCache.cellHits, n)
	}
	m.appearance = appearanceLight
	m.refreshViewport()
	if m.vpCache.cellRenders != n || m.vpCache.cellHits != 0 {
		t.Fatalf("appearance change: renders=%d hits=%d want %d/0", m.vpCache.cellRenders, m.vpCache.cellHits, n)
	}
}

func TestRefreshViewportEffectiveDarkChangeInvalidatesCache(t *testing.T) {
	// auto + BackgroundColorMsg flips effectiveDark without changing appearance mode.
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.applyEvent(protocol.UserMessage{Text: "bg user"})
	m.applyEvent(protocol.TextDelta{Text: "bg asst"})
	m.applyEvent(protocol.TurnCompleted{StopReason: "end_turn"})
	m.appearance = appearanceAuto
	m.detectedDark = true
	m.refreshViewport()
	n := len(m.vpCache.items)
	if n == 0 {
		t.Fatal("empty cache after seed")
	}
	m.refreshViewport()
	if m.vpCache.cellRenders != 0 || m.vpCache.cellHits != n {
		t.Fatalf("same effective dark: renders=%d hits=%d want 0/%d", m.vpCache.cellRenders, m.vpCache.cellHits, n)
	}
	m.detectedDark = false
	m.refreshViewport()
	if m.vpCache.cellRenders != n || m.vpCache.cellHits != 0 {
		t.Fatalf("effective dark change: renders=%d hits=%d want %d/0", m.vpCache.cellRenders, m.vpCache.cellHits, n)
	}
}

func TestRefreshViewportThinkingToggleInvalidatesVisibility(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.applyEvent(protocol.UserMessage{Text: "ask"})
	m.applyEvent(protocol.TurnStarted{})
	m.applyEvent(protocol.ReasoningDelta{Text: "secret chain of thought"})
	m.applyEvent(protocol.TextDelta{Text: "answer"})
	m.applyEvent(protocol.TurnCompleted{StopReason: "end_turn"})
	m.showThinking = false
	m.refreshViewport()
	hidden := ansi.Strip(strings.Join(m.transcriptPlainLines, "\n"))
	if strings.Contains(hidden, "secret chain") {
		t.Fatal("reasoning visible while showThinking=false")
	}

	m.showThinking = true
	m.refreshViewport()
	shown := ansi.Strip(strings.Join(m.transcriptPlainLines, "\n"))
	if !strings.Contains(shown, "secret chain") {
		t.Fatal("reasoning missing after showThinking=true")
	}
	// User + assistant should hit; reasoning is a new visible cell (miss).
	if m.vpCache.cellHits < 2 {
		t.Fatalf("thinking on: hits=%d want >= 2 (user+assistant)", m.vpCache.cellHits)
	}
	if m.vpCache.cellRenders < 1 {
		t.Fatalf("thinking on: renders=%d want >= 1 (reasoning)", m.vpCache.cellRenders)
	}

	m.showThinking = false
	m.refreshViewport()
	hidden2 := ansi.Strip(strings.Join(m.transcriptPlainLines, "\n"))
	if strings.Contains(hidden2, "secret chain") {
		t.Fatal("reasoning still visible after toggle off")
	}
}

func TestRefreshViewportSelectionDirtiesToolCell(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.applyEvent(protocol.UserMessage{Text: "run it"})
	m.applyEvent(protocol.ToolCallBegin{CallID: "c1", Name: "bash"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "c1", Title: "echo", Output: "ok\nline2\nline3\nline4\nline5\nline6\nline7\n"})
	m.refreshViewport()
	n := len(m.vpCache.items)
	m.refreshViewport()
	if m.vpCache.cellRenders != 0 {
		t.Fatalf("stable: renders=%d want 0", m.vpCache.cellRenders)
	}

	m.selectedCell = 1 // tool cell index after user
	m.refreshViewport()
	if m.vpCache.cellRenders != 1 {
		t.Fatalf("selection: renders=%d want 1 (tool only), hits=%d items=%d",
			m.vpCache.cellRenders, m.vpCache.cellHits, n)
	}
	if m.vpCache.cellHits != n-1 {
		t.Fatalf("selection: hits=%d want %d", m.vpCache.cellHits, n-1)
	}
}

func TestRefreshViewportPlainLinesMatchFullStrip(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.workDir = t.TempDir()
	m.applyEvent(protocol.UserMessage{Text: "see foo.go:12 and bar/baz.go:3"})
	m.applyEvent(protocol.TextDelta{Text: "check internal/tui/app.go:99"})
	m.applyEvent(protocol.TurnCompleted{StopReason: "end_turn"})
	m.refreshViewport()
	// Second pass uses cache; plain lines must stay identical.
	want := append([]string(nil), m.transcriptPlainLines...)
	m.refreshViewport()
	got := m.transcriptPlainLines
	if len(got) != len(want) {
		t.Fatalf("plain line count %d vs %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("plain[%d]=%q want %q", i, got[i], want[i])
		}
	}
	// Match join of per-block plains (equivalent to full strip of joined raw).
	var plains []string
	for _, it := range m.vpCache.items {
		plains = append(plains, it.plain)
	}
	naive := joinBlockPlainLines(plains)
	if len(naive) != len(got) {
		t.Fatalf("naive plain len %d vs incremental %d", len(naive), len(got))
	}
	for i := range naive {
		if naive[i] != got[i] {
			t.Fatalf("naive plain[%d]=%q incremental=%q", i, naive[i], got[i])
		}
	}
}

func TestRefreshViewportPreservesScrollOnPartialRebuild(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := range 40 {
		m.applyEvent(protocol.UserMessage{Text: strings.Repeat("scroll ", 8) + string(rune('a'+i%26))})
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.viewport.AtBottom() {
		t.Fatal("setup: still at bottom")
	}
	off := m.viewport.YOffset()
	// Stream while scrolled up — offset must not jump to bottom.
	m.applyEvent(protocol.TurnStarted{})
	m.applyEvent(protocol.TextDelta{Text: "tail-only-change"})
	m.refreshViewport()
	if m.viewport.AtBottom() {
		t.Fatal("partial rebuild yanked to bottom")
	}
	if got := m.viewport.YOffset(); got < off-2 || got > off+2 {
		t.Fatalf("YOffset=%d want near %d", got, off)
	}
}

func TestJoinBlockPlainLines(t *testing.T) {
	got := joinBlockPlainLines([]string{"a\nb", "c", "d\ne\nf"})
	want := []string{"a", "b", "", "c", "", "d", "e", "f"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d]=%q want %q", i, got[i], want[i])
		}
	}
	if joinBlockPlainLines(nil) != nil {
		t.Fatal("nil input should return nil")
	}
}
