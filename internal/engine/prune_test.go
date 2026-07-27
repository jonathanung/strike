package engine

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/provider"
)

// bigOutput returns a string whose ~4-chars/token estimate is at least n tokens.
func bigOutput(tokens int) string {
	if tokens <= 0 {
		return ""
	}
	return strings.Repeat("x", tokens*4)
}

func toolMsg(id, name, out string) (assistant, result provider.Message) {
	assistant = provider.Message{
		Role:      provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{ID: id, Name: name, Args: []byte(`{}`)}},
	}
	result = provider.Message{
		Role:       provider.RoleTool,
		ToolResult: &provider.ToolResult{CallID: id, Output: out},
	}
	return assistant, result
}

// historyWithSizedTools builds three user turns so older tools are prune-eligible
// (OpenCode skips tool results while userTurns < 2 when walking backward):
//
//	u-old + oldN tools | u-mid + mid tools | u-recent + recentN tools
//
// Only the u-old batch can be blanked once protect budget is exceeded.
func historyWithSizedTools(oldOut, recentOut string, oldN, recentN int) []provider.Message {
	msgs := []provider.Message{{Role: provider.RoleUser, Text: "old-turn"}}
	for i := 0; i < oldN; i++ {
		id := "old-" + strings.Repeat("a", i+1)
		a, r := toolMsg(id, "bash", oldOut)
		msgs = append(msgs, a, r)
	}
	msgs = append(msgs, provider.Message{Role: provider.RoleUser, Text: "mid-turn"})
	aMid, rMid := toolMsg("mid-1", "bash", bigOutput(500))
	msgs = append(msgs, aMid, rMid)
	msgs = append(msgs, provider.Message{Role: provider.RoleUser, Text: "recent-turn"})
	for i := 0; i < recentN; i++ {
		id := "new-" + strings.Repeat("b", i+1)
		a, r := toolMsg(id, "bash", recentOut)
		msgs = append(msgs, a, r)
	}
	return msgs
}

func TestPruneToolResultsBlanksOldBeyondProtect(t *testing.T) {
	// Each old result ~25k tokens; two old results = 50k. After protect 40k,
	// second (older when walking back... wait we walk back so newest old first)
	// Walk: skip recent turn tools; then encounter old tools from end of old batch.
	// oldN=3 * 25k = 75k total eligible. Protect first 40k of those (newest),
	// prune the rest (~35k) which is > pruneMinimum 20k.
	const per = 25_000
	msgs := historyWithSizedTools(bigOutput(per), bigOutput(5_000), 3, 1)

	before := estimateTokens("", msgs)
	cleared, freed := pruneToolResults(msgs)
	if cleared == 0 {
		t.Fatalf("expected some results cleared, freed=%d", freed)
	}
	if freed <= pruneMinimum {
		t.Fatalf("tokensFreed=%d, want > pruneMinimum %d", freed, pruneMinimum)
	}
	if !historyToolPairsValid(msgs) {
		t.Fatal("tool pairs invalid after prune")
	}

	// Recent-turn tool output must stay intact.
	var recentOut string
	for _, m := range msgs {
		if m.Role == provider.RoleTool && m.ToolResult != nil && strings.HasPrefix(m.ToolResult.CallID, "new-") {
			recentOut = m.ToolResult.Output
		}
	}
	if recentOut != bigOutput(5_000) {
		t.Fatalf("recent tool result was modified: len=%d", len(recentOut))
	}

	// At least one old result cleared.
	clearedOld := 0
	intactOld := 0
	for _, m := range msgs {
		if m.Role != provider.RoleTool || m.ToolResult == nil {
			continue
		}
		if !strings.HasPrefix(m.ToolResult.CallID, "old-") {
			continue
		}
		if m.ToolResult.Output == prunedToolResultText {
			clearedOld++
		} else if m.ToolResult.Output == bigOutput(per) {
			intactOld++
		}
	}
	if clearedOld == 0 {
		t.Fatal("no old tool results cleared")
	}
	if intactOld == 0 {
		t.Fatal("expected some old results inside protect budget to stay intact")
	}

	after := estimateTokens("", msgs)
	if after >= before {
		t.Fatalf("estimateTokens after=%d before=%d, want fewer", after, before)
	}
}

