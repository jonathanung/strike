package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestVisualizerWindowUnknownUsageNotZero(t *testing.T) {
	w := newVisualizerWindow().resize(32, 20).(visualizerWindow)
	updated, _ := w.update(visualizerStateMsg{
		SessionID:   "sess-1",
		Label:       "main",
		Kind:        "root",
		State:       theme.AgentStateReady,
		StatusLabel: "ready",
	})
	plain := ansi.Strip(updated.view(theme.Default()))
	if strings.Contains(plain, "in 0") || strings.Contains(plain, "out 0") {
		t.Fatalf("unknown usage fabricated as zero:\n%s", plain)
	}
	if !strings.Contains(plain, "tokens") {
		t.Fatalf("missing tokens row:\n%s", plain)
	}
	// Detail separator stands in for unknown sides.
	dash := theme.Default().Resolve().Icons.DetailSeparator
	if !strings.Contains(plain, dash) {
		t.Fatalf("expected explicit unknown marker %q:\n%s", dash, plain)
	}
	if strings.Contains(plain, "$0") && !strings.Contains(plain, dash) {
		t.Fatalf("cost fabricated zero without unknown marker:\n%s", plain)
	}
}

func TestVisualizerWindowKnownUsageAndSparkline(t *testing.T) {
	w := newVisualizerWindow().resize(40, 24).(visualizerWindow)
	updated, _ := w.update(visualizerStateMsg{
		SessionID:         "sess-1",
		Label:             "build",
		Kind:              "root",
		State:             theme.AgentStateWorking,
		StatusLabel:       "working",
		Input:             protocol.KnownTokens(1200),
		Output:            protocol.KnownTokens(400),
		Used:              protocol.KnownTokens(1600),
		ContextLimit:      200_000,
		ContextLimitKnown: true,
		Source:            protocol.UsageSourceActual,
		CostUSD:           0.012,
		CostOK:            true,
		Activity:          []float64{100, 400, 200, 800},
		Tools: []visualizerTool{
			{Name: "read", Done: true},
			{Name: "bash", Done: false},
		},
	})
	plain := ansi.Strip(updated.view(theme.Default()))
	for _, want := range []string{
		"build", "working", "1.2k", "400", "1.6k",
		"tokens/turn", "4 turns", "peak 800", "last 800",
		"tools", "read", "bash",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q:\n%s", want, plain)
		}
	}
	// Opaque bare "activity" label is gone — metric must name units.
	if strings.Contains(plain, "activity") && !strings.Contains(plain, "tokens/turn") {
		t.Errorf("opaque activity label without metric:\n%s", plain)
	}
	if strings.Contains(plain, "select a session") {
		t.Errorf("should not show empty hint when node present:\n%s", plain)
	}
}

func TestVisualizerActivityMetricLabels(t *testing.T) {
	th := theme.Default()
	if got := visualizerActivityHeading(th, nil); got != "tokens/turn" {
		t.Errorf("empty heading = %q", got)
	}
	if got := visualizerActivityScale(th, nil); got != "" {
		t.Errorf("empty scale = %q, want blank", got)
	}
	if got, want := visualizerActivityHeading(th, []float64{10}), dotJoin(th, "tokens/turn", "1 turn"); got != want {
		t.Errorf("single heading = %q, want %q", got, want)
	}
	if got, want := visualizerActivityHeading(th, []float64{10, 20, 5}), dotJoin(th, "tokens/turn", "3 turns"); got != want {
		t.Errorf("multi heading = %q, want %q", got, want)
	}
	if got, want := visualizerActivityScale(th, []float64{100, 400, 200}), dotJoin(th, "peak 400", "last 200"); got != want {
		t.Errorf("scale = %q, want %q", got, want)
	}

	// Empty samples: heading only, no fabricated peak/last.
	w := newVisualizerWindow().resize(32, 16).(visualizerWindow)
	updated, _ := w.update(visualizerStateMsg{
		SessionID: "s",
		Label:     "main",
		State:     theme.AgentStateReady,
	})
	plain := ansi.Strip(updated.view(theme.Default()))
	if !strings.Contains(plain, "tokens/turn") {
		t.Fatalf("missing metric name:\n%s", plain)
	}
	if strings.Contains(plain, "peak") || strings.Contains(plain, "last") {
		t.Fatalf("empty samples should not show scale:\n%s", plain)
	}
}

