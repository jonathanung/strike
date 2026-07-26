package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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
	for _, want := range []string{"build", "working", "1.2k", "400", "1.6k", "activity", "tools", "read", "bash"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "select a session") {
		t.Errorf("should not show empty hint when node present:\n%s", plain)
	}
}

func TestVisualizerWindowWidthSafe(t *testing.T) {
	w := newVisualizerWindow()
	msg := visualizerStateMsg{
		SessionID: "s",
		Label:     "node-with-a-very-long-label-that-must-truncate",
		State:     theme.AgentStateReady,
		Input:     protocol.KnownTokens(99),
		Output:    protocol.KnownTokens(1),
		Activity:  []float64{1, 2, 3, 4, 5, 6, 7, 8},
		Tools:     []visualizerTool{{Name: "tool-with-long-name", Done: true, IsError: true}},
	}
	for _, width := range []int{8, 16, 24, 40} {
		updated, _ := w.resize(width, 20).update(msg)
		view := updated.view(theme.Default())
		for i, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("width %d line %d width %d: %q", width, i, got, ansi.Strip(line))
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
