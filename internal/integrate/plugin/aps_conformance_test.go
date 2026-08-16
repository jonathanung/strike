package plugin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Agent Plugins 1.0.0 client conformance (spec appendix A, working draft).
// This suite tracks the checklist; it is not a certification claim.

type apsAppendixItem struct {
	ID       string
	Bullet   string
	Coverage string // TestAPSConformance/<subtest> or client-N/A: …
}

// apsAppendixA is the Agent Plugins 1.0.0 appendix A checklist.
// Every bullet must be tested here or listed as client-N/A.
var apsAppendixA = []apsAppendixItem{
	{"A.loader.parse", "Parse and validate plugin.json (§5.1, §5.2)", "TestAPSConformance/parse_plugin_json"},
	{"A.loader.required", "Validate required $schema and name (§5.3)", "TestAPSConformance/required_schema_and_name"},
	{"A.loader.name", "Validate plugin name against naming constraints (§5.5)", "TestAPSConformance/name_constraints"},
	{"A.loader.unknown", "Report and ignore unknown plugin.json fields (§5.2)", "TestAPSConformance/unknown_top_level_fields"},
	{"A.loader.ext_ns", "Ignore unimplemented namespaces in extensions without validating values (§8.1)", "TestAPSConformance/ignore_unimplemented_extensions"},
	{"A.loader.ext_nonobj", "Ignore non-object extensions (§8.1)", "TestAPSConformance/ignore_non_object_extensions"},
	{"A.loader.confine", "Reject package paths that resolve outside the plugin root (§4.1)", "TestAPSConformance/reject_plugin_json_outside_root"},
	{"A.loader.ext_dir", "Discover implemented file-based extensions from top-level namespace directories (§8.2)", "TestAPSConformance/file_based_extensions"},
	{"A.discover.fixed", "Scan the fixed location for each supported component type (§6.1)", "TestAPSConformance/fixed_component_locations"},
	{"A.discover.missing", "Ignore missing fixed locations without error (§6.2)", "TestAPSConformance/missing_fixed_locations"},
	{"A.mcp.schema", "Select a supported $schema, then validate closed mcp.json and each server variant (§7.2.1)", "TestAPSConformance/mcp_schema_and_variants"},
	{"A.mcp.transport_min", "If supporting MCP, implement at least one of stdio or Streamable HTTP (§7.2.1)", "TestAPSConformance/mcp_stdio_and_http"},
	{"A.mcp.declared", "Use each server entry's declared transport for the initial connection attempt (§7.2.1)", "TestAPSConformance/mcp_declared_transport"},
	{"A.mcp.remote", "Enforce remote URL and literal-header requirements (§7.2.1)", "TestAPSConformance/mcp_url_and_headers"},
	{"A.mcp.sse", "SSE transport (§7.2.1 transport support)", "client-N/A: SSE is optional; Strike skips type=sse and continues"},
	{"A.env.plugin_dirs", "Provide PLUGIN_ROOT and a dedicated writable PLUGIN_DATA directory (§9.1)", "TestAPSConformance/plugin_root_and_data"},
	{"A.env.command", "Resolve MCP server command as a single bare or plugin-relative executable token (§7.2.1)", "TestAPSConformance/command_token"},
	{"A.env.default_cwd", "Use the plugin root as the default MCP server working directory (§7.2.1)", "TestAPSConformance/default_cwd"},
	{"A.env.cwd_forms", "Validate explicit cwd forms and post-resolution containment (§7.2.1)", "TestAPSConformance/cwd_forms_and_containment"},
	{"A.env.overlay", "Overlay configured env entries on a client-selected base environment (§9.1)", "TestAPSConformance/env_overlay_then_reserved"},
	{"A.env.reserved_last", "Set PLUGIN_ROOT and PLUGIN_DATA after applying configured env (§9.1)", "TestAPSConformance/env_overlay_then_reserved"},
	{"A.env.path", "Do not require configured PATH to affect bare-command resolution (§7.2.1)", "TestAPSConformance/bare_command_ignores_path"},
	{"A.env.expand", "Expand only ${PLUGIN_ROOT} and ${PLUGIN_DATA} in args, env, and cwd (§9.2)", "TestAPSConformance/placeholder_expansion_scope"},
	{"A.resilience.ignore_types", "Ignore unsupported component types (§11.3)", "TestAPSConformance/ignore_unsupported_component_types"},
	{"A.resilience.skip_transport", "Skip unsupported transport entries without affecting other servers or components (§7.2.2)", "TestAPSConformance/skip_unsupported_transport"},
	{"A.resilience.continue", "Continue loading when an independent component fails (§11.3)", "TestAPSConformance/continue_on_component_failure"},
	{"A.resilience.one_type", "Support at least one component type (§11.1)", "TestAPSConformance/supports_skills_and_mcp"},
}

