package engine

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/provider"
)

func TestCompactMessagesKeepsRecentUserTurnsAndValidToolPairs(t *testing.T) {
	call := provider.ToolCall{ID: "c1", Name: "bash", Args: []byte(`{}`)}
	msgs := []provider.Message{
		{Role: provider.RoleUser, Text: "old-1"},
		{Role: provider.RoleAssistant, Text: "a1"},
		{Role: provider.RoleUser, Text: "old-2"},
		{Role: provider.RoleAssistant, Text: "a2", ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "c1", Output: "out"}},
		{Role: provider.RoleUser, Text: "recent"},
		{Role: provider.RoleAssistant, Text: "a3"},
	}
	out, removed, kept, ok := compactMessages(msgs, 2)
	if !ok {
		t.Fatal("compactMessages returned ok=false")
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	if kept != 6 { // marker + 5 tail msgs (from old-2)
		t.Fatalf("kept = %d, want 6", kept)
	}
	if out[0].Role != provider.RoleUser || !strings.HasPrefix(out[0].Text, compactMarkerPrefix) {
		t.Fatalf("marker = %#v", out[0])
	}
	if !strings.Contains(out[0].Text, "2 earlier") {
		t.Fatalf("marker text = %q", out[0].Text)
	}
	if out[1].Text != "old-2" {
		t.Fatalf("tail start = %#v, want old-2 user turn", out[1])
	}
	if !historyToolPairsValid(out) {
		t.Fatalf("compacted history has invalid tool pairs: %#v", out)
	}
	// Recent intent survives.
	foundRecent := false
	for _, m := range out {
		if m.Role == provider.RoleUser && m.Text == "recent" {
			foundRecent = true
		}
	}
	if !foundRecent {
		t.Fatalf("recent user message missing: %#v", out)
	}
}

func TestDropLastUserTurn(t *testing.T) {
	call := provider.ToolCall{ID: "c1", Name: "bash", Args: []byte(`{}`)}
	msgs := []provider.Message{
		{Role: provider.RoleUser, Text: "first"},
		{Role: provider.RoleAssistant, Text: "a1"},
		{Role: provider.RoleUser, Text: "second"},
		{Role: provider.RoleAssistant, Text: "a2", ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "c1", Output: "ok"}},
	}
	got, ok := dropLastUserTurn(msgs)
	if !ok {
		t.Fatal("expected drop")
	}
	want := []provider.Message{
		{Role: provider.RoleUser, Text: "first"},
		{Role: provider.RoleAssistant, Text: "a1"},
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Text != want[i].Text {
			t.Fatalf("got[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
	// Second drop removes the remaining turn.
	got, ok = dropLastUserTurn(got)
	if !ok || len(got) != 0 {
		t.Fatalf("second drop = %#v ok=%v", got, ok)
	}
	_, ok = dropLastUserTurn(nil)
	if ok {
		t.Fatal("empty should not drop")
	}
	// Compact marker alone is not a real turn.
	markerOnly := []provider.Message{
		{Role: provider.RoleUser, Text: compactMarker(3)},
		{Role: provider.RoleAssistant, Text: "tail"},
	}
	if _, ok := dropLastUserTurn(markerOnly); ok {
		t.Fatal("should not drop compact-marker-only history without a real user turn")
	}
}

func TestCompactMessagesNothingToDrop(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Text: "only"},
		{Role: provider.RoleAssistant, Text: "reply"},
	}
	_, _, _, ok := compactMessages(msgs, 2)
	if ok {
		t.Fatal("expected ok=false when history is already minimal")
	}
}

func TestHistoryToolPairsValid(t *testing.T) {
	valid := []provider.Message{
		{Role: provider.RoleUser, Text: "u"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "a", Name: "bash"}}},
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "a", Output: "ok"}},
	}
	if !historyToolPairsValid(valid) {
		t.Fatal("valid pairs rejected")
	}
	danglingCall := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "a", Name: "bash"}}},
	}
	if historyToolPairsValid(danglingCall) {
		t.Fatal("dangling tool call accepted")
	}
	orphanResult := []provider.Message{
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "missing", Output: "x"}},
	}
	if historyToolPairsValid(orphanResult) {
		t.Fatal("orphan tool result accepted")
	}
}

func TestEstimateTokensPositive(t *testing.T) {
	n := estimateTokens("system prompt here", []provider.Message{
		{Role: provider.RoleUser, Text: "hello world this is a longer message"},
	})
	if n <= 0 {
		t.Fatalf("estimate = %d, want > 0", n)
	}
}