func TestVisualizerWindowWidthSafe(t *testing.T) {
	w := newVisualizerWindow()
	msg := visualizerStateMsg{
		SessionID:    "s",
		Label:        "node-with-a-very-long-label-that-must-truncate",
		Kind:         "child",
		State:        theme.AgentStateAttention,
		StatusLabel:  "needs you",
		Objective:    "investigate a very long objective that must not blow the layout at narrow widths",
		LastAction:   "grep for something-with-an-extremely-long-pattern-name",
		BlockReason:  "waiting on permission for a long shell command that exceeds the pane",
		FilesTouched: []string{"internal/tui/_src/window/visualizer_window.go", "pkg/protocol/protocol.go", "a/b/c/d/e/f/g/h/i/j/k/long.go"},
		Input:        protocol.KnownTokens(99),
		Output:       protocol.KnownTokens(1),
		Activity:     []float64{1, 2, 3, 4, 5, 6, 7, 8},
		Tools:        []visualizerTool{{Name: "tool-with-long-name", Done: true, IsError: true}},
	}
	for _, width := range []int{8, 16, 24, 40, 80} {
		updated, _ := w.resize(width, 24).update(msg)
		view := updated.view(theme.Default())
		for i, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("width %d line %d width %d: %q", width, i, got, ansi.Strip(line))
			}
		}
	}
}

func TestVisualizerChildDetailFields(t *testing.T) {
	w := newVisualizerWindow().resize(40, 24).(visualizerWindow)
	updated, _ := w.update(visualizerStateMsg{
		SessionID:   "child-1",
		Label:       "explore",
		Kind:        "child",
		State:       theme.AgentStateWorking,
		StatusLabel: "working",
		Objective:   "map auth flow",
		LastAction:  "read config.go",
		FilesTouched: []string{
			"internal/auth/store.go",
			"internal/auth/oauth.go",
			"cmd/strike/main.go",
			"docs/auth.md",
			"pkg/protocol/protocol.go",
			"extra/overflow.go",
		},
	})
	plain := ansi.Strip(updated.view(theme.Default()))
	for _, want := range []string{
		"objective", "map auth flow",
		"action", "read config.go",
		"files (6)", "internal/auth/store.go", "+1 more",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q:\n%s", want, plain)
		}
	}
	// Overflow path must not appear once bounded to visualizerMaxFilesShown.
	if strings.Contains(plain, "extra/overflow.go") {
		t.Errorf("unbounded file list leaked overflow path:\n%s", plain)
	}
	// No fabricated block row when not blocked and reason empty.
	if strings.Contains(plain, "blocked") {
		t.Errorf("unexpected blocked row:\n%s", plain)
	}
}