func TestAPSConformanceChecklistCoverage(t *testing.T) {
	if len(apsAppendixA) == 0 {
		t.Fatal("appendix A mapping is empty")
	}
	subtests := apsConformanceSubtests(t)
	seen := map[string]struct{}{}
	for _, item := range apsAppendixA {
		if item.ID == "" || item.Bullet == "" || item.Coverage == "" {
			t.Errorf("incomplete mapping: %+v", item)
			continue
		}
		if _, ok := seen[item.ID]; ok {
			t.Errorf("duplicate checklist id %s", item.ID)
		}
		seen[item.ID] = struct{}{}
		if strings.HasPrefix(item.Coverage, "client-N/A:") {
			continue
		}
		const prefix = "TestAPSConformance/"
		if !strings.HasPrefix(item.Coverage, prefix) {
			t.Errorf("%s: coverage %q must be TestAPSConformance/<subtest> or client-N/A:", item.ID, item.Coverage)
			continue
		}
		name := strings.TrimPrefix(item.Coverage, prefix)
		if _, ok := subtests[name]; !ok {
			t.Errorf("%s: coverage subtest %q is not registered in TestAPSConformance", item.ID, name)
		}
	}
}

func apsConformanceSubtests(t *testing.T) map[string]struct{} {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "aps_conformance_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]struct{}{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestAPSConformance" || fn.Body == nil {
			continue
		}
		for _, stmt := range fn.Body.List {
			call, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			sel, ok := call.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			fun, ok := sel.Fun.(*ast.SelectorExpr)
			if !ok || fun.Sel.Name != "Run" || len(sel.Args) < 1 {
				continue
			}
			lit, ok := sel.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			name, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			out[name] = struct{}{}
		}
	}
	if len(out) == 0 {
		t.Fatal("no TestAPSConformance subtests found")
	}
	return out
}

func TestAPSConformance(t *testing.T) {
	t.Run("parse_plugin_json", testAPSParsePluginJSON)
	t.Run("required_schema_and_name", testAPSRequiredSchemaAndName)
	t.Run("name_constraints", testAPSNameConstraints)
	t.Run("unknown_top_level_fields", testAPSUnknownTopLevelFields)
	t.Run("ignore_unimplemented_extensions", testAPSIgnoreUnimplementedExtensions)
	t.Run("ignore_non_object_extensions", testAPSIgnoreNonObjectExtensions)
	t.Run("reject_plugin_json_outside_root", testAPSRejectPluginJSONOutsideRoot)
	t.Run("file_based_extensions", testAPSFileBasedExtensions)
	t.Run("fixed_component_locations", testAPSFixedComponentLocations)
	t.Run("missing_fixed_locations", testAPSMissingFixedLocations)
	t.Run("mcp_schema_and_variants", testAPSMCPSchemaAndVariants)
	t.Run("mcp_stdio_and_http", testAPSMCPStdioAndHTTP)
	t.Run("mcp_declared_transport", testAPSMCPDeclaredTransport)
	t.Run("mcp_url_and_headers", testAPSMCPURLAndHeaders)
	t.Run("plugin_root_and_data", testAPSPluginRootAndData)
	t.Run("command_token", testAPSCommandToken)
	t.Run("default_cwd", testAPSDefaultCwd)
	t.Run("cwd_forms_and_containment", testAPSCWDFormsAndContainment)
	t.Run("env_overlay_then_reserved", testAPSEnvOverlayThenReserved)
	t.Run("bare_command_ignores_path", testAPSBareCommandIgnoresPATH)
	t.Run("placeholder_expansion_scope", testAPSPlaceholderExpansionScope)
	t.Run("ignore_unsupported_component_types", testAPSIgnoreUnsupportedComponentTypes)
	t.Run("skip_unsupported_transport", testAPSSkipUnsupportedTransport)
	t.Run("continue_on_component_failure", testAPSContinueOnComponentFailure)
	t.Run("supports_skills_and_mcp", testAPSSupportsSkillsAndMCP)
	t.Run("aps_does_not_require_contributions", testAPSDoesNotRequireContributions)
	t.Run("no_schema_network_fetch", testAPSNoSchemaNetworkFetch)
}

func testAPSParsePluginJSON(t *testing.T) {
	opts, _ := installAPSFixture(t, "portable-ok")
	res := Discover(opts)
	p := mustAPSPlugin(t, res, "cf.portable")
	if p.Manifest.Format != FormatAPS {
		t.Fatalf("format=%s", p.Manifest.Format)
	}
	if p.Manifest.Schema != APSPluginSchemaV1 {
		t.Fatalf("$schema=%s", p.Manifest.Schema)
	}
	if p.Manifest.SchemaVersion != 0 {
		t.Fatalf("legacy schemaVersion leaked into APS: %d", p.Manifest.SchemaVersion)
	}
}