func TestSummaryCompactMarkerSharesPrefix(t *testing.T) {
	m := summaryCompactMarker(3, "did X then Y")
	if !strings.HasPrefix(m, compactMarkerPrefix) {
		t.Fatalf("marker missing compact prefix: %q", m)
	}
	if !strings.Contains(m, "did X then Y") {
		t.Fatalf("summary missing: %q", m)
	}
	// findCompactSplit must skip summary markers as non-real user turns.
	msgs := []provider.Message{
		{Role: provider.RoleUser, Text: m},
		{Role: provider.RoleAssistant, Text: "ok"},
		{Role: provider.RoleUser, Text: "new"},
		{Role: provider.RoleAssistant, Text: "reply"},
	}
	// With keep=1, only "new" is a real turn — split drops marker+ok.
	split := findCompactSplit(msgs, 1)
	if split != 2 {
		t.Fatalf("split = %d, want 2", split)
	}
}

func TestFormatDroppedForSummaryBounded(t *testing.T) {
	call := provider.ToolCall{ID: "c1", Name: "bash", Args: []byte(`{"command":"echo hi"}`)}
	msgs := []provider.Message{
		{Role: provider.RoleUser, Text: "old"},
		{Role: provider.RoleAssistant, Text: "a", ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "c1", Output: "hi"}},
	}
	got := formatDroppedForSummary(msgs)
	if !strings.Contains(got, "User: old") {
		t.Fatalf("missing user: %q", got)
	}
	if !strings.Contains(got, "tool bash") {
		t.Fatalf("missing tool: %q", got)
	}
	if !strings.Contains(got, "Tool(c1)") {
		t.Fatalf("missing tool result: %q", got)
	}
}

func TestResolveCompactionStrategy(t *testing.T) {
	if got := resolveCompactionStrategy(""); got != "trim" {
		t.Fatalf("empty = %q", got)
	}
	if got := resolveCompactionStrategy("SUMMARIZE"); got != "summarize" {
		t.Fatalf("summarize = %q", got)
	}
	if got := resolveCompactionStrategy("nope"); got != "trim" {
		t.Fatalf("unknown = %q", got)
	}
}

func TestOverCompactionThreshold(t *testing.T) {
	// Window 1000, MaxTokens 10, buffer 1 → reserve 11, budget 989.
	// Default threshold 0.70 → limit 700. Custom 0.80 → limit 800.
	cases := []struct {
		name      string
		window    int
		threshold float64
		buffer    int
		maxTokens int
		used      int
		want      bool
	}{
		{name: "default fires at 70%", window: 1000, buffer: 1, maxTokens: 10, used: 700, want: true},
		{name: "default quiet below 70%", window: 1000, buffer: 1, maxTokens: 10, used: 699, want: false},
		{name: "legacy 0.80 still quiet at 700", window: 1000, threshold: 0.80, buffer: 1, maxTokens: 10, used: 700, want: false},
		{name: "legacy 0.80 fires at 800", window: 1000, threshold: 0.80, buffer: 1, maxTokens: 10, used: 800, want: true},
		{name: "custom 0.5 fires earlier", window: 1000, threshold: 0.5, buffer: 1, maxTokens: 10, used: 500, want: true},
		{name: "threshold >=1 disables", window: 1000, threshold: 1, buffer: 1, maxTokens: 10, used: 999, want: false},
		{name: "unknown window never fires", window: 0, threshold: 0.5, buffer: 1, maxTokens: 10, used: 900, want: false},
		// lastUsed must be >0 so occupancyTokens does not fall through to estimate.
		{name: "tiny occupancy quiet", window: 1000, threshold: 0.5, buffer: 1, maxTokens: 10, used: 1, want: false},
		// reserve = 400+200=600 → budget 400; limit = min(0.9*1000, 400) = 400
		{name: "buffer lowers effective limit", window: 1000, threshold: 0.9, buffer: 200, maxTokens: 400, used: 400, want: true},
		{name: "buffer limit not yet reached", window: 1000, threshold: 0.9, buffer: 200, maxTokens: 400, used: 399, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &Engine{
				opts: Options{
					ContextWindow:       tc.window,
					CompactionThreshold: tc.threshold,
					CompactionBuffer:    tc.buffer,
					MaxTokens:           tc.maxTokens,
				},
				lastUsed:      tc.used,
				lastUsedKnown: tc.used > 0,
			}
			if got := e.overCompactionThreshold(); got != tc.want {
				t.Fatalf("overCompactionThreshold() = %v, want %v (used=%d window=%d thr=%v buf=%d max=%d)",
					got, tc.want, tc.used, tc.window, tc.threshold, tc.buffer, tc.maxTokens)
			}
		})
	}
}

func TestDefaultCompactionThresholdConstant(t *testing.T) {
	if defaultCompactionThreshold != 0.70 {
		t.Fatalf("defaultCompactionThreshold = %v, want 0.70", defaultCompactionThreshold)
	}
}