func TestVisualizerBlockReasonAndEmptyPlaceholders(t *testing.T) {
	w := newVisualizerWindow().resize(36, 20).(visualizerWindow)
	// Needs-attention with reason.
	updated, _ := w.update(visualizerStateMsg{
		SessionID:   "c1",
		Label:       "build",
		Kind:        "child",
		State:       theme.AgentStateAttention,
		StatusLabel: "needs you",
		BlockReason: "permission: bash",
	})
	plain := ansi.Strip(updated.view(theme.Default()))
	if !strings.Contains(plain, "blocked") || !strings.Contains(plain, "permission: bash") {
		t.Fatalf("block reason missing:\n%s", plain)
	}
	// Child with empty objective/action shows muted unknown marker, not fake text.
	dash := theme.Default().Resolve().Icons.DetailSeparator
	updated, _ = w.update(visualizerStateMsg{
		SessionID:   "c2",
		Label:       "scout",
		Kind:        "child",
		State:       theme.AgentStateWorking,
		StatusLabel: "working",
	})
	plain = ansi.Strip(updated.view(theme.Default()))
	if !strings.Contains(plain, "objective") || !strings.Contains(plain, "action") {
		t.Fatalf("child should show objective/action rows:\n%s", plain)
	}
	if !strings.Contains(plain, dash) {
		t.Fatalf("empty child detail should use unknown marker %q:\n%s", dash, plain)
	}
	// Objective/action values must stay the unknown marker — not invented copy.
	if strings.Contains(plain, "unknown objective") || strings.Contains(plain, "TODO") {
		t.Errorf("fabricated detail copy:\n%s", plain)
	}
	// Ensure the objective row value is only the dash (not a prose placeholder).
	for _, line := range strings.Split(plain, "\n") {
		if !strings.Contains(line, "objective") {
			continue
		}
		if strings.Contains(line, "n/a") || strings.Contains(line, "none") {
			t.Errorf("fabricated objective placeholder on %q", line)
		}
	}
	// Root omits empty detail rows (tokens stay primary).
	updated, _ = w.update(visualizerStateMsg{
		SessionID:   "root",
		Label:       "main",
		Kind:        "root",
		State:       theme.AgentStateReady,
		StatusLabel: "ready",
	})
	plain = ansi.Strip(updated.view(theme.Default()))
	if strings.Contains(plain, "objective") || strings.Contains(plain, "action") {
		t.Fatalf("root should omit empty detail rows:\n%s", plain)
	}
	// Files section omitted when unknown — never a fake path list.
	if strings.Contains(plain, "files") {
		t.Fatalf("root should omit empty files section:\n%s", plain)
	}
}

func TestVisualizerFailedChildOmitsEmptyBlockRow(t *testing.T) {
	// Failed/error without blockReason must not look "blocked".
	w := newVisualizerWindow().resize(32, 16).(visualizerWindow)
	updated, _ := w.update(visualizerStateMsg{
		SessionID:   "c",
		Label:       "x",
		Kind:        "child",
		State:       theme.AgentStateError,
		StatusLabel: "failed",
	})
	plain := ansi.Strip(updated.view(theme.Default()))
	if strings.Contains(plain, "blocked") {
		t.Fatalf("failed child should not show empty blocked row:\n%s", plain)
	}
	// Explicit reason still surfaces even on failed.
	updated, _ = w.update(visualizerStateMsg{
		SessionID:   "c",
		Label:       "x",
		Kind:        "child",
		State:       theme.AgentStateError,
		StatusLabel: "failed",
		BlockReason: "verifier rejected",
	})
	plain = ansi.Strip(updated.view(theme.Default()))
	if !strings.Contains(plain, "blocked") || !strings.Contains(plain, "verifier rejected") {
		t.Fatalf("explicit blockReason missing on failed child:\n%s", plain)
	}
}

func TestVisualizerLastActionFallsBackToTool(t *testing.T) {
	w := newVisualizerWindow().resize(32, 16).(visualizerWindow)
	updated, _ := w.update(visualizerStateMsg{
		SessionID: "c",
		Label:     "x",
		Kind:      "child",
		State:     theme.AgentStateWorking,
		Tools: []visualizerTool{
			{Name: "bash", Done: false},
			{Name: "read", Done: true},
		},
	})
	plain := ansi.Strip(updated.view(theme.Default()))
	if !strings.Contains(plain, "action") || !strings.Contains(plain, "bash") {
		t.Fatalf("expected in-flight tool as action hint:\n%s", plain)
	}
}

func TestVisualizerDetailAtGalleryWidths(t *testing.T) {
	// ~80×24 and narrow panes used by the gallery matrix.
	msg := visualizerStateMsg{
		SessionID:    "child-g",
		Label:        "implementer",
		Kind:         "child",
		State:        theme.AgentStateAttention,
		StatusLabel:  "needs you",
		Objective:    "fix flaky test",
		LastAction:   "edit foo_test.go",
		BlockReason:  "awaiting review",
		FilesTouched: []string{"foo_test.go", "foo.go"},
		Activity:     []float64{10, 20},
		Tools:        []visualizerTool{{Name: "edit", Done: true}},
	}
	for _, tc := range []struct {
		w, h int
	}{
		{8, 24},
		{16, 24},
		{24, 20},
		{32, 24},
		{80, 24},
	} {
		updated, _ := newVisualizerWindow().resize(tc.w, tc.h).update(msg)
		view := updated.view(theme.Default())
		plain := ansi.Strip(view)
		if plain == "" && tc.w > 0 {
			t.Errorf("%dx%d empty view", tc.w, tc.h)
		}
		for i, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > tc.w {
				t.Errorf("%dx%d line %d width %d: %q", tc.w, tc.h, i, got, ansi.Strip(line))
			}
		}
		if tc.w >= 24 {
			for _, want := range []string{"objective", "fix flaky", "blocked", "awaiting review", "files"} {
				if !strings.Contains(plain, want) {
					t.Errorf("%dx%d missing %q:\n%s", tc.w, tc.h, want, plain)
				}
			}
		}
	}
}