func testAPSRequiredSchemaAndName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		err  string
	}{
		{"missing_name", `{"$schema":"` + APSPluginSchemaV1 + `"}`, "name"},
		{"empty_name", `{"$schema":"` + APSPluginSchemaV1 + `","name":""}`, "name"},
		{"missing_schema", `{"name":"cf.ok"}`, ""},
		{"empty_schema", `{"$schema":"","name":"cf.ok"}`, ""},
		{"wrong_type_name", `{"$schema":"` + APSPluginSchemaV1 + `","name":1}`, "name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.raw))
			if err == nil {
				t.Fatal("expected reject")
			}
			if tc.err != "" && !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("want %q in %v", tc.err, err)
			}
		})
	}
}

func testAPSNameConstraints(t *testing.T) {
	t.Parallel()
	valid := []string{"a", "my-plugin", "acme.tools", "lint3r", "a.b-c.d9"}
	invalid := []string{
		"",
		"My-Plugin",
		"-start",
		"end-",
		"has--double",
		"too.many..dots",
		".hidden",
		"has space",
		"under_score",
		strings.Repeat("a", 65),
	}
	for _, name := range valid {
		raw := `{"$schema":"` + APSPluginSchemaV1 + `","name":"` + name + `"}`
		m, err := ParseManifest([]byte(raw))
		if err != nil {
			t.Errorf("valid %q: %v", name, err)
			continue
		}
		if m.ID != name || m.Format != FormatAPS {
			t.Errorf("valid %q: id=%s format=%s", name, m.ID, m.Format)
		}
	}
	for _, name := range invalid {
		raw := `{"$schema":"` + APSPluginSchemaV1 + `","name":"` + name + `"}`
		if _, err := ParseManifest([]byte(raw)); err == nil {
			t.Errorf("invalid %q: expected error", name)
		}
	}
}

func testAPSUnknownTopLevelFields(t *testing.T) {
	opts, _ := installAPSFixture(t, "unknown-fields")
	res := Discover(opts)
	p := mustAPSPlugin(t, res, "cf.unknown")
	if len(p.Skills) != 1 || !strings.HasSuffix(p.Skills[0].RelPath, "skills/keep/SKILL.md") {
		t.Fatalf("skills=%+v diags=%v", p.Skills, res.Diagnostics)
	}
	if !diagContains(res.Diagnostics, func(d Diagnostic) bool {
		return d.Code == "unknown_field" && strings.Contains(d.Message, "extra")
	}) {
		t.Fatalf("expected unknown_field for extra: %v", res.Diagnostics)
	}
	if !diagContains(res.Diagnostics, func(d Diagnostic) bool {
		return d.Code == "unknown_field" && strings.Contains(d.Message, "contributions")
	}) {
		t.Fatalf("expected unknown_field for contributions: %v", res.Diagnostics)
	}
}

func testAPSIgnoreUnimplementedExtensions(t *testing.T) {
	opts, _ := installAPSFixture(t, "extensions-foreign")
	res := Discover(opts)
	p := mustAPSPlugin(t, res, "cf.ext-foreign")
	if len(p.Skills) != 1 {
		t.Fatalf("want portable skill, got %+v diags=%v", p.Skills, res.Diagnostics)
	}
	if len(p.Agents) != 0 || len(p.Workflows) != 0 {
		t.Fatalf("foreign extension dir must not load: agents=%d workflows=%d", len(p.Agents), len(p.Workflows))
	}
	if p.Manifest.StrikeCLI != nil {
		t.Fatalf("unimplemented namespace must not populate StrikeCLI: %+v", p.Manifest.StrikeCLI)
	}
}

func testAPSIgnoreNonObjectExtensions(t *testing.T) {
	opts, _ := installAPSFixture(t, "extensions-non-object")
	res := Discover(opts)
	p := mustAPSPlugin(t, res, "cf.ext-nonobj")
	if len(p.Skills) != 1 {
		t.Fatalf("plugin should continue loading, skills=%+v diags=%v", p.Skills, res.Diagnostics)
	}
	if !diagContains(res.Diagnostics, func(d Diagnostic) bool {
		return strings.Contains(d.Message, "extensions") && strings.Contains(d.Message, "not an object")
	}) {
		t.Fatalf("expected non-object extensions diagnostic: %v", res.Diagnostics)
	}
}

