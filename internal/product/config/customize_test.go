package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const customizeSecret = "sk-abcdefghijklmnopqrstuvwxyz0123456789"

func customizeHome(t *testing.T) (home, work string) {
	t.Helper()
	home = t.TempDir()
	work = t.TempDir()
	t.Setenv("HOME", home)
	ResetPluginCache()
	return home, work
}

func mustPreview(t *testing.T, work string, ch CustomizeChange) CustomizeResult {
	t.Helper()
	res, err := PreviewCustomize(work, ch)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if strings.Contains(res.Diff, customizeSecret) {
		t.Fatalf("preview diff leaked a secret:\n%s", res.Diff)
	}
	return res
}

func mustApply(t *testing.T, work string, ch CustomizeChange) CustomizeResult {
	t.Helper()
	prev := mustPreview(t, work, ch)
	ch.Confirm = true
	ch.DiffHash = prev.DiffHash
	ch.BaseHash = prev.BaseHash
	res, err := ApplyCustomize(work, ch)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Wrote {
		t.Fatal("apply did not write")
	}
	if strings.Contains(res.Diff, customizeSecret) {
		t.Fatalf("apply diff leaked a secret:\n%s", res.Diff)
	}
	return res
}

func TestCustomizeCatalogOrigins(t *testing.T) {
	home, work := customizeHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".strike", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, ".strike", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(home, ".strike", "skills", "note.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: note\ndescription: seeded\n---\nSay hello.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(work, ".strike", "agents", "scribe.md")
	if err := os.WriteFile(agentPath, []byte("---\ndescription: takes notes\npermission.write: deny\n---\nWrite it down.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTree(t, filepath.Join(home, ".strike", "plugins", "acme.pack"), map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.pack",
  "version": "1.0.0",
  "name": "Acme",
  "strike": { "min": "0.1.0" },
  "contributions": {
    "skills": [{ "path": "skills/pack-skill.md" }],
    "mcp": [{ "name": "pack", "command": "echo", "env": {"TOKEN": "` + customizeSecret + `"}}]
  }
}`,
		"skills/pack-skill.md": "---\ndescription: from a plugin\n---\nPlugin skill.\n",
	})

	cat, err := LoadCustomize(work)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSkillsWithError(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Skills) != len(loaded) {
		t.Fatalf("skills %d, LoadSkills %d", len(cat.Skills), len(loaded))
	}
	var note, pack, builtin *CustomizeSkill
	for i := range cat.Skills {
		switch cat.Skills[i].Name {
		case "note":
			note = &cat.Skills[i]
		case "pack-skill":
			pack = &cat.Skills[i]
		case "commit":
			builtin = &cat.Skills[i]
		}
	}
	if note == nil || note.Origin != OriginGlobal || !note.Editable {
		t.Fatalf("note = %+v", note)
	}
	if pack == nil || pack.Origin != OriginPlugin || pack.Editable || pack.PluginID != "acme.pack" {
		t.Fatalf("pack skill = %+v", pack)
	}
	if builtin == nil || builtin.Origin != OriginBuiltin || builtin.Editable {
		t.Fatalf("builtin = %+v", builtin)
	}

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Agents) != len(agents) {
		t.Fatalf("agents %d, LoadAgents %d", len(cat.Agents), len(agents))
	}
	var scribe *CustomizeAgent
	for i := range cat.Agents {
		if cat.Agents[i].Name == "scribe" {
			scribe = &cat.Agents[i]
		}
	}
	if scribe == nil || scribe.Origin != OriginWorkspace || !scribe.Editable {
		t.Fatalf("scribe = %+v", scribe)
	}
	if len(scribe.Permissions) == 0 || !strings.Contains(strings.Join(scribe.Permissions, "\n"), "deny") {
		t.Fatalf("permissions = %v", scribe.Permissions)
	}

	raw, err := json.Marshal(cat)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), customizeSecret) {
		t.Fatalf("catalog JSON leaked a secret: %s", raw)
	}
	if strings.Contains(string(raw), ":null") {
		t.Fatalf("catalog JSON used null instead of an empty list: %s", raw)
	}
	var sawPackMCP bool
	for _, row := range cat.MCP {
		if row.Name == "pack" {
			sawPackMCP = true
			if row.Editable || row.Origin != OriginPlugin {
				t.Fatalf("plugin mcp = %+v", row)
			}
			if len(row.EnvKeys) != 1 || row.EnvKeys[0] != "TOKEN" {
				t.Fatalf("env keys = %v", row.EnvKeys)
			}
		}
	}
	if !sawPackMCP {
		t.Fatal("missing plugin mcp row")
	}
	var sawPlugin bool
	for _, p := range cat.Plugins {
		if p.ID == "acme.pack" {
			sawPlugin = true
			if !p.Enabled || p.Skills != 1 || p.MCP != 1 {
				t.Fatalf("plugin = %+v", p)
			}
		}
	}
	if !sawPlugin {
		t.Fatal("missing plugin row")
	}
}

func TestCustomizeSkillPreviewApplyAndStale(t *testing.T) {
	home, work := customizeHome(t)
	path := filepath.Join(home, ".strike", "skills", "note.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: note\ndescription: seeded\n---\nSay hello.\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenCustomizeDoc(work, "skill", "note", "global", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Editable || doc.Description != "seeded" || doc.BaseHash == "" || doc.BaseHash == "missing" {
		t.Fatalf("doc = %+v", doc)
	}
	ch := CustomizeChange{
		Kind: "skill", Scope: "global", Name: "note",
		Description: "updated", Body: "Say hello again.", BaseHash: doc.BaseHash,
	}
	prev := mustPreview(t, work, ch)
	if !strings.Contains(prev.Diff, "updated") {
		t.Fatalf("diff = %s", prev.Diff)
	}
	if _, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != body {
		t.Fatal("preview wrote the file")
	}
	ch.BaseHash = "deadbeef"
	if _, err := PreviewCustomize(work, ch); err == nil {
		t.Fatal("stale base hash was accepted")
	}
	ch.BaseHash = doc.BaseHash
	mustApply(t, work, ch)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "description: updated") || !strings.Contains(string(got), "Say hello again.") {
		t.Fatalf("written = %s", got)
	}
}

func TestCustomizeReservedSkillAndSecretBody(t *testing.T) {
	_, work := customizeHome(t)
	_, err := PreviewCustomize(work, CustomizeChange{
		Kind: "skill", Scope: "workspace", Name: "mcp",
		Description: "nope", Body: "body", BaseHash: "missing",
	})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("err = %v", err)
	}
	_, err = PreviewCustomize(work, CustomizeChange{
		Kind: "skill", Scope: "workspace", Name: "leak",
		Description: "nope", Body: "token " + customizeSecret, BaseHash: "missing",
	})
	if err == nil {
		t.Fatal("secret body was accepted")
	}
	if strings.Contains(err.Error(), customizeSecret) {
		t.Fatalf("error leaked secret: %v", err)
	}
}

func TestCustomizeMCPMaskKeepAndMode(t *testing.T) {
	home, work := customizeHome(t)
	path := filepath.Join(home, ".strike", "mcp.jsonc")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{
  "servers": {
    "docs": {
      "command": "echo",
      "args": ["ok"],
      "env": {"TOKEN": "` + customizeSecret + `"},
      "headers": {"Authorization": "Bearer ` + customizeSecret + `"},
      "oauth": {"clientSecret": "` + customizeSecret + `"}
    }
  }
}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadCustomize(work)
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(cat)
	if strings.Contains(string(blob), customizeSecret) {
		t.Fatalf("catalog leaked secret: %s", blob)
	}
	doc, err := OpenCustomizeDoc(work, "mcp", "docs", "global", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if doc.MCP == nil || len(doc.MCP.Env) != 1 || doc.MCP.Env[0].Value != SecretKeep {
		t.Fatalf("draft env = %+v", doc.MCP)
	}
	if doc.MCP.Headers[0].Value != SecretKeep {
		t.Fatalf("header = %+v", doc.MCP.Headers)
	}
	docBlob, _ := json.Marshal(doc)
	if strings.Contains(string(docBlob), customizeSecret) {
		t.Fatalf("doc leaked secret: %s", docBlob)
	}
	if strings.Contains(string(docBlob), ":null") {
		t.Fatalf("doc JSON used null: %s", docBlob)
	}
	ch := CustomizeChange{
		Kind: "mcp", Scope: "global", Name: "docs", BaseHash: doc.BaseHash,
		MCP: &CustomizeMCPDraft{
			Type: "stdio", Command: "echo", Args: []string{"ok"},
			Env:     []CustomizePair{{Key: "TOKEN", Value: SecretKeep}, {Key: "OTHER", Value: "plain"}},
			Headers: []CustomizePair{{Key: "Authorization", Value: SecretKeep}},
		},
	}
	prev := mustPreview(t, work, ch)
	if strings.Contains(prev.Diff, customizeSecret) || strings.Contains(prev.Diff, "Bearer") {
		t.Fatalf("diff leaked: %s", prev.Diff)
	}
	if !strings.Contains(prev.Diff, "OTHER") || !strings.Contains(prev.Diff, "(set)") {
		t.Fatalf("diff = %s", prev.Diff)
	}
	mustApply(t, work, ch)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", st.Mode().Perm())
	}
	mc, err := ReadMCPFile(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := mc.Servers["docs"]
	if srv.Env["TOKEN"] != customizeSecret || srv.Env["OTHER"] != "plain" {
		t.Fatalf("env = %#v", srv.Env)
	}
	if srv.Headers["Authorization"] != "Bearer "+customizeSecret {
		t.Fatalf("header = %#v", srv.Headers)
	}
	if srv.OAuth == nil || srv.OAuth.ClientSecret != customizeSecret {
		t.Fatalf("oauth = %+v", srv.OAuth)
	}

	// A secret-shaped command is kept when the form sends keepCommand, and
	// never appears in the doc or the diff.
	secretCmd := "echo " + customizeSecret
	srv.Command = secretCmd
	mc.Servers["docs"] = srv
	rewritten, err := json.Marshal(map[string]any{"servers": mc.Servers})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err = OpenCustomizeDoc(work, "mcp", "docs", "global", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.MCP.KeepCommand || doc.MCP.Command != "" {
		t.Fatalf("draft = %+v", doc.MCP)
	}
	again, _ := json.Marshal(doc)
	if strings.Contains(string(again), customizeSecret) {
		t.Fatalf("secret command leaked: %s", again)
	}
	ch = CustomizeChange{
		Kind: "mcp", Scope: "global", Name: "docs", BaseHash: doc.BaseHash,
		MCP: &CustomizeMCPDraft{
			Type: "stdio", KeepCommand: true,
			Env: []CustomizePair{{Key: "TOKEN", Value: SecretKeep}},
		},
	}
	prev = mustPreview(t, work, ch)
	if strings.Contains(prev.Diff, customizeSecret) {
		t.Fatalf("diff leaked command: %s", prev.Diff)
	}
	mustApply(t, work, ch)
	mc, err = ReadMCPFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if mc.Servers["docs"].Command != secretCmd {
		t.Fatalf("command was wiped: %q", mc.Servers["docs"].Command)
	}
}

func TestCustomizeHookPreservesOtherKeys(t *testing.T) {
	_, work := customizeHome(t)
	path := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := `{
  "permissionMode": "plan",
  "hooks": [{"event": "pre_tool_use", "command": "echo hi"}]
}`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenCustomizeDoc(work, "hook", "", "workspace", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	idx := 1
	ch := CustomizeChange{
		Kind: "hook", Scope: "workspace", BaseHash: doc.BaseHash, Index: &idx,
		Hook: &CustomizeHookDraft{Event: "post_tool_use", Command: "echo there"},
	}
	mustApply(t, work, ch)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"permissionMode"`) || !strings.Contains(string(data), "plan") {
		t.Fatalf("permissionMode dropped: %s", data)
	}
	if !strings.Contains(string(data), "echo hi") || !strings.Contains(string(data), "echo there") {
		t.Fatalf("hooks = %s", data)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	if _, ok := obj["permissions"]; ok {
		t.Fatalf("permissions key appeared: %s", data)
	}
}

func TestCustomizeHookSecretNotEditable(t *testing.T) {
	_, work := customizeHome(t)
	path := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"hooks":[{"event":"pre_tool_use","command":"echo ` + customizeSecret + `"}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadCustomize(work)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cat)
	if strings.Contains(string(raw), customizeSecret) {
		t.Fatalf("catalog leaked hook command: %s", raw)
	}
	var secretHook *CustomizeHook
	for i := range cat.Hooks {
		if cat.Hooks[i].Origin == OriginWorkspace {
			secretHook = &cat.Hooks[i]
		}
	}
	if secretHook == nil || secretHook.Editable {
		t.Fatalf("hook = %+v", secretHook)
	}
	idx := 0
	_, err = PreviewCustomize(work, CustomizeChange{
		Kind: "hook", Scope: "workspace", BaseHash: secretHook.BaseHash, Index: &idx,
		Hook: &CustomizeHookDraft{Event: "pre_tool_use", Command: "echo safe"},
	})
	if err == nil || strings.Contains(err.Error(), customizeSecret) {
		t.Fatalf("err = %v", err)
	}
}

func TestCustomizeRosterPatchesOnlyRosterKeys(t *testing.T) {
	_, work := customizeHome(t)
	path := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := `{
  "permissionMode": "default",
  "session": {
    "model": "echo",
    "childHandoffRequired": true,
    "agentBudget": {"stallAfterS": 30, "maxTurns": 4}
  }
}`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := LoadCustomize(work)
	if err != nil {
		t.Fatal(err)
	}
	if cat.Roster.WorkspaceStall != 30 || cat.Roster.WorkspaceHandoff != "required" {
		t.Fatalf("roster = %+v", cat.Roster)
	}
	ch := CustomizeChange{
		Kind: "roster", Scope: "project", BaseHash: cat.Roster.WorkspaceBaseHash,
		StallOp: "set", StallSeconds: 90, RosterHandoff: "optional",
	}
	mustApply(t, work, ch)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["permissionMode"] != "default" {
		t.Fatalf("permissionMode = %#v\n%s", obj["permissionMode"], data)
	}
	session := obj["session"].(map[string]any)
	if session["model"] != "echo" {
		t.Fatalf("model = %#v", session["model"])
	}
	if session["childHandoffRequired"] != false {
		t.Fatalf("handoff = %#v", session["childHandoffRequired"])
	}
	budget := session["agentBudget"].(map[string]any)
	if budget["stallAfterS"] != float64(90) || budget["maxTurns"] != float64(4) {
		t.Fatalf("budget = %#v", budget)
	}
	cat, err = LoadCustomize(work)
	if err != nil {
		t.Fatal(err)
	}
	clear := CustomizeChange{
		Kind: "roster", Scope: "workspace", BaseHash: cat.Roster.WorkspaceBaseHash,
		StallOp: "clear", RosterHandoff: "unchanged",
	}
	mustApply(t, work, clear)
	data, _ = os.ReadFile(path)
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	session = obj["session"].(map[string]any)
	budget = session["agentBudget"].(map[string]any)
	if _, ok := budget["stallAfterS"]; ok {
		t.Fatalf("stall key remained: %#v", budget)
	}
	if budget["maxTurns"] != float64(4) {
		t.Fatalf("maxTurns = %#v", budget["maxTurns"])
	}
}

func TestCustomizeRulesAndAgentPermissions(t *testing.T) {
	home, work := customizeHome(t)
	ch := CustomizeChange{
		Kind: "rule", Scope: "workspace", Name: AgentsMDName,
		Body: "Prefer small patches.\n", BaseHash: "missing",
	}
	mustApply(t, work, ch)
	got, err := os.ReadFile(filepath.Join(work, AgentsMDName))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "Prefer small patches.\n" {
		t.Fatalf("rules = %q", got)
	}
	_, err = PreviewCustomize(work, CustomizeChange{
		Kind: "rule", Scope: "global", Body: "token " + customizeSecret, BaseHash: "missing",
	})
	if err == nil || strings.Contains(err.Error(), customizeSecret) {
		t.Fatalf("err = %v", err)
	}

	agentPath := filepath.Join(home, ".strike", "agents", "scribe.md")
	if err := os.MkdirAll(filepath.Dir(agentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "---\ndescription: code review\npermission.write: deny\n---\nReview carefully.\n"
	if err := os.WriteFile(agentPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenCustomizeDoc(work, "agent", "scribe", "global", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	mustApply(t, work, CustomizeChange{
		Kind: "agent", Scope: "global", Name: "scribe", BaseHash: doc.BaseHash,
		Description: "reviewer", Body: "Review carefully.\n", Handoff: "inherit",
	})
	updated, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "permission.write: deny") {
		t.Fatalf("permission line dropped: %s", updated)
	}
	if !strings.Contains(string(updated), "description: reviewer") {
		t.Fatalf("description = %s", updated)
	}
	parsed, err := parseAgentFile(agentPath)
	if err != nil || parsed == nil {
		t.Fatal(err)
	}
	if len(parsed.Permissions) != 1 || parsed.Permissions[0].Action != "deny" {
		t.Fatalf("rules = %+v", parsed.Permissions)
	}

	_, err = PreviewCustomize(work, CustomizeChange{
		Kind: "agent", Scope: "workspace", Name: "fresh", BaseHash: "missing",
		Description: "new", Body: "Hello.\npermission.write: allow\n", Handoff: "inherit",
	})
	if err != nil {
		t.Fatal(err)
	}
	mustApply(t, work, CustomizeChange{
		Kind: "agent", Scope: "workspace", Name: "fresh", BaseHash: "missing",
		Description: "new", Body: "Hello.\n", Handoff: "inherit",
	})
	fresh, err := parseAgentFile(filepath.Join(work, ".strike", "agents", "fresh.md"))
	if err != nil || fresh == nil {
		t.Fatal(err)
	}
	if len(fresh.Permissions) != 0 {
		t.Fatalf("new agent rules = %+v", fresh.Permissions)
	}
	_, err = PreviewCustomize(work, CustomizeChange{
		Kind: "agent", Scope: "workspace", Name: "bad-effort", BaseHash: "missing",
		Description: "new", Body: "Hello.\n", Effort: "nope", Handoff: "inherit",
	})
	if err == nil || !strings.Contains(err.Error(), "effort") {
		t.Fatalf("err = %v", err)
	}
}

func TestCustomizeRejectsPathEscapeAndSymlink(t *testing.T) {
	home, work := customizeHome(t)
	_, err := PreviewCustomize(work, CustomizeChange{
		Kind: "skill", Scope: "global", Name: "../note",
		Description: "x", Body: "body", BaseHash: "missing",
	})
	if err == nil {
		t.Fatal("path escape was accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	outsideBody := "---\nname: note\ndescription: out\n---\nOutside.\n"
	if err := os.WriteFile(outside, []byte(outsideBody), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".strike", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "note.md")); err != nil {
		t.Fatal(err)
	}
	_, err = ApplyCustomize(work, CustomizeChange{
		Confirm: true, Kind: "skill", Scope: "global", Name: "note",
		Description: "changed", Body: "Inside.", BaseHash: "missing", DiffHash: "nope",
	})
	if err == nil {
		t.Fatal("symlink write was accepted")
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != outsideBody {
		t.Fatalf("symlink target changed: %s", got)
	}
}
