package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const apsSchema = APSPluginSchemaV1

func apsManifest(name string) string {
	return `{
  "$schema": "` + apsSchema + `",
  "name": "` + name + `",
  "version": "1.0.0"
}`
}

func TestParseManifest_APSMinimal(t *testing.T) {
	m, err := ParseManifest([]byte(apsManifest("minimal-plugin")))
	if err != nil {
		t.Fatal(err)
	}
	if m.Format != FormatAPS || m.ID != "minimal-plugin" || m.Name != "minimal-plugin" {
		t.Fatalf("got format=%s id=%s name=%s", m.Format, m.ID, m.Name)
	}
	if m.SchemaVersion != 0 {
		t.Fatalf("schemaVersion should be unset for APS, got %d", m.SchemaVersion)
	}
}

func TestParseManifest_APSRejectsMissingName(t *testing.T) {
	raw := `{"$schema":"` + apsSchema + `"}`
	if _, err := ParseManifest([]byte(raw)); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("want name error, got %v", err)
	}
}

func TestParseManifest_APSRejectsMissingSchema(t *testing.T) {
	raw := `{"name":"minimal-plugin"}`
	if _, err := ParseManifest([]byte(raw)); err == nil {
		t.Fatal("expected reject")
	}
}

func TestParseManifest_APSUnknownTopLevelContinues(t *testing.T) {
	raw := `{
  "$schema": "` + apsSchema + `",
  "name": "acme.tools",
  "extra": true
}`
	m, diags, err := parseManifestBytes([]byte(raw), "plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "acme.tools" {
		t.Fatalf("id=%s", m.ID)
	}
	var found bool
	for _, d := range diags {
		if d.Code == "unknown_field" && strings.Contains(d.Message, "extra") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected unknown_field diagnostic, got %v", diags)
	}
}

func TestParseManifest_APSInvalidName(t *testing.T) {
	for _, name := range []string{"My-Plugin", "-start", "has--double", "too.many..dots", ""} {
		raw := `{"$schema":"` + apsSchema + `","name":"` + name + `"}`
		if _, err := ParseManifest([]byte(raw)); err == nil {
			t.Errorf("%q: expected error", name)
		}
	}
}