func testAPSRejectPluginJSONOutsideRoot(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "plugin.json"), []byte(apsManifest("cf.escape")), 0o644); err != nil {
		t.Fatal(err)
	}
	gRoot := filepath.Join(home, ".strike")
	root := filepath.Join(gRoot, "plugins", "cf.escape")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "plugin.json"), filepath.Join(root, "plugin.json")); err != nil {
		t.Skip("symlinks not supported")
	}
	res := Discover(Options{GlobalRoot: gRoot, StrikeVersion: "0.2.0"})
	if len(res.Plugins) != 0 {
		t.Fatalf("escaped plugin.json must reject plugin, got %+v", res.Plugins)
	}
	if !diagContains(res.Diagnostics, func(d Diagnostic) bool {
		return strings.Contains(d.Message, "plugin.json") && (d.Code == "path" || d.Code == "malformed" || strings.Contains(d.Message, "escapes"))
	}) {
		t.Fatalf("expected escape diagnostic: %v", res.Diagnostics)
	}

	t.Run("skill_symlink_escape", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte(validSkillMD("evil")), 0o644); err != nil {
			t.Fatal(err)
		}
		gRoot := filepath.Join(home, ".strike")
		root := filepath.Join(gRoot, "plugins", "cf.skill-escape")
		writePlugin(t, root, "cf.skill-escape", map[string]string{
			"plugin.json":        apsManifest("cf.skill-escape"),
			"skills/ok/SKILL.md": validSkillMD("ok"),
			"skills/evil/ignore": "x",
		})
		if err := os.Remove(filepath.Join(root, "skills", "evil", "ignore")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "SKILL.md"), filepath.Join(root, "skills", "evil", "SKILL.md")); err != nil {
			t.Skip("symlinks not supported")
		}
		res := Discover(Options{GlobalRoot: gRoot, StrikeVersion: "0.2.0"})
		p := mustAPSPlugin(t, res, "cf.skill-escape")
		if len(p.Skills) != 1 || !strings.Contains(p.Skills[0].RelPath, "skills/ok/") {
			t.Fatalf("escaped SKILL.md must be skipped, got %+v diags=%v", p.Skills, res.Diagnostics)
		}
	})
}

func testAPSFileBasedExtensions(t *testing.T) {
	opts, _ := installAPSFixture(t, "portable-ok")
	res := Discover(opts)
	p := mustAPSPlugin(t, res, "cf.portable")
	if len(p.Agents) != 0 {
		t.Fatalf("missing com.strike.cli/ must not invent agents: %+v", p.Agents)
	}

	home := t.TempDir()
	gRoot := filepath.Join(home, ".strike")
	root := filepath.Join(gRoot, "plugins", "cf.strike-ext")
	writePlugin(t, root, "cf.strike-ext", map[string]string{
		"plugin.json":                    apsManifest("cf.strike-ext"),
		"skills/port/SKILL.md":           validSkillMD("port"),
		"com.strike.cli/agents/extra.md": validAgentMD("extra"),
	})
	res = Discover(Options{GlobalRoot: gRoot, StrikeVersion: "0.2.0"})
	p = mustAPSPlugin(t, res, "cf.strike-ext")
	if len(p.Agents) != 1 {
		t.Fatalf("implemented namespace directory must be discovered, agents=%+v diags=%v", p.Agents, res.Diagnostics)
	}
}

func testAPSFixedComponentLocations(t *testing.T) {
	opts, _ := installAPSFixture(t, "portable-ok")
	res := Discover(opts)
	p := mustAPSPlugin(t, res, "cf.portable")
	if len(p.Skills) != 1 || !strings.HasSuffix(p.Skills[0].RelPath, "skills/summarize/SKILL.md") {
		t.Fatalf("skills from skills/: %+v", p.Skills)
	}
	if p.MCPCount != 2 {
		t.Fatalf("MCPCount from mcp.json=%d diags=%v", p.MCPCount, res.Diagnostics)
	}
}

func testAPSMissingFixedLocations(t *testing.T) {
	opts, _ := installAPSFixture(t, "skills-absent")
	res := Discover(opts)
	p := mustAPSPlugin(t, res, "cf.skills-absent")
	if len(p.Skills) != 0 {
		t.Fatalf("missing skills/ must be empty, got %+v", p.Skills)
	}
	if p.MCPCount != 0 {
		t.Fatalf("missing mcp.json must not error, MCPCount=%d diags=%v", p.MCPCount, res.Diagnostics)
	}
	if diagContains(res.Diagnostics, func(d Diagnostic) bool {
		return d.PluginID == "cf.skills-absent" && d.Severity == SeverityError &&
			(strings.Contains(d.Message, "skills") || strings.Contains(d.Message, "mcp.json"))
	}) {
		t.Fatalf("missing locations must not be errors: %v", res.Diagnostics)
	}
}

