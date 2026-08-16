package tool

import (
	"fmt"
	"strings"
	"testing"
)

func TestShortPurposeBuiltins(t *testing.T) {
	for name, want := range shortPurposes {
		if got := ShortPurpose(name, "ignored long description"); got != want {
			t.Errorf("ShortPurpose(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestShortPurposeFallback(t *testing.T) {
	got := ShortPurpose("mcp_demo_echo", "[mcp:demo] Echo a message back to the caller. Extra detail.")
	if got != "echo a message back to the caller" {
		t.Fatalf("fallback = %q", got)
	}
}

func TestCompactSchemaDescriptionBuiltins(t *testing.T) {
	for name, want := range shortPurposes {
		if name == "skill" {
			continue
		}
		long := "Long multi-paragraph usage notes that should not appear on the wire. " + strings.Repeat("x", 200)
		if got := CompactSchemaDescription(name, long); got != want {
			t.Errorf("CompactSchemaDescription(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestCompactSchemaDescriptionSkillKeepsCatalog(t *testing.T) {
	desc := NewSkill([]SkillInfo{
		{Name: "write-go-tests", Description: "tests"},
		{Name: "test-and-validate", Description: "validate"},
	}).Description()
	got := CompactSchemaDescription("skill", desc)
	if !strings.Contains(got, "Available skills:") {
		t.Fatalf("missing skills catalog: %q", got)
	}
	if !strings.Contains(got, "write-go-tests") || !strings.Contains(got, "test-and-validate") {
		t.Fatalf("missing skill names: %q", got)
	}
	if len(got) >= len(desc) {
		t.Fatalf("skill compact not smaller: compact=%d full=%d", len(got), len(desc))
	}
	// Empty catalog path.
	empty := CompactSchemaDescription("skill", NewSkill(nil).Description())
	if !strings.Contains(empty, "Available skills:") || !strings.Contains(empty, "(none loaded)") {
		t.Fatalf("empty skill catalog: %q", empty)
	}
}

func TestCompactSchemaDescriptionMCPTruncates(t *testing.T) {
	long := "[mcp:demo] " + strings.Repeat("word ", 80) + "End sentence. More."
	got := CompactSchemaDescription("mcp_demo_bulk", long)
	if got == "" {
		t.Fatal("empty compact MCP description")
	}
	if len(got) >= len(long) {
		t.Fatalf("MCP compact not smaller: %d vs %d", len(got), len(long))
	}
	if strings.Contains(got, "More.") {
		t.Fatalf("kept second sentence: %q", got)
	}
}

func TestPermissionName(t *testing.T) {
	cases := map[string]string{
		"read":          "read",
		"edit":          "edit",
		"apply_patch":   "edit",
		"notebook_edit": "edit",
		"move":          "edit",
		"delete":        "edit",
		"mcp_foo_bar":   "mcp",
		"task":          "task",
	}
	for name, want := range cases {
		if got := PermissionName(name); got != want {
			t.Errorf("PermissionName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestBuildGuidanceAdditiveOnlyNoCatalog(t *testing.T) {
	text := BuildGuidance([]GuidanceEntry{
		{Name: "read"},
		{Name: "glob"},
		{Name: "bash"},
	})
	for _, want := range []string{
		"# Available tools",
		"provider Tools array",
		"additive only",
		"## Recommended use",
		"Prefer `read`/`glob`/`grep` over shelling out",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// Must not restate name — purpose catalog lines (schemas own those).
	for _, banned := range []string{
		"`read` —",
		"`glob` —",
		"`bash` —",
		"`write`",
		"`task`",
	} {
		if strings.Contains(text, banned) {
			t.Errorf("catalog-style or absent-tool mention %q in:\n%s", banned, text)
		}
	}
}

func TestBuildGuidanceOmitsMutationRecsWhenAbsent(t *testing.T) {
	text := BuildGuidance([]GuidanceEntry{
		{Name: "read"},
		{Name: "glob"},
		{Name: "grep"},
	})
	for _, banned := range []string{
		"`edit`", "`write`", "`apply_patch`", "`bash`", "multi-file",
	} {
		if strings.Contains(text, banned) {
			t.Errorf("read-only guidance contains %q:\n%s", banned, text)
		}
	}
	if !strings.Contains(text, "Use `read`/`glob`/`grep` for ordinary code exploration") {
		t.Fatalf("missing read-only explore guidance:\n%s", text)
	}
}

func TestBuildGuidanceOmitsTaskWhenAbsent(t *testing.T) {
	text := BuildGuidance([]GuidanceEntry{
		{Name: "read"},
		{Name: "sleep"},
	})
	if strings.Contains(text, "`task`") {
		t.Fatalf("task mentioned without registry entry:\n%s", text)
	}
}

func TestBuildGuidanceTaskStatusPreferred(t *testing.T) {
	text := BuildGuidance([]GuidanceEntry{
		{Name: "task"},
		{Name: "task_status"},
		{Name: "task_read"},
		{Name: "wait"},
		{Name: "sleep"},
	})
	if !strings.Contains(text, "task_status") || !strings.Contains(text, "task_read") {
		t.Fatalf("missing task_status/task_read guidance:\n%s", text)
	}
	if !strings.Contains(text, "busy-poll") && !strings.Contains(text, "Do not busy-poll") {
		t.Fatalf("task guidance should discourage busy-poll:\n%s", text)
	}
	if !strings.Contains(text, "progressive `task`") {
		t.Fatalf("task guidance should recommend progressive task:\n%s", text)
	}
	if !strings.Contains(text, "compatibility shims") {
		t.Fatalf("expected compat shim note:\n%s", text)
	}
}

func TestBuildGuidanceWaitPreferred(t *testing.T) {
	// With progressive task present, wait is a compat shim — prefer task action=wait.
	text := BuildGuidance([]GuidanceEntry{
		{Name: "wait"},
		{Name: "task"},
		{Name: "task_status"},
	})
	if !strings.Contains(text, "action=wait") && !strings.Contains(text, "compatibility shims") {
		t.Fatalf("expected progressive wait guidance:\n%s", text)
	}
	// Without task, standalone wait keeps its preference line.
	solo := BuildGuidance([]GuidanceEntry{{Name: "wait"}})
	if !strings.Contains(solo, "Prefer `wait`") && !strings.Contains(solo, "prefer `wait`") {
		t.Fatalf("expected standalone wait preference:\n%s", solo)
	}
}

func TestBuildGuidancePeerCoordination(t *testing.T) {
	text := BuildGuidance([]GuidanceEntry{
		{Name: "task"},
		{Name: "task_message"},
		{Name: "agent_roster"},
		{Name: "agent_message"},
		{Name: "agent_broadcast"},
	})
	for _, needle := range []string{
		"agent_message",
		"mid-flight",
		"child.completed",
		"task_message",
		"parent→owned-child",
		"chatty",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("missing peer coordination needle %q:\n%s", needle, text)
		}
	}
	if strings.Contains(text, "parent-only control plane") && !strings.Contains(text, "not a parent-only") {
		// Ensure we do not reintroduce parent-only control-plane framing without negation.
		t.Fatalf("guidance should not imply parent-only control plane:\n%s", text)
	}
}

func TestBuildGuidanceMCPFewNoPerToolList(t *testing.T) {
	text := BuildGuidance([]GuidanceEntry{
		{Name: "read"},
		{Name: "mcp_demo_echo"},
	})
	// Schemas list MCP tools; guidance must not restate each name — purpose.
	if strings.Contains(text, "`mcp_demo_echo`") {
		t.Fatalf("small MCP set should not list individual tools:\n%s", text)
	}
	if !strings.Contains(text, "registered `mcp_*` names") {
		t.Fatalf("mcp rec missing:\n%s", text)
	}
	if strings.Contains(text, "MCP tools (") {
		t.Fatalf("unexpected MCP bulk summary for small set:\n%s", text)
	}
}

func TestBuildGuidanceMCPSummarizesWhenMany(t *testing.T) {
	entries := []GuidanceEntry{{Name: "read"}, {Name: "toolsearch"}}
	for i := 0; i < MaxMCPGuidanceListed+5; i++ {
		entries = append(entries, GuidanceEntry{
			Name: fmt.Sprintf("mcp_srv_tool%d", i),
		})
	}
	text := BuildGuidance(entries)
	if strings.Contains(text, "`mcp_srv_tool0`") {
		t.Fatalf("expected MCP summary, got per-tool list:\n%s", text)
	}
	if !strings.Contains(text, fmt.Sprintf("MCP tools (%d from", MaxMCPGuidanceListed+5)) {
		t.Fatalf("missing MCP count summary:\n%s", text)
	}
	if !strings.Contains(text, "`srv` (") {
		t.Fatalf("missing server group:\n%s", text)
	}
	// Bounded growth: summary should stay small vs listing every purpose.
	if len(text) > 2500 {
		t.Fatalf("guidance too large with many MCP tools: %d chars", len(text))
	}
}

func TestBuildGuidanceDeterministic(t *testing.T) {
	entries := []GuidanceEntry{
		{Name: "grep"},
		{Name: "read"},
		{Name: "mcp_b_x"},
		{Name: "mcp_a_y"},
	}
	a := BuildGuidance(entries)
	b := BuildGuidance(entries)
	if a != b {
		t.Fatalf("non-deterministic guidance")
	}
}

func TestBuiltinShortPurposesCoversCoreTools(t *testing.T) {
	core := []string{
		"read", "write", "edit", "move", "delete", "glob", "grep", "bash", "webfetch", "websearch", "browser",
		"todowrite", "todoread", "memory_write", "memory_read",
		"issue_write", "issue_read", "plan_write", "plan_read", "plan_delegate",
		"notebook_edit", "sleep", "skill",
		"toolsearch", "question", "apply_patch", "enter_plan_mode",
		"exit_plan_mode", "phase_done", "task",
		"task_status", "task_read", "task_message", "task_interrupt", "delegate", "wait",
		"agent_roster", "agent_ownership", "agent_message", "agent_broadcast", "agent_thread", "team_task",
	}
	m := BuiltinShortPurposes()
	for _, name := range core {
		if _, ok := m[name]; !ok {
			t.Errorf("missing short purpose for built-in %q", name)
		}
	}
}

func TestBuildGuidanceEmpty(t *testing.T) {
	if got := BuildGuidance(nil); got != "" {
		t.Fatalf("empty entries = %q", got)
	}
}

func TestBuildGuidanceLayerPresence(t *testing.T) {
	text := BuildGuidance([]GuidanceEntry{{Name: "read"}, {Name: "bash"}})
	if !strings.Contains(text, "# Available tools") {
		t.Fatal("missing tools layer heading")
	}
	if !strings.Contains(text, "## Recommended use") {
		t.Fatal("missing recommended-use layer")
	}
	if !strings.Contains(text, "provider Tools array") {
		t.Fatal("missing schemas-vs-guidance split note")
	}
}
