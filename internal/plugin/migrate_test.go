package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mixedLegacyBundle(t *testing.T, root string) {
	t.Helper()
	writePlugin(t, root, "acme.review-pack", map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.review-pack",
  "version": "1.2.0",
  "name": "Acme Review Pack",
  "description": "Review agent, /ship-review skill, workflow, theme, and optional MCP.",
  "strike": { "min": "0.2.0" },
  "capabilities": ["agents", "skills", "workflows", "themes", "mcp.stdio", "panes"],
  "contributions": {
    "agents": [{ "path": "agents/reviewer-strict.md" }],
    "skills": [
      { "path": "skills/ship-review/SKILL.md" },
      { "path": "skills/flat-portable.md" },
      { "path": "skills/strike-extra.md" }
    ],
    "workflows": [{ "path": "workflows/review-gate.json" }],
    "themes": [{ "path": "themes/acme-dark.json" }],
    "providers": [{ "path": "providers/acme-proxy.json", "profileName": "acme-proxy" }],
    "mcp": [
      {
        "name": "acme-lint",
        "transport": "stdio",
        "command": "bin/acme-lint-mcp",
        "args": ["--serve"]
      },
      {
        "name": "acme-cloud",
        "transport": "http",
        "url": "https://mcp.example.com/acme"
      }
    ],
    "harnesses": [{
      "name": "acme-choose",
      "command": "bin/choose-best",
      "args": []
    }],
    "hooks": [{
      "event": "pre_tool_use",
      "matcher": "bash",
      "type": "command",
      "command": "bin/hook-pre-bash.sh"
    }],
    "panes": [{
      "id": "acme.status",
      "path": "panes/status.json",
      "abi": "pane/1"
    }]
  }
}`,
		"agents/reviewer-strict.md":   validAgentMD("reviewer-strict"),
		"skills/ship-review/SKILL.md": validSkillMD("ship-review"),
		"skills/flat-portable.md":     "---\nname: flat-portable\ndescription: portable flat skill\n---\nDo portable $ARGUMENTS\n",
		"skills/strike-extra.md":      "---\ndescription: strike extra\neffort: high\n---\nStrike-only extra $ARGUMENTS\n",
		"workflows/review-gate.json":  validWorkflowJSON("review-gate"),
		"themes/acme-dark.json":       validThemeJSON("acme-dark"),
		"providers/acme-proxy.json":   validProviderJSON("acme-proxy"),
		"bin/acme-lint-mcp":           "#!/bin/sh\necho lint\n",
		"bin/choose-best":             "#!/bin/sh\necho choose\n",
		"bin/hook-pre-bash.sh":        "#!/bin/sh\necho hook\n",
		"panes/status.json": `{
  "schemaVersion": 1,
  "id": "acme.status",
  "title": "Acme Status",
  "mode": "static",
  "permissions": {
    "host": [],
    "fs": "none",
    "network": "none",
    "command": "none"
  },
  "view": { "type": "text", "text": "hi" }
}`,
		"README.md": "Acme Review Pack\n",
	})
}

func TestMigrate_MixedContributionExample(t *testing.T) {
	src := t.TempDir()
	mixedLegacyBundle(t, src)

	before, diags := LoadOne(src, ScopeGlobal, "0.2.0")
	if before == nil {
		t.Fatalf("legacy load failed: %v", diags)
	}

	res, err := Migrate(MigrateOptions{Target: src, StrikeVersion: "0.2.0", Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || res.ID != "acme.review-pack" {
		t.Fatalf("result: %+v", res)
	}

	after, diags := LoadOne(src, ScopeGlobal, "0.2.0")
	if after == nil {
		t.Fatalf("APS load failed: %v", diags)
	}
	if after.Manifest.Format != FormatAPS {
		t.Fatalf("format=%s", after.Manifest.Format)
	}
	if after.Manifest.ID != "acme.review-pack" {
		t.Fatalf("name=%s", after.Manifest.ID)
	}
	if after.Manifest.Name != "Acme Review Pack" {
		t.Fatalf("displayName=%s", after.Manifest.Name)
	}
	if after.Manifest.StrikeCLI == nil || after.Manifest.StrikeCLI.DisplayName != "Acme Review Pack" {
		t.Fatalf("strike cli ext: %+v", after.Manifest.StrikeCLI)
	}
	if after.Manifest.SchemaVersion != 0 || after.Manifest.Contributions.Agents != nil {
		t.Fatal("legacy fields must not remain as a second source of truth")
	}
	if len(after.Agents) != len(before.Agents) || after.Agents[0].RelPath != "com.strike.cli/agents/reviewer-strict.md" {
		t.Fatalf("agents before=%v after=%v", before.Agents, after.Agents)
	}
	if len(after.Skills) != len(before.Skills) {
		t.Fatalf("skills before=%d after=%d (%v)", len(before.Skills), len(after.Skills), after.Skills)
	}
	if after.MCPCount != before.MCPCount {
		t.Fatalf("mcp before=%d after=%d", before.MCPCount, after.MCPCount)
	}
	if len(after.Workflows) != 1 || len(after.Themes) != 1 || len(after.Providers) != 1 {
		t.Fatalf("strike surfaces: wf=%d themes=%d prov=%d", len(after.Workflows), len(after.Themes), len(after.Providers))
	}

	mustExist := []string{
		"plugin.json",
		"mcp.json",
		"skills/ship-review/SKILL.md",
		"skills/flat-portable/SKILL.md",
		"com.strike.cli/skills/strike-extra.md",
		"com.strike.cli/agents/reviewer-strict.md",
		"com.strike.cli/workflows/review-gate.json",
		"com.strike.cli/themes/acme-dark.json",
		"com.strike.cli/providers/acme-proxy.json",
		"com.strike.cli/harnesses/acme-choose.json",
		"com.strike.cli/hooks/hook-1.json",
		"com.strike.cli/panes/status.json",
		"com.strike.cli/bin/choose-best",
		"bin/acme-lint-mcp",
		"README.md",
	}
	for _, rel := range mustExist {
		if _, err := os.Stat(filepath.Join(src, rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(src, "skills/flat-portable.md")); !os.IsNotExist(err) {
		t.Fatal("flat portable skill should be wrapped, not left at skills/flat-portable.md")
	}

	raw, err := os.ReadFile(filepath.Join(src, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, forbidden := range []string{`"schemaVersion"`, `"id":`, `"contributions"`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("plugin.json still has %s:\n%s", forbidden, body)
		}
	}
	if !strings.Contains(body, APSPluginSchemaV1) {
		t.Fatalf("missing APS $schema:\n%s", body)
	}

	mcpRaw, err := os.ReadFile(filepath.Join(src, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mcpDoc struct {
		Servers map[string]struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			URL     string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(mcpRaw, &mcpDoc); err != nil {
		t.Fatal(err)
	}
	if mcpDoc.Servers["acme-lint"].Type != "stdio" || mcpDoc.Servers["acme-lint"].Command != "./bin/acme-lint-mcp" {
		t.Fatalf("stdio mcp: %+v", mcpDoc.Servers["acme-lint"])
	}
	if mcpDoc.Servers["acme-cloud"].Type != "streamable-http" {
		t.Fatalf("http should map to streamable-http: %+v", mcpDoc.Servers["acme-cloud"])
	}

	for _, d := range diags {
		if d.Code == "deprecated" && strings.Contains(d.Message, "format=legacy") {
			t.Fatalf("legacy deprecation after migrate: %v", diags)
		}
	}
}

func TestMigrate_DryRunDoesNotWrite(t *testing.T) {
	src := t.TempDir()
	mixedLegacyBundle(t, src)
	before, err := os.ReadFile(filepath.Join(src, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Migrate(MigrateOptions{Target: src, StrikeVersion: "0.2.0", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied {
		t.Fatal("dry-run must not apply")
	}
	if !strings.Contains(res.Plan.Format(), "legacy → Agent Plugins") {
		t.Fatalf("plan:\n%s", res.Plan.Format())
	}
	after, err := os.ReadFile(filepath.Join(src, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("dry-run mutated plugin.json")
	}
	if _, err := os.Stat(filepath.Join(src, "mcp.json")); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote mcp.json")
	}
}

func TestMigrate_AlreadyAPS(t *testing.T) {
	src := t.TempDir()
	writePlugin(t, src, "acme.skills", map[string]string{
		"plugin.json":         apsManifest("acme.skills"),
		"skills/foo/SKILL.md": validSkillMD("foo"),
	})
	_, err := Migrate(MigrateOptions{Target: src, StrikeVersion: "0.2.0", Confirm: true})
	if !errors.Is(err, ErrAlreadyAPS) {
		t.Fatalf("want ErrAlreadyAPS, got %v", err)
	}
	m, _, err := ReadManifest(src)
	if err != nil {
		t.Fatal(err)
	}
	if m.Format != FormatAPS {
		t.Fatalf("tree mutated: format=%s", m.Format)
	}
}

func TestMigrate_InstalledClearsTrustAndUpdatesDigest(t *testing.T) {
	home := t.TempDir()
	gRoot := filepath.Join(home, ".strike")
	src := t.TempDir()
	mixedLegacyBundle(t, src)

	inst, err := Install(context.Background(), InstallOptions{
		Scope:         ScopeGlobal,
		GlobalRoot:    gRoot,
		LocalPath:     src,
		StrikeVersion: "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	oldDigest := inst.Digest
	if _, err := Trust(TrustOptions{ID: inst.ID, Scope: ScopeGlobal, GlobalRoot: gRoot, StrikeVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	lf, err := ReadLockfile(filepath.Join(gRoot, LockfileName))
	if err != nil {
		t.Fatal(err)
	}
	if lf.Plugins[inst.ID].Trust == nil {
		t.Fatal("expected trust before migrate")
	}

	_, err = Migrate(MigrateOptions{
		Target: inst.ID, Scope: ScopeGlobal, GlobalRoot: gRoot, StrikeVersion: "0.2.0",
	})
	if !errors.Is(err, ErrNeedYes) {
		t.Fatalf("want ErrNeedYes, got %v", err)
	}
	m, _, err := ReadManifest(inst.Root)
	if err != nil || m.Format != FormatLegacy {
		t.Fatalf("without --yes tree should stay legacy: %+v %v", m, err)
	}

	res, err := Migrate(MigrateOptions{
		Target: inst.ID, Scope: ScopeGlobal, GlobalRoot: gRoot, StrikeVersion: "0.2.0", Confirm: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Digest == "" || res.Digest == oldDigest {
		t.Fatalf("digest should change: old=%s new=%s", oldDigest, res.Digest)
	}
	if !res.TrustCleared {
		t.Fatal("expected trust cleared")
	}
	if !strings.Contains(res.Review, "CLEARED") {
		t.Fatalf("review:\n%s", res.Review)
	}

	lf, err = ReadLockfile(filepath.Join(gRoot, LockfileName))
	if err != nil {
		t.Fatal(err)
	}
	e := lf.Plugins[inst.ID]
	if e.Digest != res.Digest {
		t.Fatalf("lock digest=%s want %s", e.Digest, res.Digest)
	}
	if e.Trust != nil {
		t.Fatalf("lock trust still set: %+v", e.Trust)
	}

	report, err := Doctor(DoctorOptions{ID: inst.ID, GlobalRoot: gRoot, StrikeVersion: "0.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Plugins) != 1 || report.Plugins[0].Format != FormatAPS {
		t.Fatalf("doctor format: %+v", report.Plugins)
	}
	for _, f := range report.Plugins[0].Findings {
		if f.Code == "deprecated" && strings.Contains(f.Message, "format=legacy") {
			t.Fatalf("doctor still reports legacy: %v", report.Plugins[0].Findings)
		}
	}
	text := FormatDoctorText(report)
	if !strings.Contains(text, "format:    aps") {
		t.Fatalf("doctor text:\n%s", text)
	}
}

func TestMigrate_FailedValidationLeavesInstall(t *testing.T) {
	home := t.TempDir()
	gRoot := filepath.Join(home, ".strike")
	src := t.TempDir()
	writePlugin(t, src, "acme.future", map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.future",
  "version": "1.0.0",
  "name": "Future",
  "strike": { "min": "99.0.0" },
  "contributions": { "agents": [{ "path": "agents/a.md" }] }
}`,
		"agents/a.md": validAgentMD("a"),
	})
	inst, err := Install(context.Background(), InstallOptions{
		Scope:         ScopeGlobal,
		GlobalRoot:    gRoot,
		LocalPath:     src,
		StrikeVersion: "99.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(inst.Root, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = Migrate(MigrateOptions{
		Target: inst.ID, Scope: ScopeGlobal, GlobalRoot: gRoot, StrikeVersion: "0.2.0", Confirm: true,
	})
	if err == nil || !strings.Contains(err.Error(), "validation failed") {
		t.Fatalf("want validation failed, got %v", err)
	}
	after, err := os.ReadFile(filepath.Join(inst.Root, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("failed migrate replaced the install")
	}
	m, _, err := ReadManifest(inst.Root)
	if err != nil || m.Format != FormatLegacy {
		t.Fatalf("want legacy remaining, got %+v %v", m, err)
	}
	lf, err := ReadLockfile(filepath.Join(gRoot, LockfileName))
	if err != nil {
		t.Fatal(err)
	}
	if lf.Plugins[inst.ID].Digest != inst.Digest {
		t.Fatalf("lockfile digest changed on failed migrate: %+v", lf.Plugins[inst.ID])
	}
	entries, _ := os.ReadDir(filepath.Join(gRoot, "plugins"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging") {
			t.Fatalf("staging leftover: %s", e.Name())
		}
	}
}