func TestVisualizerFollowsAgentsHighlightAndUsage(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.children = []childActivity{
		{sessionID: "child-1", agent: "explore", status: "running"},
	}
	m.windows = m.windows.resize(40, 24)
	m = updateApp(t, m, agentsHighlightMsg{sessionID: "child-1"})

	viz := mustVisualizer(t, m)
	plain := ansi.Strip(viz.view(theme.Default()))
	if !strings.Contains(plain, "child") && !strings.Contains(plain, "explore") {
		t.Fatalf("child highlight not shown:\n%s", plain)
	}
	// Child usage unknown — no fabricated zeros.
	if strings.Contains(plain, "in 0") || strings.Contains(plain, "out 0") {
		t.Fatalf("child unknown usage fabricated zero:\n%s", plain)
	}

	// Switch highlight to root and apply usage.
	m = updateApp(t, m, agentsHighlightMsg{sessionID: "root-a"})
	m.applyEvent(protocol.UsageReported{
		Input:  protocol.KnownTokens(50),
		Output: protocol.KnownTokens(10),
		Used:   protocol.KnownTokens(60),
		Source: protocol.UsageSourceActual,
	})
	m.windows, _ = m.windows.broadcast(m.visualizerStateSnapshot())
	viz = mustVisualizer(t, m)
	plain = ansi.Strip(viz.view(theme.Default()))
	if !strings.Contains(plain, "50") || !strings.Contains(plain, "10") {
		t.Fatalf("root usage missing after highlight switch:\n%s", plain)
	}
}

func TestVisualizerDetailUpdatesOnStateBroadcast(t *testing.T) {
	// Selecting different nodes pushes a new visualizerStateMsg; detail must
	// refresh without restart. (Roster→snapshot plumbing is #922 / VIZ.1.)
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.windows = m.windows.resize(48, 28)
	if reg, ok := m.windows.activate(visualizerWindowID); ok {
		m.windows = reg
	}
	m.windows, _ = m.windows.broadcast(visualizerStateMsg{
		SessionID:    "child-1",
		Label:        "scout",
		Kind:         "child",
		State:        theme.AgentStateAttention,
		StatusLabel:  "needs you",
		Objective:    "trace login",
		LastAction:   "grep Session",
		BlockReason:  "needs you: confirm scope",
		FilesTouched: []string{"auth.go", "session.go"},
	})
	plain := ansi.Strip(mustVisualizer(t, m).view(theme.Default()))
	for _, want := range []string{
		"objective", "trace login",
		"action", "grep Session",
		"blocked", "needs you: confirm scope",
		"files", "auth.go", "session.go",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q:\n%s", want, plain)
		}
	}

	m.windows, _ = m.windows.broadcast(visualizerStateMsg{
		SessionID:   "child-2",
		Label:       "builder",
		Kind:        "child",
		State:       theme.AgentStateWorking,
		StatusLabel: "working",
		Objective:   "ship patch",
		LastAction:  "edit app.go",
	})
	plain = ansi.Strip(mustVisualizer(t, m).view(theme.Default()))
	if !strings.Contains(plain, "ship patch") || !strings.Contains(plain, "edit app.go") {
		t.Fatalf("child-2 detail missing after reselect:\n%s", plain)
	}
	if strings.Contains(plain, "trace login") || strings.Contains(plain, "confirm scope") {
		t.Fatalf("stale child-1 detail after reselect:\n%s", plain)
	}
}

