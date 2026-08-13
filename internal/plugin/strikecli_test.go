package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func apsManifestExt(name, strikeCLIJSON string) string {
	return `{
  "$schema": "` + apsSchema + `",
  "name": "` + name + `",
  "version": "1.0.0",
  "extensions": {
    "com.other.client": {"ignored": true},
    "com.strike.cli": ` + strikeCLIJSON + `
  }
}`
}

func TestParseManifest_APSUnknownStrikeCLIKeyIgnored(t *testing.T) {
	raw := apsManifestExt("acme.tools", `{
      "displayName": "Acme Tools",
      "futureThing": true
    }`)
	m, diags, err := parseManifestBytes([]byte(raw), "plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "Acme Tools" {
		t.Fatalf("displayName not applied: %s", m.Name)
	}
	if m.StrikeCLI == nil || m.StrikeCLI.SkipContributions {
		t.Fatalf("unknown key must not skip contributions: %+v", m.StrikeCLI)
	}
	var found bool
	for _, d := range diags {
		if d.Code == "unknown_field" && strings.Contains(d.Message, "futureThing") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected unknown_field for futureThing, got %v", diags)
	}
}

func TestParseManifest_APSInvalidStrikeCLITypeSkipsContributions(t *testing.T) {
	raw := apsManifestExt("acme.tools", `{"displayName": 1}`)
	m, diags, err := parseManifestBytes([]byte(raw), "plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	if m.StrikeCLI == nil || !m.StrikeCLI.SkipContributions {
		t.Fatalf("want SkipContributions, got %+v diags=%v", m.StrikeCLI, diags)
	}
	var found bool
	for _, d := range diags {
		if d.Code == "malformed" && strings.Contains(d.Message, "skipping Strike-only") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected skip diagnostic, got %v", diags)
	}
}