func TestMigrate_PathNotInstalledDoesNotNeedYes(t *testing.T) {
	src := t.TempDir()
	writePlugin(t, src, "acme.pack", map[string]string{
		"plugin.json": manifest("acme.pack", `{"agents":[{"path":"agents/a.md"}]}`),
		"agents/a.md": validAgentMD("a"),
	})
	res, err := Migrate(MigrateOptions{Target: src, StrikeVersion: "0.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || res.Installed {
		t.Fatalf("result: %+v", res)
	}
	m, _, err := ReadManifest(src)
	if err != nil || m.Format != FormatAPS {
		t.Fatalf("got %+v %v", m, err)
	}
}

func TestIsPortableAgentSkill(t *testing.T) {
	if !isPortableAgentSkill([]byte("---\ndescription: d\n---\nbody\n"), "ok") {
		t.Fatal("description-only should be portable")
	}
	if isPortableAgentSkill([]byte("---\ndescription: d\neffort: high\n---\nbody\n"), "ok") {
		t.Fatal("effort makes Strike-only extra")
	}
	if isPortableAgentSkill([]byte("no frontmatter\n"), "ok") {
		t.Fatal("missing frontmatter is not a valid Agent Skill")
	}
}

func TestMigrate_DirectorySkillOutsideSkillsRemaps(t *testing.T) {
	src := t.TempDir()
	writePlugin(t, src, "acme.pack", map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.pack",
  "version": "1.0.0",
  "name": "Pack",
  "strike": { "min": "0.1.0" },
  "contributions": { "skills": [{ "path": "vendor/ship-review/SKILL.md" }] }
}`,
		"vendor/ship-review/SKILL.md":      validSkillMD("ship-review"),
		"vendor/ship-review/scripts/do.sh": "#!/bin/sh\necho hi\n",
	})
	if _, err := Migrate(MigrateOptions{Target: src, StrikeVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	p, diags := LoadOne(src, ScopeGlobal, "0.2.0")
	if p == nil {
		t.Fatalf("load: %v", diags)
	}
	if len(p.Skills) != 1 || p.Skills[0].RelPath != "skills/ship-review/SKILL.md" {
		t.Fatalf("skills=%v", p.Skills)
	}
	if _, err := os.Stat(filepath.Join(src, "skills/ship-review/scripts/do.sh")); err != nil {
		t.Fatal(err)
	}
}

func TestMigrate_ProviderProfileNameRewritten(t *testing.T) {
	src := t.TempDir()
	writePlugin(t, src, "acme.pack", map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.pack",
  "version": "1.0.0",
  "name": "Pack",
  "strike": { "min": "0.1.0" },
  "contributions": { "providers": [{ "path": "providers/p.json", "profileName": "acme-proxy" }] }
}`,
		"providers/p.json": validProviderJSON("ignored"),
	})
	if _, err := Migrate(MigrateOptions{Target: src, StrikeVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(src, "com.strike.cli/providers/p.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"acme-proxy"`) || strings.Contains(string(raw), `"ignored"`) {
		t.Fatalf("provider json: %s", raw)
	}
}

func TestMigrate_MCPSSEMapsToStreamableHTTP(t *testing.T) {
	src := t.TempDir()
	writePlugin(t, src, "acme.pack", map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.pack",
  "version": "1.0.0",
  "name": "Pack",
  "strike": { "min": "0.1.0" },
  "contributions": {
    "mcp": [{ "name": "cloud", "transport": "sse", "url": "https://mcp.example.com/sse" }]
  }
}`,
	})
	if _, err := Migrate(MigrateOptions{Target: src, StrikeVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(src, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"streamable-http"`) || strings.Contains(string(raw), `"sse"`) {
		t.Fatalf("mcp.json: %s", raw)
	}
	p, diags := LoadOne(src, ScopeGlobal, "0.2.0")
	if p == nil || p.MCPCount != 1 {
		t.Fatalf("mcp count=%v diags=%v", p, diags)
	}
}