func TestPruneToolResultsProtectsRecentBudget(t *testing.T) {
	// Single large old result (~30k) under protect+minimum dynamics:
	// total eligible 30k <= protect 40k → nothing pruned.
	msgs := historyWithSizedTools(bigOutput(30_000), bigOutput(1_000), 1, 1)
	cleared, freed := pruneToolResults(msgs)
	if cleared != 0 || freed != 0 {
		t.Fatalf("cleared=%d freed=%d, want no-op under protect budget", cleared, freed)
	}
	for _, m := range msgs {
		if m.Role == provider.RoleTool && m.ToolResult != nil {
			if m.ToolResult.Output == prunedToolResultText {
				t.Fatal("unexpected clear under protect budget")
			}
		}
	}
}

func TestPruneToolResultsNoopBelowMinimum(t *testing.T) {
	// Once past protect, the whole tool body is a candidate (not a partial slice).
	// 35k (protected) + 15k (candidate) → freed 15k <= pruneMinimum 20k → no-op.
	msgs := []provider.Message{{Role: provider.RoleUser, Text: "old-turn"}}
	a1, r1 := toolMsg("old-a", "bash", bigOutput(15_000))
	a2, r2 := toolMsg("old-aa", "bash", bigOutput(35_000))
	msgs = append(msgs, a1, r1, a2, r2)
	msgs = append(msgs,
		provider.Message{Role: provider.RoleUser, Text: "mid-turn"},
	)
	am, rm := toolMsg("mid-1", "bash", bigOutput(100))
	msgs = append(msgs, am, rm,
		provider.Message{Role: provider.RoleUser, Text: "recent-turn"},
	)
	an, rn := toolMsg("new-b", "bash", bigOutput(100))
	msgs = append(msgs, an, rn)

	cleared, freed := pruneToolResults(msgs)
	if cleared != 0 || freed != 0 {
		t.Fatalf("cleared=%d freed=%d, want no-op when freed <= pruneMinimum", cleared, freed)
	}
	for _, m := range msgs {
		if m.Role == provider.RoleTool && m.ToolResult != nil && m.ToolResult.Output == prunedToolResultText {
			t.Fatal("cleared despite below PRUNE_MINIMUM")
		}
	}
}

func TestPruneToolResultsKeepsPairsValid(t *testing.T) {
	const per = 30_000
	msgs := historyWithSizedTools(bigOutput(per), bigOutput(2_000), 4, 2)
	if !historyToolPairsValid(msgs) {
		t.Fatal("setup invalid")
	}
	cleared, _ := pruneToolResults(msgs)
	if cleared == 0 {
		t.Fatal("expected clears")
	}
	if !historyToolPairsValid(msgs) {
		t.Fatalf("pairs broken after prune: %#v", msgs)
	}
	// Tool call structure preserved: every RoleTool still has CallID and a
	// matching assistant ToolCall; only Output text may change.
	for _, m := range msgs {
		if m.Role != provider.RoleTool || m.ToolResult == nil {
			continue
		}
		if m.ToolResult.CallID == "" {
			t.Fatal("empty CallID after prune")
		}
	}
}