func TestDiscover_APSStrikeCLIPassiveSurfaces(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.review")
	writePlugin(t, root, "acme.review", map[string]string{
		"plugin.json": apsManifestExt("acme.review", `{
      "displayName": "Acme Review",
      "strike": {"min": "0.1.0"},
      "capabilities": ["agents", "themes"]
    }`),
		"skills/ship-review/SKILL.md":               validSkillMD("ship-review"),
		"com.strike.cli/agents/reviewer.md":         validAgentMD("reviewer"),
		"com.strike.cli/workflows/review-gate.json": validWorkflowJSON("review-gate"),
		"com.strike.cli/themes/acme-dark.json":      validThemeJSON("acme-dark"),
		"com.strike.cli/providers/acme-proxy.json":  validProviderJSON("acme-proxy"),
		"com.strike.cli/skills/extra.md":            validSkillMD("extra"),
		"com.strike.cli/skills/ship-review.md":      validSkillMD("ship-review-flat"),
		"agents/legacy-top.md":                      validAgentMD("legacy-top"),
		"com.other.client/agents/foreign.md":        validAgentMD("foreign"),
	})
	res := Discover(Options{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if len(res.Plugins) != 1 {
		t.Fatalf("plugins=%d diags=%v", len(res.Plugins), res.Diagnostics)
	}
	p := res.Plugins[0]
	if p.Name != "Acme Review" {
		t.Fatalf("displayName=%s", p.Name)
	}
	if len(p.Agents) != 1 || p.Agents[0].RelPath != "com.strike.cli/agents/reviewer.md" {
		t.Fatalf("agents=%+v", p.Agents)
	}
	if p.Agents[0].PluginID != "acme.review" {
		t.Fatalf("provenance plugin=%s", p.Agents[0].PluginID)
	}
	if len(p.Workflows) != 1 || !strings.Contains(p.Workflows[0].RelPath, "review-gate.json") {
		t.Fatalf("workflows=%+v", p.Workflows)
	}
	if len(p.Themes) != 1 {
		t.Fatalf("themes=%+v", p.Themes)
	}
	if len(p.Providers) != 1 {
		t.Fatalf("providers=%+v", p.Providers)
	}
	// Portable skill + extra; colliding Strike-flat ship-review skipped.
	if len(p.Skills) != 2 {
		t.Fatalf("skills=%+v diags=%v", p.Skills, res.Diagnostics)
	}
	var names []string
	for _, s := range p.Skills {
		names = append(names, contributionPublicName(s.RelPath))
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "ship-review") || !strings.Contains(joined, "extra") {
		t.Fatalf("skill names=%v", names)
	}
	var skipFlat bool
	for _, d := range res.Diagnostics {
		if d.Code == "collision" && strings.Contains(d.Message, "ship-review") {
			skipFlat = true
		}
	}
	if !skipFlat {
		t.Fatalf("expected portable-wins diagnostic: %v", res.Diagnostics)
	}
}

func TestDiscover_APSNoStrikeCLIDirValid(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.skills")
	writePlugin(t, root, "acme.skills", map[string]string{
		"plugin.json":         apsManifest("acme.skills"),
		"skills/foo/SKILL.md": validSkillMD("foo"),
	})
	res := Discover(Options{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if len(res.Plugins) != 1 {
		t.Fatalf("portable-only APS should load, diags=%v", res.Diagnostics)
	}
	p := res.Plugins[0]
	if len(p.Agents) != 0 || len(p.Themes) != 0 || len(p.Skills) != 1 {
		t.Fatalf("want skills only, got agents=%d themes=%d skills=%d", len(p.Agents), len(p.Themes), len(p.Skills))
	}
}

func TestDiscover_APSInvalidStrikeCLITypeKeepsPortable(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.tools")
	writePlugin(t, root, "acme.tools", map[string]string{
		"plugin.json":                       apsManifestExt("acme.tools", `{"capabilities": "nope"}`),
		"skills/foo/SKILL.md":               validSkillMD("foo"),
		"com.strike.cli/agents/reviewer.md": validAgentMD("reviewer"),
	})
	res := Discover(Options{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if len(res.Plugins) != 1 {
		t.Fatalf("plugin skipped: %v", res.Diagnostics)
	}
	p := res.Plugins[0]
	if len(p.Skills) != 1 {
		t.Fatalf("portable skill missing: %+v", p.Skills)
	}
	if len(p.Agents) != 0 {
		t.Fatalf("Strike-only agents should be skipped: %+v", p.Agents)
	}
}

func TestDiscover_APSStrikeCLIDirEscapeRejected(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.tools")
	writePlugin(t, root, "acme.tools", map[string]string{
		"plugin.json": apsManifest("acme.tools"),
	})
	outside := filepath.Join(home, "outside")
	if err := os.MkdirAll(filepath.Join(outside, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "agents", "x.md"), []byte(validAgentMD("x")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, strikeCLIDir)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	res := Discover(Options{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if len(res.Plugins) != 1 {
		t.Fatalf("plugin should still load: diags=%v", res.Diagnostics)
	}
	if len(res.Plugins[0].Agents) != 0 {
		t.Fatalf("escaped agents loaded: %+v", res.Plugins[0].Agents)
	}
	var pathDiag bool
	for _, d := range res.Diagnostics {
		if d.Code == "path" && strings.Contains(d.Path, strikeCLIDir) {
			pathDiag = true
		}
	}
	if !pathDiag {
		t.Fatalf("expected path diagnostic, got %v", res.Diagnostics)
	}
}

func TestDiscover_APSIgnoresOtherNamespaces(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.tools")
	writePlugin(t, root, "acme.tools", map[string]string{
		"plugin.json": `{
  "$schema": "` + apsSchema + `",
  "name": "acme.tools",
  "version": "1.0.0",
  "extensions": {
    "com.zed.editor": {"agents": ["nope"]}
  }
}`,
		"com.zed.editor/agents/zed.md": validAgentMD("zed"),
	})
	res := Discover(Options{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if len(res.Plugins) != 1 {
		t.Fatalf("got %+v %v", res.Plugins, res.Diagnostics)
	}
	if len(res.Plugins[0].Agents) != 0 {
		t.Fatalf("foreign namespace loaded: %+v", res.Plugins[0].Agents)
	}
}

func writeAPSStrikeCLIExec(t *testing.T, root string) {
	t.Helper()
	writePlugin(t, root, "acme.ext", map[string]string{
		"plugin.json": apsManifest("acme.ext"),
		"com.strike.cli/harnesses/choose.json": `{
  "name": "acme-choose",
  "command": "com.strike.cli/bin/choose",
  "args": []
}`,
		"com.strike.cli/hooks/pre-bash.json": `{
  "event": "pre_tool_use",
  "matcher": "bash",
  "type": "command",
  "command": "com.strike.cli/bin/hook.sh"
}`,
		"com.strike.cli/hooks/rule.json": `{
  "event": "pre_tool_use",
  "action": "deny",
  "message": "blocked"
}`,
		"com.strike.cli/panes/board.json": `{
  "schemaVersion": 1,
  "id": "acme.board",
  "title": "Board",
  "mode": "process",
  "command": "com.strike.cli/bin/board",
  "permissions": {"host": [], "fs": "none", "network": "none", "command": "none"}
}`,
		"com.strike.cli/bin/choose":  "#!/bin/sh\necho choose\n",
		"com.strike.cli/bin/hook.sh": "#!/bin/sh\necho hook\n",
		"com.strike.cli/bin/board":   "#!/bin/sh\necho board\n",
	})
}

func TestCompileExecutables_APSStrikeCLIUntrusted(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.ext")
	writeAPSStrikeCLIExec(t, root)

	set := CompileExecutables(Options{
		GlobalRoot:    filepath.Join(home, ".strike"),
		StrikeVersion: "0.2.0",
	}, nil, nil)
	if len(set.Harnesses) != 0 {
		t.Fatalf("untrusted harness: %+v", set.Harnesses)
	}
	var shell, rule int
	for _, h := range append(set.GlobalHooks, set.ProjectHooks...) {
		if h.Command != "" {
			shell++
		}
		if h.Action != "" {
			rule++
		}
	}
	if shell != 0 {
		t.Fatalf("untrusted shell hooks=%d", shell)
	}
	if rule != 1 {
		t.Fatalf("declarative hooks=%d want 1", rule)
	}
	var untrusted bool
	for _, d := range set.Diagnostics {
		if d.Code == "executable_untrusted" {
			untrusted = true
		}
	}
	if !untrusted {
		t.Fatalf("expected executable_untrusted: %v", set.Diagnostics)
	}
	m := Manifest{Format: FormatAPS}
	if !HasExecutableContributionsAt(m, root) {
		t.Fatal("extension harness/hook/process pane should count as executable")
	}
	if !HasProcessPanes(m, root) {
		t.Fatal("expected process pane")
	}
}

func TestCompileExecutables_APSStrikeCLITrusted(t *testing.T) {
	home := t.TempDir()
	gRoot := filepath.Join(home, ".strike")
	root := filepath.Join(gRoot, "plugins", "acme.ext")
	writeAPSStrikeCLIExec(t, root)

	if _, err := Trust(TrustOptions{
		ID:            "acme.ext",
		GlobalRoot:    gRoot,
		StrikeVersion: "0.2.0",
	}); err != nil {
		t.Fatal(err)
	}
	set := CompileExecutables(Options{
		GlobalRoot:    gRoot,
		StrikeVersion: "0.2.0",
	}, nil, nil)
	if len(set.Harnesses) != 1 || set.Harnesses[0].Name != "acme-choose" {
		t.Fatalf("harnesses=%+v diags=%v", set.Harnesses, set.Diagnostics)
	}
	if !strings.Contains(set.Harnesses[0].Command, "com.strike.cli") {
		t.Fatalf("command not confined: %s", set.Harnesses[0].Command)
	}
	var shell int
	for _, h := range set.GlobalHooks {
		if h.Command != "" {
			shell++
		}
	}
	if shell != 1 {
		t.Fatalf("trusted shell hooks=%d hooks=%+v", shell, set.GlobalHooks)
	}
}

func TestDiscover_LegacyStillUsesContributions(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.pack")
	writePlugin(t, root, "acme.pack", map[string]string{
		"plugin.json":                      manifest("acme.pack", `{"agents":[{"path":"agents/a.md"}]}`),
		"agents/a.md":                      validAgentMD("a"),
		"com.strike.cli/agents/ignored.md": validAgentMD("ignored"),
	})
	res := Discover(Options{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if len(res.Plugins) != 1 || len(res.Plugins[0].Agents) != 1 {
		t.Fatalf("legacy should use contributions only: %+v %v", res.Plugins, res.Diagnostics)
	}
	if res.Plugins[0].Agents[0].RelPath != "agents/a.md" {
		t.Fatalf("rel=%s", res.Plugins[0].Agents[0].RelPath)
	}
}