func TestMigrate_BasenameCollisionPreservesBoth(t *testing.T) {
	src := t.TempDir()
	writePlugin(t, src, "acme.pack", map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.pack",
  "version": "1.0.0",
  "name": "Pack",
  "strike": { "min": "0.1.0" },
  "contributions": {
    "agents": [
      { "path": "agents/a.md" },
      { "path": "extra/a.md" }
    ],
    "harnesses": [{ "name": "h1", "command": "bin/run" }],
    "hooks": [{ "event": "pre_tool_use", "type": "command", "command": "scripts/run" }]
  }
}`,
		"agents/a.md": validAgentMD("agents-a"),
		"extra/a.md":  validAgentMD("extra-a"),
		"bin/run":     "#!/bin/sh\necho bin\n",
		"scripts/run": "#!/bin/sh\necho scripts\n",
	})
	if _, err := Migrate(MigrateOptions{Target: src, StrikeVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	p, diags := LoadOne(src, ScopeGlobal, "0.2.0")
	if p == nil {
		t.Fatalf("load: %v", diags)
	}
	if len(p.Agents) != 2 {
		t.Fatalf("agents=%v", p.Agents)
	}
	seen := map[string]bool{}
	for _, a := range p.Agents {
		seen[filepath.Base(a.RelPath)] = true
	}
	if !seen["a.md"] || !seen["a-2.md"] {
		t.Fatalf("want unique agent dests, got %v", p.Agents)
	}
	binBody, err := os.ReadFile(filepath.Join(src, "com.strike.cli/bin/run"))
	if err != nil {
		t.Fatal(err)
	}
	scriptBody, err := os.ReadFile(filepath.Join(src, "com.strike.cli/bin/scripts/run"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(binBody), "echo bin") || !strings.Contains(string(scriptBody), "echo scripts") {
		t.Fatalf("binaries collided: %q %q", binBody, scriptBody)
	}
}

func TestMigrate_LockfileWriteFailureLeavesLegacy(t *testing.T) {
	home := t.TempDir()
	gRoot := filepath.Join(home, ".strike")
	src := t.TempDir()
	writePlugin(t, src, "acme.pack", map[string]string{
		"plugin.json": manifest("acme.pack", `{"agents":[{"path":"agents/a.md"}]}`),
		"agents/a.md": validAgentMD("a"),
	})
	inst, err := Install(context.Background(), InstallOptions{
		Scope:         ScopeGlobal,
		GlobalRoot:    gRoot,
		LocalPath:     src,
		StrikeVersion: "0.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeLockfileAfterSwap = func(string, Lockfile) error {
		return errors.New("injected lock write failure")
	}
	t.Cleanup(func() { writeLockfileAfterSwap = WriteLockfile })

	_, err = Migrate(MigrateOptions{
		Target: inst.ID, Scope: ScopeGlobal, GlobalRoot: gRoot, StrikeVersion: "0.2.0", Confirm: true,
	})
	if err == nil {
		t.Fatal("expected lockfile failure")
	}
	if !strings.Contains(err.Error(), "injected lock write failure") {
		t.Fatalf("want injected lock write error, got %v", err)
	}
	m, _, err := ReadManifest(inst.Root)
	if err != nil || m.Format != FormatLegacy {
		t.Fatalf("want legacy remaining after lockfile failure, got %+v %v", m, err)
	}
}

func TestMigrate_LeftoverUndeclaredSkillNotCopied(t *testing.T) {
	src := t.TempDir()
	writePlugin(t, src, "acme.pack", map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.pack",
  "version": "1.0.0",
  "name": "Pack",
  "strike": { "min": "0.1.0" },
  "contributions": { "skills": [{ "path": "skills/ship-review/SKILL.md" }] }
}`,
		"skills/ship-review/SKILL.md": validSkillMD("ship-review"),
		"skills/wip/SKILL.md":         validSkillMD("wip"),
		"mcp.json": `{
  "$schema": "https://raw.githubusercontent.com/anthropics/agent-plugins/v1.0.0/schemas/schema.json",
  "mcpServers": {
    "leftover": { "type": "stdio", "command": "echo" }
  }
}
`,
	})
	before, diags := LoadOne(src, ScopeGlobal, "0.2.0")
	if before == nil {
		t.Fatalf("legacy load: %v", diags)
	}
	if len(before.Skills) != 1 || before.Skills[0].RelPath != "skills/ship-review/SKILL.md" {
		t.Fatalf("legacy skills=%v", before.Skills)
	}

	dry, err := Migrate(MigrateOptions{Target: src, StrikeVersion: "0.2.0", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	plan := dry.Plan.Format()
	if !strings.Contains(plan, "skip undeclared leftover skill dir skills/wip") {
		t.Fatalf("dry-run missing leftover skip note:\n%s", plan)
	}

	if _, err := Migrate(MigrateOptions{Target: src, StrikeVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	after, diags := LoadOne(src, ScopeGlobal, "0.2.0")
	if after == nil {
		t.Fatalf("aps load: %v", diags)
	}
	if len(after.Skills) != 1 || after.Skills[0].RelPath != "skills/ship-review/SKILL.md" {
		t.Fatalf("want only declared skill, got %v", after.Skills)
	}
	if _, err := os.Stat(filepath.Join(src, "skills/wip/SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("undeclared leftover skill should not be copied")
	}
	if after.MCPCount != 0 {
		t.Fatalf("leftover mcp.json must not become APS MCP, got %d", after.MCPCount)
	}
}

func TestMigrate_HarnessNameCollisionUniquified(t *testing.T) {
	src := t.TempDir()
	writePlugin(t, src, "acme.pack", map[string]string{
		"plugin.json": `{
  "schemaVersion": 1,
  "id": "acme.pack",
  "version": "1.0.0",
  "name": "Pack",
  "strike": { "min": "0.1.0" },
  "capabilities": ["harnesses"],
  "contributions": {
    "harnesses": [
      { "name": "foo bar", "command": "bin/a" },
      { "name": "foo-bar", "command": "bin/b" }
    ]
  }
}`,
		"bin/a": "#!/bin/sh\necho a\n",
		"bin/b": "#!/bin/sh\necho b\n",
	})
	if _, err := Migrate(MigrateOptions{Target: src, StrikeVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(src, "com.strike.cli/harnesses/foo-bar.json"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(src, "com.strike.cli/harnesses/foo-bar-2.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), `"name": "foo bar"`) && !strings.Contains(string(second), `"name": "foo bar"`) {
		t.Fatalf("lost foo bar harness: %s %s", first, second)
	}
	if !strings.Contains(string(first), `"name": "foo-bar"`) && !strings.Contains(string(second), `"name": "foo-bar"`) {
		t.Fatalf("lost foo-bar harness: %s %s", first, second)
	}
	if strings.Contains(string(first), string(second)) {
		t.Fatal("harness files should not be identical")
	}
	a, err := os.ReadFile(filepath.Join(src, "com.strike.cli/bin/a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(src, "com.strike.cli/bin/b"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(a), "echo a") || !strings.Contains(string(b), "echo b") {
		t.Fatalf("binaries: %q %q", a, b)
	}
}
