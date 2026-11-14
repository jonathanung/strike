package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Review pass 1 on #1330: editing a hook's matcher must not reset an
// explicit failClosed to the event default.
func TestCustomizeHookEditKeepsFailClosed(t *testing.T) {
	_, work := customizeHome(t)
	path := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := `{"hooks": [{"event": "pre_tool_use", "matcher": "bash", "command": "echo hi", "failClosed": false}]}`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenCustomizeDoc(work, "hook", "", "workspace", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	idx := 0
	mustApply(t, work, CustomizeChange{
		Kind: "hook", Scope: "workspace", BaseHash: doc.BaseHash, Index: &idx,
		Hook: &CustomizeHookDraft{Event: "pre_tool_use", Matcher: "edit", Command: "echo hi"},
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var obj struct {
		Hooks []Hook `json:"hooks"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	if len(obj.Hooks) != 1 || obj.Hooks[0].Matcher != "edit" {
		t.Fatalf("hooks = %s", data)
	}
	if obj.Hooks[0].FailClosed == nil || *obj.Hooks[0].FailClosed {
		t.Fatalf("failClosed was not kept: %s", data)
	}
}

func writeCustomizeMCP(t *testing.T, home, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(home, ".strike", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// The MCP diff shows a changed secret as "(updated)" either way, so the
// hash must bind the written bytes: a value that was not previewed fails.
func TestCustomizeMCPApplyRejectsUnpreviewedSecret(t *testing.T) {
	home, work := customizeHome(t)
	path := writeCustomizeMCP(t, home, `{"servers":{"docs":{"command":"echo","env":{"TOKEN":"old-value"}}}}`, 0o600)
	draft := func(token string) CustomizeChange {
		return CustomizeChange{
			Kind: "mcp", Scope: "global", Name: "docs",
			MCP: &CustomizeMCPDraft{Type: "stdio", Command: "echo", Env: []CustomizePair{{Key: "TOKEN", Value: token}}},
		}
	}
	prevA := mustPreview(t, work, draft("value-a"))
	prevB := mustPreview(t, work, draft("value-b"))
	if prevA.Diff != prevB.Diff {
		t.Fatalf("test assumes identical redacted diffs:\n%s\n%s", prevA.Diff, prevB.Diff)
	}
	if prevA.DiffHash == prevB.DiffHash {
		t.Fatal("different secret values produced the same diffHash")
	}
	ch := draft("value-b")
	ch.Confirm = true
	ch.DiffHash = prevA.DiffHash
	ch.BaseHash = prevA.BaseHash
	if _, err := ApplyCustomize(work, ch); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("apply with A's hash and B's value: err = %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "value-b") {
		t.Fatalf("unpreviewed value was written: %s", data)
	}
}

// An empty env value is sent back as "" and must round-trip instead of
// failing with "needs a value"; a looser pre-existing mode ends at 0600.
func TestCustomizeMCPEmptyEnvValueSaves(t *testing.T) {
	home, work := customizeHome(t)
	path := writeCustomizeMCP(t, home, `{"servers":{"docs":{"command":"echo","env":{"NO_COLOR":""}}}}`, 0o644)
	doc, err := OpenCustomizeDoc(work, "mcp", "docs", "global", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if doc.MCP == nil || len(doc.MCP.Env) != 1 {
		t.Fatalf("doc = %+v", doc.MCP)
	}
	d := *doc.MCP
	d.Args = []string{"changed"}
	mustApply(t, work, CustomizeChange{Kind: "mcp", Scope: "global", Name: "docs", BaseHash: doc.BaseHash, MCP: &d})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Servers map[string]MCPServer `json:"servers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	got := file.Servers["docs"]
	if v, ok := got.Env["NO_COLOR"]; !ok || v != "" {
		t.Fatalf("env = %#v", got.Env)
	}
	if len(got.Args) != 1 || got.Args[0] != "changed" {
		t.Fatalf("args = %#v", got.Args)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

// A mask with nothing stored would write dots: it still needs a value.
func TestCustomizeMCPMaskWithoutStoredValueFails(t *testing.T) {
	_, work := customizeHome(t)
	_, err := PreviewCustomize(work, CustomizeChange{
		Kind: "mcp", Scope: "global", Name: "fresh",
		MCP: &CustomizeMCPDraft{Type: "stdio", Command: "echo", Env: []CustomizePair{{Key: "TOKEN", Value: SecretKeep}}},
	})
	if err == nil || !strings.Contains(err.Error(), "needs a value") {
		t.Fatalf("err = %v", err)
	}
}

// One bad skill in a strict root lists an error; the rest of the page loads.
func TestCustomizeCatalogSurvivesBrokenSkill(t *testing.T) {
	home, work := customizeHome(t)
	bad := filepath.Join(home, ".strike", "skills", "bad.md")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("---\nname: has space\ndescription: x\n---\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeCustomizeMCP(t, home, `{"servers":{"docs":{"command":"echo"}}}`, 0o600)
	cat, err := LoadCustomize(work)
	if err != nil {
		t.Fatalf("LoadCustomize: %v", err)
	}
	var warned bool
	for _, w := range cat.Warnings {
		if strings.Contains(w, "Skills did not load") && strings.Contains(w, "bad.md") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("warnings = %v", cat.Warnings)
	}
	var sawMCP bool
	for _, m := range cat.MCP {
		if m.Name == "docs" {
			sawMCP = true
		}
	}
	if !sawMCP {
		t.Fatalf("mcp section missing: %+v", cat.MCP)
	}
	if len(cat.Agents) == 0 {
		t.Fatal("agents section should still load")
	}
}
