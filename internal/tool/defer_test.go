package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/provider"
)

func TestIsCoreTool(t *testing.T) {
	for _, name := range []string{
		"read", "glob", "grep", "edit", "write", "apply_patch", "bash",
		"task", "task_status", "delegate", "wait", "agent_roster", "agent_ownership", "agent_message", "agent_broadcast", "agent_thread", "team_task",
		"plan_write", "plan_read", "plan_delegate", "toolsearch", "question", "enter_plan_mode",
	} {
		if !IsCoreTool(name) {
			t.Errorf("IsCoreTool(%q) = false, want true", name)
		}
		if IsDeferredTool(name) {
			t.Errorf("IsDeferredTool(%q) = true, want false", name)
		}
	}
	for _, name := range []string{
		"webfetch", "sleep", "skill", "todowrite", "memory_read",
		"issue_write", "notebook_edit", "mcp_demo_ping",
		"definition", "references", "symbols", "diagnostics",
	} {
		if IsCoreTool(name) {
			t.Errorf("IsCoreTool(%q) = true, want false", name)
		}
		if !IsDeferredTool(name) {
			t.Errorf("IsDeferredTool(%q) = false, want true", name)
		}
	}
	if IsCoreTool("") || IsDeferredTool("") {
		t.Fatal("empty name should not be core or deferred")
	}
}

func TestSchemasForProviderDeferOff(t *testing.T) {
	reg := NewRegistry(NewRead(), NewWebFetch(), NewSleep())
	reg.Register(NewToolSearch(reg))
	got := reg.SchemasForProvider()
	if len(got) != len(reg.Schemas()) {
		t.Fatalf("defer off: SchemasForProvider len = %d, want %d", len(got), len(reg.Schemas()))
	}
}

func TestSchemasForProviderOmitUntilDiscover(t *testing.T) {
	reg := NewRegistry(NewRead(), NewWebFetch(), NewSleep())
	reg.Register(NewToolSearch(reg))
	reg.SetDeferLoading(true)

	got := reg.SchemasForProvider()
	names := schemaNameSet(got)
	if !names["read"] || !names["toolsearch"] {
		t.Fatalf("core missing from provider schemas: %v", names)
	}
	if names["webfetch"] || names["sleep"] {
		t.Fatalf("deferred tools present before discover: %v", names)
	}
	if reg.DeferredPendingCount() != 2 {
		t.Fatalf("DeferredPendingCount = %d, want 2", reg.DeferredPendingCount())
	}

	reg.Discover("webfetch")
	got = reg.SchemasForProvider()
	names = schemaNameSet(got)
	if !names["webfetch"] {
		t.Fatal("webfetch missing after Discover")
	}
	if names["sleep"] {
		t.Fatal("sleep should still be deferred")
	}
	if reg.DeferredPendingCount() != 1 {
		t.Fatalf("DeferredPendingCount = %d, want 1", reg.DeferredPendingCount())
	}
	if !reg.Discovered("webfetch") || reg.Discovered("sleep") {
		t.Fatalf("Discovered flags wrong: webfetch=%v sleep=%v",
			reg.Discovered("webfetch"), reg.Discovered("sleep"))
	}
}

func TestToolSearchDiscoversDeferred(t *testing.T) {
	reg := NewRegistry(NewRead(), NewWebFetch(), NewSleep())
	ts := NewToolSearch(reg)
	reg.Register(ts)
	reg.SetDeferLoading(true)

	if schemaNameSet(reg.SchemasForProvider())["webfetch"] {
		t.Fatal("webfetch should not be in provider schemas before search")
	}

	res, err := ts.Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "webfetch",
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "- webfetch:") {
		t.Fatalf("output = %q", res.Output)
	}
	if !strings.Contains(res.Output, "Discovered tools are available") {
		t.Fatalf("missing discover note: %q", res.Output)
	}
	var meta struct {
		Discovered []string `json:"discovered"`
		Count      int      `json:"count"`
	}
	if err := json.Unmarshal(res.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Count < 1 || len(meta.Discovered) < 1 {
		t.Fatalf("meta = %+v", meta)
	}

	names := schemaNameSet(reg.SchemasForProvider())
	if !names["webfetch"] {
		t.Fatal("webfetch not promoted after toolsearch")
	}
	if names["sleep"] {
		t.Fatal("unrelated sleep should stay deferred")
	}
	if !strings.Contains(ts.Description(), "deferred") {
		t.Fatalf("description should mention deferred mode: %q", ts.Description())
	}
}

func TestCloneWithoutCopiesDeferState(t *testing.T) {
	reg := NewRegistry(NewRead(), NewWebFetch(), NewTask())
	reg.SetDeferLoading(true)
	reg.Discover("webfetch")

	child := reg.CloneWithout("task")
	if !child.DeferLoading() {
		t.Fatal("child should inherit defer loading")
	}
	names := schemaNameSet(child.SchemasForProvider())
	if names["task"] {
		t.Fatal("task should be stripped")
	}
	if !names["webfetch"] {
		t.Fatal("discovered webfetch should copy to child")
	}
	if !names["read"] {
		t.Fatal("read missing on child")
	}
}

func TestDiscoverIgnoresUnknownAndCore(t *testing.T) {
	reg := NewRegistry(NewRead(), NewSleep())
	reg.SetDeferLoading(true)
	reg.Discover("read", "nope", "sleep")
	if !reg.Discovered("sleep") {
		t.Fatal("sleep should be discovered")
	}
	if !reg.Discovered("read") {
		t.Fatal("read should report discovered")
	}
	if reg.Discovered("nope") {
		t.Fatal("unknown should not be discovered")
	}
}

func schemaNameSet(schemas []provider.ToolSchema) map[string]bool {
	out := make(map[string]bool, len(schemas))
	for _, s := range schemas {
		out[s.Name] = true
	}
	return out
}