func TestVisualizerToolsUpdateOnToolCallMidTurn(t *testing.T) {
	// Tool activity must reach the visualizer strip when bash starts, not only
	// after TurnCompleted (#625). Snapshot is what broadcastVisualizerState pushes.
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updateApp(t, m, engineEventMsg{ev: protocol.TurnStarted{}})
	m = updateApp(t, m, engineEventMsg{ev: protocol.ToolCallBegin{
		CallID: "c1", Name: "bash", Args: []byte(`{"command":"ls"}`),
	}})
	if !m.turnRunning {
		t.Fatal("turn should still be running")
	}
	snap := m.visualizerStateSnapshot()
	if len(snap.Tools) == 0 {
		t.Fatal("visualizer snapshot has no tools after ToolCallBegin mid-turn")
	}
	found := false
	for _, tool := range snap.Tools {
		if tool.Name == "bash" || strings.Contains(tool.Name, "ls") {
			found = true
			if tool.Done {
				t.Error("in-flight bash marked done")
			}
		}
	}
	if !found {
		t.Fatalf("snapshot tools = %#v, want bash", snap.Tools)
	}
	// applyEvent returns broadcastVisualizerState on ToolCallBegin; push it
	// the same way the runtime window update path does.
	m.windows, _ = m.windows.broadcast(snap)
	viz := mustVisualizer(t, m).resize(40, 24).(visualizerWindow)
	plain := ansi.Strip(viz.view(theme.Default()))
	if !strings.Contains(plain, "bash") {
		t.Fatalf("visualizer view missing bash:\n%s", plain)
	}
}

func TestVisualizerSelectingNodesUpdatesWithoutRestart(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.roots = map[string]*rootPane{
		"root-b": {
			sessionID:   "root-b",
			titleTopic:  "other",
			usageInput:  protocol.KnownTokens(7),
			usageOutput: protocol.KnownTokens(3),
			usageUsed:   protocol.KnownTokens(10),
		},
	}
	m.windows = m.windows.resize(40, 24)
	// Highlight root-b directly from stash.
	m = updateApp(t, m, agentsHighlightMsg{sessionID: "root-b"})
	viz := mustVisualizer(t, m)
	plain := ansi.Strip(viz.view(theme.Default()))
	if !strings.Contains(plain, "7") || !strings.Contains(plain, "3") {
		t.Fatalf("stashed root stats missing:\n%s", plain)
	}

	m = updateApp(t, m, agentsHighlightMsg{sessionID: "root-a"})
	viz = mustVisualizer(t, m)
	plain = ansi.Strip(viz.view(theme.Default()))
	if strings.Contains(plain, "in 7") {
		t.Fatalf("still showing previous node after reselect:\n%s", plain)
	}
}

func mustVisualizer(t *testing.T, m Model) visualizerWindow {
	t.Helper()
	for _, w := range m.windows.windows {
		if v, ok := w.(visualizerWindow); ok {
			return v
		}
	}
	t.Fatal("visualizer window missing from registry")
	return visualizerWindow{}
}

func TestUsageActivitySamplesSkipUnknown(t *testing.T) {
	var totals usageTotals
	totals.add(protocol.UsageReported{Input: protocol.KnownTokens(10), Output: protocol.KnownTokens(5), Used: protocol.KnownTokens(15)})
	totals.add(protocol.UsageReported{})                              // fully unknown — skip
	totals.add(protocol.UsageReported{Used: protocol.KnownTokens(0)}) // measured zero — keep
	got := usageActivitySamples(totals)
	if len(got) != 2 || got[0] != 15 || got[1] != 0 {
		t.Fatalf("samples = %v, want [15 0]", got)
	}
}

func TestVisualizerInDefaultRegistry(t *testing.T) {
	r := newWindowRegistry()
	found := false
	for _, w := range r.windows {
		if w.id() == visualizerWindowID {
			found = true
			if _, ok := w.(visualizerWindow); !ok {
				t.Fatalf("visualizer id is %T", w)
			}
		}
	}
	if !found {
		t.Fatal("visualizer not in default registry")
	}
	next, ok := r.activate(visualizerWindowID)
	if !ok || next.active().id() != visualizerWindowID {
		t.Fatalf("activate visualizer failed ok=%v id=%q", ok, next.active().id())
	}
}