func TestPruneToolResultsSkipsAlreadyCleared(t *testing.T) {
	const per = 30_000
	msgs := historyWithSizedTools(bigOutput(per), bigOutput(1_000), 4, 1)
	// Pre-clear the oldest eligible result and ensure walk stops at boundary
	// only after seeing pruned text going backward through old batch.
	// Manually mark one middle old result as already cleared.
	for i := range msgs {
		if msgs[i].Role == provider.RoleTool && msgs[i].ToolResult != nil &&
			msgs[i].ToolResult.CallID == "old-aa" {
			msgs[i].ToolResult.Output = prunedToolResultText
			break
		}
	}
	// First prune pass on remaining should still work or stop at boundary.
	_, _ = pruneToolResults(msgs)
	// Second pass: everything already at boundary / under minimum → stable.
	cleared2, freed2 := pruneToolResults(msgs)
	if cleared2 != 0 {
		t.Fatalf("second pass cleared=%d freed=%d, want stable no-op", cleared2, freed2)
	}
	if !historyToolPairsValid(msgs) {
		t.Fatal("pairs invalid")
	}
}

func TestPruneToolResultsProtectsSkillTool(t *testing.T) {
	// Large skill output must not be blanked even when far past protect budget.
	// Need 3 user turns so the oldest batch is eligible (turns >= 2).
	skillOut := bigOutput(50_000)
	bashOut := bigOutput(30_000)
	msgs := []provider.Message{{Role: provider.RoleUser, Text: "u1"}}
	aSkill, rSkill := toolMsg("sk1", "skill", skillOut)
	aBash1, rBash1 := toolMsg("b1", "bash", bashOut)
	aBash2, rBash2 := toolMsg("b2", "bash", bashOut)
	aBash3, rBash3 := toolMsg("b3", "bash", bashOut)
	msgs = append(msgs, aSkill, rSkill, aBash1, rBash1, aBash2, rBash2, aBash3, rBash3)
	msgs = append(msgs, provider.Message{Role: provider.RoleUser, Text: "u2"})
	aMid, rMid := toolMsg("bm", "bash", bigOutput(100))
	msgs = append(msgs, aMid, rMid)
	msgs = append(msgs, provider.Message{Role: provider.RoleUser, Text: "u3"})
	aRecent, rRecent := toolMsg("br", "bash", bigOutput(100))
	msgs = append(msgs, aRecent, rRecent)

	cleared, _ := pruneToolResults(msgs)
	if cleared == 0 {
		t.Fatal("expected bash results to clear")
	}
	for _, m := range msgs {
		if m.Role != provider.RoleTool || m.ToolResult == nil {
			continue
		}
		if m.ToolResult.CallID == "sk1" && m.ToolResult.Output != skillOut {
			t.Fatalf("skill tool result was pruned: %q", truncateForTest(m.ToolResult.Output, 40))
		}
	}
}

func TestPruneToolResultsEmptyNoop(t *testing.T) {
	cleared, freed := pruneToolResults(nil)
	if cleared != 0 || freed != 0 {
		t.Fatalf("nil: cleared=%d freed=%d", cleared, freed)
	}
	cleared, freed = pruneToolResults([]provider.Message{
		{Role: provider.RoleUser, Text: "hi"},
		{Role: provider.RoleAssistant, Text: "yo"},
	})
	if cleared != 0 || freed != 0 {
		t.Fatalf("no tools: cleared=%d freed=%d", cleared, freed)
	}
}

func TestMaybePruneToolResultsResetsOccupancy(t *testing.T) {
	e := &Engine{
		messages:      historyWithSizedTools(bigOutput(30_000), bigOutput(500), 4, 1),
		lastUsed:      99999,
		lastUsedKnown: true,
	}
	e.maybePruneToolResults()
	if e.lastUsedKnown || e.lastUsed != 0 {
		t.Fatalf("occupancy not reset: known=%v used=%d", e.lastUsedKnown, e.lastUsed)
	}
}

func TestEstimateToolOutputTokens(t *testing.T) {
	if estimateToolOutputTokens("") != 0 {
		t.Fatal("empty")
	}
	// 4 chars → 1 token with (n+3)/4
	if got := estimateToolOutputTokens("abcd"); got != 1 {
		t.Fatalf("got %d want 1", got)
	}
	if got := estimateToolOutputTokens(bigOutput(100)); got != 100 {
		t.Fatalf("got %d want 100", got)
	}
}

func truncateForTest(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