func TestParseManifest_APSAcceptsSingleCharName(t *testing.T) {
	m, err := ParseManifest([]byte(`{"$schema":"` + apsSchema + `","name":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "a" {
		t.Fatalf("id=%s", m.ID)
	}
}

func TestParseManifest_APSUnsupportedSchemaVersion(t *testing.T) {
	raw := `{"$schema":"https://agent-plugins.org/schemas/2.0.0/plugin.schema.json","name":"x"}`
	_, err := ParseManifest([]byte(raw))
	if err == nil {
		t.Fatal("expected unsupported schema error")
	}
	var unsupp unsupportedSchemaError
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("got %v", err)
	}
	_ = unsupp
}

func TestParseManifest_APSJSONCRejected(t *testing.T) {
	raw := `{
  // comment
  "$schema": "` + apsSchema + `",
  "name": "acme.tools"
}`
	if _, err := ParseManifest([]byte(raw)); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("want JSON-only error, got %v", err)
	}
}

func TestParseManifest_LegacyStillStrictUnknown(t *testing.T) {
	raw := `{
  "schemaVersion": 1,
  "id": "acme.pack",
  "version": "1.0.0",
  "name": "Pack",
  "strike": { "min": "0.2.0" },
  "contributions": { "agents": [{ "path": "agents/a.md" }] },
  "extra": true
}`
	if _, err := ParseManifest([]byte(raw)); err == nil {
		t.Fatal("legacy unknown field should still reject")
	}
}

func TestDiscover_APSSkillsImmediateOnly(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.skills")
	writePlugin(t, root, "acme.skills", map[string]string{
		"plugin.json":                 apsManifest("acme.skills"),
		"skills/foo/SKILL.md":         validSkillMD("foo"),
		"skills/nested/deep/SKILL.md": validSkillMD("deep"),
		"skills/flat.md":              validSkillMD("flat"),
	})
	res := Discover(Options{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if len(res.Plugins) != 1 {
		t.Fatalf("plugins=%d diags=%v", len(res.Plugins), res.Diagnostics)
	}
	p := res.Plugins[0]
	if p.ID != "acme.skills" || len(p.Skills) != 1 {
		t.Fatalf("want one skill foo, got %+v", p.Skills)
	}
	if !strings.HasSuffix(p.Skills[0].RelPath, "skills/foo/SKILL.md") {
		t.Fatalf("rel=%s", p.Skills[0].RelPath)
	}
}

func TestDiscover_APSMissingStrikeMinLoads(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.skills")
	writePlugin(t, root, "acme.skills", map[string]string{
		"plugin.json":         apsManifest("acme.skills"),
		"skills/foo/SKILL.md": validSkillMD("foo"),
	})
	res := Discover(Options{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if len(res.Plugins) != 1 {
		t.Fatalf("want load without strike.min, got diags=%v", res.Diagnostics)
	}
}

func TestDiscover_APSUnsupportedSchemaSkipped(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "future")
	writePlugin(t, root, "future", map[string]string{
		"plugin.json": `{"$schema":"https://agent-plugins.org/schemas/9.0.0/plugin.schema.json","name":"future"}`,
	})
	res := Discover(Options{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if len(res.Plugins) != 0 {
		t.Fatalf("want skip, got %+v", res.Plugins)
	}
	var found bool
	for _, d := range res.Diagnostics {
		if d.Code == "schema_version" {
			found = true
		}
	}
	if !found {
		t.Fatalf("diags: %v", res.Diagnostics)
	}
}

func TestDiscover_LegacyEmitsFormatLegacy(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.pack")
	writePlugin(t, root, "acme.pack", map[string]string{
		"plugin.json": manifest("acme.pack", `{"agents":[{"path":"agents/a.md"}]}`),
		"agents/a.md": validAgentMD("a"),
	})
	res := Discover(Options{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if len(res.Plugins) != 1 {
		t.Fatalf("got %+v %v", res.Plugins, res.Diagnostics)
	}
	var found bool
	for _, d := range res.Diagnostics {
		if d.Code == "deprecated" && strings.Contains(d.Message, "format=legacy") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected format=legacy diagnostic: %v", res.Diagnostics)
	}
}

func TestDiscover_LegacyJSONCStillLoads(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.pack")
	writePlugin(t, root, "acme.pack", map[string]string{
		"plugin.jsonc": `{
  // comment
  "schemaVersion": 1,
  "id": "acme.pack",
  "version": "1.0.0",
  "name": "Pack",
  "strike": { "min": "0.1.0" },
  "contributions": { "agents": [{ "path": "agents/a.md" }] }
}`,
		"agents/a.md": validAgentMD("a"),
	})
	res := Discover(Options{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if len(res.Plugins) != 1 || res.Plugins[0].ID != "acme.pack" {
		t.Fatalf("got %+v diags=%v", res.Plugins, res.Diagnostics)
	}
}

func writeAPSExecPlugin(t *testing.T, root string) {
	t.Helper()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "mcp.sh"), []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	writePlugin(t, root, "acme.tools", map[string]string{
		"plugin.json": apsManifest("acme.tools"),
		"mcp.json": `{
  "$schema": "` + APSMCPSchemaV1 + `",
  "mcpServers": {
    "acmeLint": {
      "type": "stdio",
      "command": "./bin/mcp.sh",
      "args": ["--data", "${PLUGIN_DATA}/lint", "--root", "${PLUGIN_ROOT}/cfg"],
      "env": {
        "CONFIG": "${PLUGIN_ROOT}/config.json",
        "OTHER": "plain"
      },
      "cwd": "${PLUGIN_ROOT}"
    },
    "legacyEvents": {
      "type": "sse",
      "url": "https://legacy.example.com/sse"
    },
    "cloud": {
      "type": "streamable-http",
      "url": "https://mcp.example.com/acme"
    }
  }
}`,
		"skills/foo/SKILL.md": validSkillMD("foo"),
	})
}

func TestCompileExecutables_APSUntrustedBlocksMCP(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.tools")
	writeAPSExecPlugin(t, root)

	set := CompileExecutables(Options{
		GlobalRoot:    filepath.Join(home, ".strike"),
		StrikeVersion: "0.2.0",
	}, nil, nil)
	if len(set.MCP) != 0 {
		t.Fatalf("untrusted MCP compiled: %+v", set.MCP)
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
}

func TestCompileExecutables_APSTrustedExpandsAndSetsEnv(t *testing.T) {
	home := t.TempDir()
	gRoot := filepath.Join(home, ".strike")
	root := filepath.Join(gRoot, "plugins", "acme.tools")
	writeAPSExecPlugin(t, root)

	if _, err := Trust(TrustOptions{
		ID:            "acme.tools",
		GlobalRoot:    gRoot,
		StrikeVersion: "0.2.0",
	}); err != nil {
		t.Fatal(err)
	}

	set := CompileExecutables(Options{
		GlobalRoot:    gRoot,
		StrikeVersion: "0.2.0",
	}, nil, nil)
	byName := map[string]CompiledMCP{}
	for _, m := range set.MCP {
		byName[m.Name] = m
	}
	if _, ok := byName["legacyEvents"]; ok {
		t.Fatal("sse transport should be skipped")
	}
	cloud, ok := byName["cloud"]
	if !ok || cloud.Transport != "http" || cloud.URL != "https://mcp.example.com/acme" {
		t.Fatalf("cloud = %+v diags=%v", cloud, set.Diagnostics)
	}
	lint, ok := byName["acmeLint"]
	if !ok {
		t.Fatalf("missing acmeLint: %+v diags=%v", set.MCP, set.Diagnostics)
	}
	pluginRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		pluginRoot = root
	}
	pluginRoot, err = filepath.Abs(pluginRoot)
	if err != nil {
		t.Fatal(err)
	}
	if lint.Command != filepath.Join(pluginRoot, "bin", "mcp.sh") && !strings.HasSuffix(lint.Command, "bin/mcp.sh") {
		t.Fatalf("command=%s", lint.Command)
	}
	dataDir := filepath.Join(gRoot, "plugin-data", "acme.tools")
	if lint.Env["PLUGIN_ROOT"] != pluginRoot && lint.Env["PLUGIN_ROOT"] == "" {
		t.Fatalf("PLUGIN_ROOT missing: %+v", lint.Env)
	}
	if lint.Env["PLUGIN_DATA"] == "" || !strings.Contains(lint.Env["PLUGIN_DATA"], "plugin-data") {
		t.Fatalf("PLUGIN_DATA=%q", lint.Env["PLUGIN_DATA"])
	}
	if lint.Env["OTHER"] != "plain" {
		t.Fatalf("overlay env lost: %+v", lint.Env)
	}
	if !strings.Contains(lint.Env["CONFIG"], "config.json") || strings.Contains(lint.Env["CONFIG"], "${PLUGIN_ROOT}") {
		t.Fatalf("CONFIG not expanded: %q", lint.Env["CONFIG"])
	}
	if len(lint.Args) != 4 || strings.Contains(lint.Args[1], "${PLUGIN_DATA}") || strings.Contains(lint.Args[3], "${PLUGIN_ROOT}") {
		t.Fatalf("args not expanded: %#v", lint.Args)
	}
	if lint.Cwd == "" {
		t.Fatal("cwd should default/expand to plugin root")
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("PLUGIN_DATA dir not created: %v", err)
	}
}

func TestCompileExecutables_APSTrustedSkipDiagsOnce(t *testing.T) {
	home := t.TempDir()
	gRoot := filepath.Join(home, ".strike")
	root := filepath.Join(gRoot, "plugins", "acme.tools")
	writeAPSExecPlugin(t, root)
	if _, err := Trust(TrustOptions{ID: "acme.tools", GlobalRoot: gRoot, StrikeVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	set := CompileExecutables(Options{GlobalRoot: gRoot, StrikeVersion: "0.2.0"}, nil, nil)
	n := 0
	for _, d := range set.Diagnostics {
		if d.Code == "unsupported_transport" && strings.Contains(d.Message, "legacyEvents") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want 1 sse skip diagnostic after trust, got %d: %v", n, set.Diagnostics)
	}
}

func TestCompileExecutables_APSCommandCannotEscape(t *testing.T) {
	home := t.TempDir()
	gRoot := filepath.Join(home, ".strike")
	root := filepath.Join(gRoot, "plugins", "acme.tools")
	writePlugin(t, root, "acme.tools", map[string]string{
		"plugin.json": apsManifest("acme.tools"),
		"mcp.json": `{
  "$schema": "` + APSMCPSchemaV1 + `",
  "mcpServers": {
    "escape": {
      "type": "stdio",
      "command": "./../outside"
    }
  }
}`,
	})
	if _, err := Trust(TrustOptions{ID: "acme.tools", GlobalRoot: gRoot, StrikeVersion: "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	set := CompileExecutables(Options{GlobalRoot: gRoot, StrikeVersion: "0.2.0"}, nil, nil)
	if len(set.MCP) != 0 {
		t.Fatalf("escaped command compiled: %+v", set.MCP)
	}
	var pathErr bool
	for _, d := range set.Diagnostics {
		if d.Code == "path" || strings.Contains(d.Message, "..") || strings.Contains(d.Message, "command") {
			pathErr = true
		}
	}
	if !pathErr {
		t.Fatalf("expected path/command diagnostic: %v", set.Diagnostics)
	}
}

func TestResolveAPSCommand_BareAndRelative(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "x"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveAPSCommand(root, "npx")
	if err != nil || got != "npx" {
		t.Fatalf("bare: %q %v", got, err)
	}
	got, err = resolveAPSCommand(root, "./bin/x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "bin/x") && !strings.HasSuffix(got, `bin\x`) {
		t.Fatalf("rel=%s", got)
	}
	if _, err := resolveAPSCommand(root, "bin/x"); err == nil {
		t.Fatal("missing ./ should fail")
	}
	if _, err := resolveAPSCommand(root, "/usr/bin/npx"); err == nil {
		t.Fatal("absolute should fail")
	}
}

func TestExpandPluginPlaceholders_NonRecursive(t *testing.T) {
	got := expandPluginPlaceholders("a ${PLUGIN_ROOT} b ${PLUGIN_DATA} c", "/root", "/data")
	if got != "a /root b /data c" {
		t.Fatalf("got %q", got)
	}
	got = expandPluginPlaceholders("${PLUGIN_ROOT}${PLUGIN_DATA}", "${PLUGIN_DATA}", "x")
	if got != "${PLUGIN_DATA}x" {
		t.Fatalf("recursive scan of replacement: %q", got)
	}
}

func TestValidatePluginKey_Union(t *testing.T) {
	if err := ValidatePluginKey("a"); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePluginKey("acme.review-pack"); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePluginKey("My-Plugin"); err == nil {
		t.Fatal("expected error")
	}
}

func TestDiscover_APSInvalidMCPJSONDiagnostic(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.tools")
	writePlugin(t, root, "acme.tools", map[string]string{
		"plugin.json":         apsManifest("acme.tools"),
		"skills/foo/SKILL.md": validSkillMD("foo"),
		"mcp.json": `{
  "$schema": "https://example.com/not-aps.json",
  "mcpServers": { "x": { "type": "stdio", "command": "echo" } }
}`,
	})
	res := Discover(Options{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if len(res.Plugins) != 1 {
		t.Fatalf("plugin should still load: plugins=%d diags=%v", len(res.Plugins), res.Diagnostics)
	}
	var found bool
	for _, d := range res.Diagnostics {
		if d.Path == "mcp.json" && (d.Code == "schema_version" || strings.Contains(d.Message, "MCP disabled")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected mcp.json diagnostic: %v", res.Diagnostics)
	}
	if res.Plugins[0].MCPCount != 0 {
		t.Fatalf("MCPCount=%d", res.Plugins[0].MCPCount)
	}
}

func TestDoctor_APSSKILLMDNamesDoNotCollideWithinPlugin(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.skills")
	writePlugin(t, root, "acme.skills", map[string]string{
		"plugin.json":         apsManifest("acme.skills"),
		"skills/foo/SKILL.md": validSkillMD("foo"),
		"skills/bar/SKILL.md": validSkillMD("bar"),
	})
	report, err := Doctor(DoctorOptions{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range report.Findings {
		if f.Code == "collision" {
			t.Fatalf("false collision: %+v", f)
		}
	}
	if contributionPublicName("skills/foo/SKILL.md") != "foo" {
		t.Fatalf("public name=%s", contributionPublicName("skills/foo/SKILL.md"))
	}
}

func TestDoctor_APSInvalidMCPShowsDisabled(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".strike", "plugins", "acme.tools")
	writePlugin(t, root, "acme.tools", map[string]string{
		"plugin.json": apsManifest("acme.tools"),
		"mcp.json":    `{"$schema":"https://example.com/not-aps.json","mcpServers":{}}`,
	})
	report, err := Doctor(DoctorOptions{GlobalRoot: filepath.Join(home, ".strike"), StrikeVersion: "0.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Plugins) != 1 {
		t.Fatalf("plugins=%d", len(report.Plugins))
	}
	mcp := report.Plugins[0].Contributions.MCP
	if len(mcp) != 1 || mcp[0].Name != "(disabled)" {
		t.Fatalf("want MCP disabled, got %+v", mcp)
	}
}

func TestBuildUpdateReview_APSMCPVisible(t *testing.T) {
	oldRoot := t.TempDir()
	writePlugin(t, oldRoot, "acme.tools", map[string]string{
		"plugin.json":         apsManifest("acme.tools"),
		"skills/foo/SKILL.md": validSkillMD("foo"),
	})
	newRoot := t.TempDir()
	writeAPSExecPlugin(t, newRoot)
	oldMan, _, err := ReadManifest(oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	newMan, _, err := ReadManifest(newRoot)
	if err != nil {
		t.Fatal(err)
	}
	old := InstalledPlugin{
		ID:       "acme.tools",
		Version:  "1.0.0",
		Root:     oldRoot,
		Manifest: &oldMan,
	}
	src := SourceIdentity{Type: SourceLocal}
	rev := BuildUpdateReview(old, newMan, src, "sha256:"+strings.Repeat("c", 64), newRoot)
	text := rev.Format()
	if !rev.ExecutableChanged {
		t.Fatalf("expected APS mcp.json to count as executable change:\n%s", text)
	}
	if !strings.Contains(text, "mcp:") {
		t.Fatalf("expected mcp fingerprint in review:\n%s", text)
	}
	var hasMCP bool
	for _, tpe := range rev.ContribAdded {
		if tpe == "mcp" {
			hasMCP = true
		}
	}
	if !hasMCP {
		t.Fatalf("expected types + mcp, got added=%v text=\n%s", rev.ContribAdded, text)
	}
}