func testAPSMCPSchemaAndVariants(t *testing.T) {
	opts, _ := installAPSFixture(t, "mcp-schema-mismatch")
	res := Discover(opts)
	p := mustAPSPlugin(t, res, "cf.mcp-mismatch")
	if len(p.Skills) != 1 {
		t.Fatalf("mismatched mcp $schema must disable MCP only, skills=%+v diags=%v", p.Skills, res.Diagnostics)
	}
	if p.MCPCount != 0 {
		t.Fatalf("MCPCount=%d", p.MCPCount)
	}
	if !diagContains(res.Diagnostics, func(d Diagnostic) bool {
		return d.Path == "mcp.json" && (d.Code == "schema_version" || strings.Contains(d.Message, "MCP disabled"))
	}) {
		t.Fatalf("expected MCP disable diagnostic: %v", res.Diagnostics)
	}

	opts, _ = installAPSFixture(t, "mcp-extra-field")
	res = Discover(opts)
	p = mustAPSPlugin(t, res, "cf.mcp-extra")
	if len(p.Skills) != 1 {
		t.Fatalf("unknown mcp.json field must disable MCP only, skills=%+v diags=%v", p.Skills, res.Diagnostics)
	}
	if p.MCPCount != 0 {
		t.Fatalf("closed mcp.json extra field MCPCount=%d", p.MCPCount)
	}
	if !diagContains(res.Diagnostics, func(d Diagnostic) bool {
		return d.Path == "mcp.json" && strings.Contains(d.Message, "extra") && strings.Contains(d.Message, "MCP disabled")
	}) {
		t.Fatalf("expected unknown top-level mcp.json field diagnostic: %v", res.Diagnostics)
	}

	opts, _ = installAPSFixture(t, "mcp-mixed")
	res = Discover(opts)
	p = mustAPSPlugin(t, res, "cf.mcp-mixed")
	if len(p.Skills) != 1 {
		t.Fatalf("skills should remain, got %+v", p.Skills)
	}
	set := trustCompile(t, opts, "cf.mcp-mixed")
	byName := mcpByName(set.MCP)
	if _, ok := byName["goodStdio"]; !ok {
		t.Fatalf("good stdio missing: %+v diags=%v", set.MCP, set.Diagnostics)
	}
	if _, ok := byName["goodHTTP"]; !ok {
		t.Fatalf("good http missing: %+v", set.MCP)
	}
	if _, ok := byName["bareOk"]; !ok {
		t.Fatalf("bare command missing: %+v", set.MCP)
	}
	for _, skipped := range []string{"legacySSE", "unknownType", "badCommand", "escapeCmd", "badHTTP", "userinfo", "fragment", "dupHeaders", "badCwd"} {
		if _, ok := byName[skipped]; ok {
			t.Fatalf("server %s should be skipped: %+v", skipped, set.MCP)
		}
	}
}

func testAPSMCPStdioAndHTTP(t *testing.T) {
	opts, _ := installAPSFixture(t, "portable-ok")
	set := trustCompile(t, opts, "cf.portable")
	byName := mcpByName(set.MCP)
	if byName["local"].Transport != "stdio" {
		t.Fatalf("local transport=%s", byName["local"].Transport)
	}
	if byName["cloud"].Transport != "http" || byName["cloud"].URL != "https://mcp.example.com/v1" {
		t.Fatalf("cloud=%+v", byName["cloud"])
	}
}

func testAPSMCPDeclaredTransport(t *testing.T) {
	opts, _ := installAPSFixture(t, "portable-ok")
	set := trustCompile(t, opts, "cf.portable")
	byName := mcpByName(set.MCP)
	if byName["local"].Transport != "stdio" || byName["local"].URL != "" {
		t.Fatalf("stdio entry must keep stdio: %+v", byName["local"])
	}
	if byName["cloud"].Transport != "http" || byName["cloud"].Command != "" {
		t.Fatalf("streamable-http must stay http, not fall back to stdio: %+v", byName["cloud"])
	}
}

func testAPSMCPURLAndHeaders(t *testing.T) {
	opts, _ := installAPSFixture(t, "mcp-mixed")
	res := Discover(opts)
	mustAPSPlugin(t, res, "cf.mcp-mixed")
	set := trustCompile(t, opts, "cf.mcp-mixed")
	byName := mcpByName(set.MCP)
	if _, ok := byName["badHTTP"]; ok {
		t.Fatal("non-loopback http must be skipped")
	}
	if _, ok := byName["userinfo"]; ok {
		t.Fatal("url userinfo must be skipped")
	}
	if _, ok := byName["fragment"]; ok {
		t.Fatal("url fragment must be skipped")
	}
	if _, ok := byName["dupHeaders"]; ok {
		t.Fatal("duplicate header names must be skipped")
	}
	if !diagContains(res.Diagnostics, func(d Diagnostic) bool {
		return strings.Contains(d.Message, "dupHeaders")
	}) {
		t.Fatalf("expected dupHeaders skip diagnostic on Discover: %v", res.Diagnostics)
	}
}

func testAPSPluginRootAndData(t *testing.T) {
	opts, root := installAPSFixture(t, "portable-ok")
	set := trustCompile(t, opts, "cf.portable")
	local := mcpByName(set.MCP)["local"]
	wantRoot := canonPath(t, root)
	if local.Env["PLUGIN_ROOT"] != wantRoot {
		t.Fatalf("PLUGIN_ROOT=%q want %q", local.Env["PLUGIN_ROOT"], wantRoot)
	}
	dataDir := local.Env["PLUGIN_DATA"]
	if dataDir == "" || !strings.Contains(dataDir, "plugin-data") {
		t.Fatalf("PLUGIN_DATA=%q", dataDir)
	}
	if st, err := os.Stat(dataDir); err != nil || !st.IsDir() {
		t.Fatalf("PLUGIN_DATA must be created writable dir: %v", err)
	}
	probe := filepath.Join(dataDir, "conformance-write")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		t.Fatalf("PLUGIN_DATA not writable: %v", err)
	}
}

