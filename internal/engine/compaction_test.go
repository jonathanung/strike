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
