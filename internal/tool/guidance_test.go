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

func TestPermissionName(t *testing.T) {
	cases := map[string]string{
		"read":          "read",
		"edit":          "edit",
		"apply_patch":   "edit",
		"notebook_edit": "edit",
		"mcp_foo_bar":   "mcp",
		"task":          "task",
	}
	for name, want := range cases {
		if got := PermissionName(name); got != want {
			t.Errorf("PermissionName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestBuildGuidanceListsExactTools(t *testing.T) {
	text := BuildGuidance([]GuidanceEntry{
		{Name: "read", Purpose: "read file contents"},
		{Name: "glob", Purpose: "find files by name pattern"},
		{Name: "bash", Purpose: "run a shell command"},
	})
	for _, want := range []string{
		"# Available tools",
		"`read` — read file contents",
		"`glob` — find files by name pattern",
		"`bash` — run a shell command",
		"Prefer `read`/`glob`/`grep` over shelling out",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "`write`") || strings.Contains(text, "`task`") {
		t.Fatalf("unexpected mutation/task guidance:\n%s", text)
	}
}

func TestBuildGuidanceOmitsMutationRecsWhenAbsent(t *testing.T) {
	text := BuildGuidance([]GuidanceEntry{
		{Name: "read", Purpose: "read file contents"},
		{Name: "glob", Purpose: "find files by name pattern"},
		{Name: "grep", Purpose: "search file contents by regex"},
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
		{Name: "read", Purpose: "x"},
		{Name: "sleep", Purpose: "y"},
	})
	if strings.Contains(text, "`task`") {
		t.Fatalf("task mentioned without registry entry:\n%s", text)
	}
}

func TestBuildGuidanceTaskStatusPreferred(t *testing.T) {
	text := BuildGuidance([]GuidanceEntry{
		{Name: "task", Purpose: "delegate"},
		{Name: "task_status", Purpose: "status"},
		{Name: "task_read", Purpose: "read"},
		{Name: "sleep", Purpose: "wait"},
	})
	if !strings.Contains(text, "task_status") || !strings.Contains(text, "task_read") {
		t.Fatalf("missing task_status/task_read guidance:\n%s", text)
	}
}

func TestBuildGuidanceMCPListed(t *testing.T) {
	text := BuildGuidance([]GuidanceEntry{
		{Name: "read", Purpose: "read"},
		{Name: "mcp_demo_echo", Purpose: "echo"},
	})
	if !strings.Contains(text, "`mcp_demo_echo` — echo") {
		t.Fatalf("mcp tool missing:\n%s", text)
	}
	if !strings.Contains(text, "registered `mcp_*` names") {
		t.Fatalf("mcp rec missing:\n%s", text)
	}
}

func TestBuildGuidanceMCPSummarizesWhenMany(t *testing.T) {
	entries := []GuidanceEntry{{Name: "read", Purpose: "read"}, {Name: "toolsearch", Purpose: "search"}}
	for i := 0; i < MaxMCPGuidanceListed+5; i++ {
		entries = append(entries, GuidanceEntry{
			Name:    fmt.Sprintf("mcp_srv_tool%d", i),
			Purpose: fmt.Sprintf("purpose %d", i),
		})
	}
	text := BuildGuidance(entries)
	if strings.Contains(text, "`mcp_srv_tool0` —") {
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
		{Name: "grep", Purpose: "g"},
		{Name: "read", Purpose: "r"},
		{Name: "mcp_b_x", Purpose: "bx"},
		{Name: "mcp_a_y", Purpose: "ay"},
	}
	a := BuildGuidance(entries)
	b := BuildGuidance(entries)
	if a != b {
		t.Fatalf("non-deterministic guidance")
	}
}

func TestBuiltinShortPurposesCoversCoreTools(t *testing.T) {
	// Drift guard: every tool constructed in TestToolNames (except task, which
	// is optional in that table) plus task must have a short purpose.
	core := []string{
		"read", "write", "edit", "glob", "grep", "bash", "webfetch",
		"todowrite", "todoread", "memory_write", "memory_read",
		"issue_write", "issue_read", "notebook_edit", "sleep", "skill",
		"toolsearch", "question", "apply_patch", "enter_plan_mode",
		"exit_plan_mode", "phase_done", "task",
		"task_status", "task_read", "task_message", "task_interrupt",
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