func testAPSCommandToken(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "server.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveAPSCommand(root, "npx")
	if err != nil || got != "npx" {
		t.Fatalf("bare: %q %v", got, err)
	}
	got, err = resolveAPSCommand(root, "./bin/server.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "bin/server.sh") && !strings.HasSuffix(got, `bin\server.sh`) {
		t.Fatalf("relative command=%s", got)
	}
	if _, err := resolveAPSCommand(root, "bin/server.sh"); err == nil {
		t.Fatal("missing ./ must fail")
	}
	if _, err := resolveAPSCommand(root, "/usr/bin/npx"); err == nil {
		t.Fatal("absolute must fail")
	}
	if _, err := resolveAPSCommand(root, "${PLUGIN_ROOT}/bin/server.sh"); err == nil {
		t.Fatal("placeholder in command must not resolve")
	}

	opts, _ := installAPSFixture(t, "mcp-mixed")
	set := trustCompile(t, opts, "cf.mcp-mixed")
	byName := mcpByName(set.MCP)
	if _, ok := byName["badCommand"]; ok {
		t.Fatal("multi-token stdio command must be skipped")
	}
	if _, ok := byName["escapeCmd"]; ok {
		t.Fatal("./command that escapes the plugin root must be skipped")
	}
	if byName["bareOk"].Command != "npx" {
		t.Fatalf("bare command=%s", byName["bareOk"].Command)
	}
}

func testAPSDefaultCwd(t *testing.T) {
	opts, root := installAPSFixture(t, "mcp-default-cwd")
	set := trustCompile(t, opts, "cf.mcp-cwd")
	local := mcpByName(set.MCP)["local"]
	if canonPath(t, local.Cwd) != canonPath(t, root) {
		t.Fatalf("default cwd=%s want plugin root %s", local.Cwd, root)
	}
}

func testAPSCWDFormsAndContainment(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "plugin-data", "x")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(data, "newdir")
	cases := []struct {
		cwd  string
		want string
	}{
		{"./", root},
		{"./subdir", sub},
		{"./subdir/", sub},
		{"${PLUGIN_ROOT}", root},
		{"${PLUGIN_ROOT}/subdir", sub},
		{"${PLUGIN_DATA}", data},
		{"${PLUGIN_DATA}/newdir", missing},
	}
	for _, tc := range cases {
		got, err := resolveAPSCWD(root, data, tc.cwd)
		if err != nil {
			t.Errorf("cwd %q: %v", tc.cwd, err)
			continue
		}
		if canonPath(t, got) != canonPath(t, tc.want) {
			t.Errorf("cwd %q = %s want %s", tc.cwd, got, tc.want)
		}
	}
	if err := validateAPSCWDForm("data"); err == nil {
		t.Fatal("cwd without ./ must be invalid")
	}
	if err := validateAPSCWDForm("../out"); err == nil {
		t.Fatal("cwd ../ must be invalid")
	}
	if _, err := resolveAPSCWD(root, data, "${PLUGIN_ROOT}/../outside"); err == nil {
		t.Fatal("cwd escaping plugin root must fail")
	}
	if _, err := resolveAPSCWD(root, data, "${PLUGIN_DATA}/../outside"); err == nil {
		t.Fatal("cwd escaping PLUGIN_DATA must fail")
	}

	opts, _ := installAPSFixture(t, "mcp-mixed")
	set := trustCompile(t, opts, "cf.mcp-mixed")
	if _, ok := mcpByName(set.MCP)["badCwd"]; ok {
		t.Fatal("invalid cwd form must skip server")
	}
}

func testAPSEnvOverlayThenReserved(t *testing.T) {
	opts, root := installAPSFixture(t, "portable-ok")
	set := trustCompile(t, opts, "cf.portable")
	local := mcpByName(set.MCP)["local"]
	if local.Env["PLAIN"] != "ok" {
		t.Fatalf("configured env lost: %+v", local.Env)
	}
	if !strings.HasSuffix(filepath.ToSlash(local.Env["CONFIG"]), "config.json") {
		t.Fatalf("CONFIG=%q", local.Env["CONFIG"])
	}
	wantRoot := canonPath(t, root)
	if local.Env["PLUGIN_ROOT"] != wantRoot {
		t.Fatalf("PLUGIN_ROOT after overlay=%q", local.Env["PLUGIN_ROOT"])
	}
	if local.Env["PLUGIN_DATA"] == "" {
		t.Fatal("PLUGIN_DATA missing after overlay")
	}

	opts, _ = installAPSFixture(t, "mcp-reserved-env")
	res := Discover(opts)
	mustAPSPlugin(t, res, "cf.mcp-reserved")
	set = trustCompile(t, opts, "cf.mcp-reserved")
	byName := mcpByName(set.MCP)
	if _, ok := byName["badRoot"]; ok {
		t.Fatal("env PLUGIN_ROOT in mcp.json must skip that server")
	}
	okSrv, ok := byName["ok"]
	if !ok {
		t.Fatalf("sibling server should load: %+v diags=%v", set.MCP, set.Diagnostics)
	}
	if okSrv.Env["PLUGIN_ROOT"] == "" || okSrv.Env["PLUGIN_DATA"] == "" {
		t.Fatalf("client must set reserved env: %+v", okSrv.Env)
	}
}

func testAPSBareCommandIgnoresPATH(t *testing.T) {
	t.Setenv("PATH", "/definitely-not-a-real-path")
	got, err := resolveAPSCommand(t.TempDir(), "npx")
	if err != nil || got != "npx" {
		t.Fatalf("bare command must stay a token without PATH search at resolve: %q %v", got, err)
	}
}

func testAPSPlaceholderExpansionScope(t *testing.T) {
	opts, root := installAPSFixture(t, "mcp-placeholders")
	set := trustCompile(t, opts, "cf.mcp-expand")
	byName := mcpByName(set.MCP)
	local := byName["local"]
	pluginRoot := canonPath(t, root)
	dataDir := local.Env["PLUGIN_DATA"]
	if len(local.Args) != 3 {
		t.Fatalf("args=%#v", local.Args)
	}
	if local.Args[0] != pluginRoot+"/a" && local.Args[0] != filepath.Join(pluginRoot, "a") {
		if !strings.Contains(local.Args[0], "a") || strings.Contains(local.Args[0], "${PLUGIN_ROOT}") {
			t.Fatalf("args[0] not expanded: %q", local.Args[0])
		}
	}
	if strings.Contains(local.Args[1], "${PLUGIN_DATA}") {
		t.Fatalf("args[1] not expanded: %q", local.Args[1])
	}
	if local.Args[2] != "${UNRECOGNIZED}" {
		t.Fatalf("unrecognized placeholder must stay literal: %q", local.Args[2])
	}
	if local.Env["ROOT"] != pluginRoot {
		t.Fatalf("env ROOT=%q", local.Env["ROOT"])
	}
	if local.Env["DATA"] != dataDir {
		t.Fatalf("env DATA=%q", local.Env["DATA"])
	}
	if local.Env["NESTED"] != pluginRoot+dataDir {
		t.Fatalf("non-recursive expansion: NESTED=%q", local.Env["NESTED"])
	}
	if strings.Contains(local.Command, "${") {
		t.Fatalf("command must not expand placeholders: %s", local.Command)
	}
	remote := byName["remote"]
	if remote.URL != "https://example.com/${PLUGIN_ROOT}" {
		t.Fatalf("url must not expand placeholders: %s", remote.URL)
	}
	if remote.Headers["X-Root"] != "${PLUGIN_DATA}" {
		t.Fatalf("headers must not expand placeholders: %+v", remote.Headers)
	}
}

func testAPSIgnoreUnsupportedComponentTypes(t *testing.T) {
	opts, _ := installAPSFixture(t, "portable-ok")
	res := Discover(opts)
	p := mustAPSPlugin(t, res, "cf.portable")
	if p.Manifest.Format != FormatAPS || len(p.Skills) != 1 || p.MCPCount != 2 {
		t.Fatalf("skills+MCP must still load, got skills=%d mcp=%d diags=%v", len(p.Skills), p.MCPCount, res.Diagnostics)
	}
	if len(p.Agents) != 0 || len(p.Workflows) != 0 {
		t.Fatalf("root agents/ and commands/ are not v1 portable types: agents=%+v workflows=%+v", p.Agents, p.Workflows)
	}
}

func testAPSSkipUnsupportedTransport(t *testing.T) {
	opts, _ := installAPSFixture(t, "mcp-mixed")
	res := Discover(opts)
	p := mustAPSPlugin(t, res, "cf.mcp-mixed")
	if len(p.Skills) != 1 {
		t.Fatalf("sse skip must not drop skills: %+v", p.Skills)
	}
	if !diagContains(res.Diagnostics, func(d Diagnostic) bool {
		return d.Code == "unsupported_transport" && strings.Contains(d.Message, "legacySSE")
	}) {
		t.Fatalf("expected sse skip diagnostic: %v", res.Diagnostics)
	}
	set := trustCompile(t, opts, "cf.mcp-mixed")
	byName := mcpByName(set.MCP)
	if _, ok := byName["legacySSE"]; ok {
		t.Fatal("sse must not be compiled")
	}
	if _, ok := byName["unknownType"]; ok {
		t.Fatal("unknown transport must not be compiled")
	}
	if _, ok := byName["goodStdio"]; !ok {
		t.Fatal("supported stdio must still compile")
	}
}

func testAPSContinueOnComponentFailure(t *testing.T) {
	opts, _ := installAPSFixture(t, "skills-layout")
	res := Discover(opts)
	p := mustAPSPlugin(t, res, "cf.skills-layout")
	if len(p.Skills) != 1 || !strings.Contains(p.Skills[0].RelPath, "skills/ok/") {
		t.Fatalf("only immediate valid skill, got %+v diags=%v", p.Skills, res.Diagnostics)
	}
	if !diagContains(res.Diagnostics, func(d Diagnostic) bool {
		return strings.Contains(d.Message, "skipping invalid skill")
	}) {
		t.Fatalf("expected invalid skill skip: %v", res.Diagnostics)
	}

	opts, _ = installAPSFixture(t, "skills-not-dir")
	res = Discover(opts)
	p = mustAPSPlugin(t, res, "cf.skills-file")
	if len(p.Skills) != 0 {
		t.Fatalf("skills file must skip skills type, got %+v", p.Skills)
	}
	if p.MCPCount != 1 {
		t.Fatalf("MCP must continue, MCPCount=%d diags=%v", p.MCPCount, res.Diagnostics)
	}

	opts, _ = installAPSFixture(t, "mcp-not-file")
	res = Discover(opts)
	p = mustAPSPlugin(t, res, "cf.mcp-dir")
	if len(p.Skills) != 1 {
		t.Fatalf("invalid mcp.json kind must keep skills, got %+v diags=%v", p.Skills, res.Diagnostics)
	}
	if p.MCPCount != 0 {
		t.Fatalf("MCPCount=%d", p.MCPCount)
	}
}

func testAPSSupportsSkillsAndMCP(t *testing.T) {
	opts, _ := installAPSFixture(t, "portable-ok")
	res := Discover(opts)
	p := mustAPSPlugin(t, res, "cf.portable")
	if len(p.Skills) == 0 {
		t.Fatal("Strike must support portable skills")
	}
	if p.MCPCount == 0 {
		t.Fatal("Strike must support portable MCP")
	}
	set := trustCompile(t, opts, "cf.portable")
	if len(set.MCP) == 0 {
		t.Fatalf("trusted MCP must compile: %v", set.Diagnostics)
	}
}

func testAPSDoesNotRequireContributions(t *testing.T) {
	opts, _ := installAPSFixture(t, "portable-ok")
	res := Discover(opts)
	p := mustAPSPlugin(t, res, "cf.portable")
	if len(p.Manifest.Contributions.Agents)+len(p.Manifest.Contributions.Skills) != 0 {
		t.Fatalf("APS package must not require Strike contributions, got %+v", p.Manifest.Contributions)
	}

	opts, _ = installAPSFixture(t, "contributions-ignored")
	res = Discover(opts)
	p = mustAPSPlugin(t, res, "cf.contrib-ignored")
	if len(p.Skills) != 1 || !strings.Contains(p.Skills[0].RelPath, "skills/portable/") {
		t.Fatalf("must discover skills/ not contributions: %+v diags=%v", p.Skills, res.Diagnostics)
	}
	for _, s := range p.Skills {
		if strings.Contains(s.RelPath, "from-contrib") {
			t.Fatalf("must not load Strike contributions on APS: %+v", p.Skills)
		}
	}
}

func testAPSNoSchemaNetworkFetch(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		switch name {
		case "download.go", "catalog.go", "git.go", "install.go", "update.go":
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == "net/http" {
				t.Errorf("%s imports net/http; plugin load must not fetch $schema", name)
			}
		}
	}
	if _, err := ParseManifest([]byte(`{"$schema":"` + APSPluginSchemaV1 + `","name":"cf.offline"}`)); err != nil {
		t.Fatalf("canonical $schema must validate locally: %v", err)
	}
}

func installAPSFixture(t *testing.T, name string) (Options, string) {
	t.Helper()
	src := filepath.Join("testdata", "aps", name)
	m, _, err := ReadManifest(src)
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	home := t.TempDir()
	gRoot := filepath.Join(home, ".strike")
	dest := filepath.Join(gRoot, "plugins", m.ID)
	if err := copyTree(src, dest); err != nil {
		t.Fatal(err)
	}
	return Options{GlobalRoot: gRoot, StrikeVersion: "0.2.0"}, dest
}

func trustCompile(t *testing.T, opts Options, id string) ExecutableSet {
	t.Helper()
	if _, err := Trust(TrustOptions{ID: id, GlobalRoot: opts.GlobalRoot, StrikeVersion: opts.StrikeVersion}); err != nil {
		t.Fatal(err)
	}
	return CompileExecutables(opts, nil, nil)
}

func mustAPSPlugin(t *testing.T, res Result, id string) Plugin {
	t.Helper()
	for _, p := range res.Plugins {
		if p.ID == id {
			if p.Manifest.Format != FormatAPS {
				t.Fatalf("plugin %s format=%s (APS required)", id, p.Manifest.Format)
			}
			return p
		}
	}
	t.Fatalf("plugin %s not loaded; plugins=%d diags=%v", id, len(res.Plugins), res.Diagnostics)
	return Plugin{}
}

func diagContains(diags []Diagnostic, pred func(Diagnostic) bool) bool {
	for _, d := range diags {
		if pred(d) {
			return true
		}
	}
	return false
}

func mcpByName(list []CompiledMCP) map[string]CompiledMCP {
	out := map[string]CompiledMCP{}
	for _, m := range list {
		out[m.Name] = m
	}
	return out
}

func canonPath(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return resolveExistingPrefix(abs)
}
