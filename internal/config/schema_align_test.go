package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// schemaPath resolves schemas/strike-config.schema.json from this test file
// (worktree or primary checkout).
func schemaPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// internal/config → repo root
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	p := filepath.Join(root, "schemas", "strike-config.schema.json")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("schema file missing at %s: %v", p, err)
	}
	return p
}

// jsonFieldNames returns exported json object keys for struct type t
// (skips "-" tags and non-exported fields). Nested structs are not walked.
func jsonFieldNames(t reflect.Type) []string {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}

func loadSchemaRoot(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(schemaPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("schema JSON: %v", err)
	}
	return root
}

func schemaProps(t *testing.T, obj map[string]any) map[string]any {
	t.Helper()
	props, ok := obj["properties"].(map[string]any)
	if !ok || props == nil {
		t.Fatal("schema missing properties object")
	}
	return props
}

func schemaDefs(t *testing.T, root map[string]any) map[string]any {
	t.Helper()
	defs, ok := root["$defs"].(map[string]any)
	if !ok || defs == nil {
		t.Fatal("schema missing $defs")
	}
	return defs
}

func defProps(t *testing.T, defs map[string]any, name string) map[string]any {
	t.Helper()
	def, ok := defs[name].(map[string]any)
	if !ok {
		t.Fatalf("$defs.%s missing or not an object", name)
	}
	return schemaProps(t, def)
}

// TestStrikeConfigSchemaAlign ensures the published JSON Schema stays roughly
// aligned with Config and nested JSON shapes (issue #873). Best-effort — not
// full codegen. Runtime still ignores $schema (no fetch).
func TestStrikeConfigSchemaAlign(t *testing.T) {
	root := loadSchemaRoot(t)

	if root["additionalProperties"] != true {
		t.Fatalf("root additionalProperties: want true (runtime ignores unknown keys), got %#v", root["additionalProperties"])
	}

	id, _ := root["$id"].(string)
	wantID := "https://raw.githubusercontent.com/jonathanung/strike/main/schemas/strike-config.schema.json"
	if id != wantID {
		t.Fatalf("$id = %q, want %q", id, wantID)
	}

	props := schemaProps(t, root)
	if _, ok := props["$schema"]; !ok {
		t.Fatal("properties must document optional $schema (editor DX; ignored at load)")
	}

	// Top-level Config json tags must appear in schema properties.
	for _, name := range jsonFieldNames(reflect.TypeOf(Config{})) {
		if _, ok := props[name]; !ok {
			t.Errorf("schema missing Config field %q", name)
		}
	}

	// disable-default-* is loaded outside struct tags.
	if _, ok := props["disable-default-providers"]; !ok {
		t.Error("schema missing disable-default-providers")
	}
	pp, ok := root["patternProperties"].(map[string]any)
	if !ok || len(pp) == 0 {
		t.Fatal("schema missing patternProperties for disable-default-<name>")
	}

	defs := schemaDefs(t, root)

	// Nested high-traffic shapes.
	checks := []struct {
		def  string
		typ  any
		note string
	}{
		{"network", NetworkConfig{}, ""},
		{"webSearch", WebSearchConfig{}, ""},
		{"session", SessionConfig{}, ""},
		{"agentBudget", AgentBudgetConfig{}, ""},
		{"toolRetry", ToolRetryConfig{}, ""},
		{"hook", Hook{}, ""},
		{"mcp", MCPConfig{}, ""},
		{"mcpServer", MCPServer{}, ""},
		{"lsp", LSPConfig{}, ""},
		{"lspServer", LSPServer{}, ""},
		{"harness", HarnessConfig{}, ""},
		{"scheduler", SchedulerConfig{}, ""},
		{"customProvider", CustomProvider{}, "ModelDefs is json:\"-\""},
	}
	for _, tc := range checks {
		t.Run("def/"+tc.def, func(t *testing.T) {
			dp := defProps(t, defs, tc.def)
			for _, name := range jsonFieldNames(reflect.TypeOf(tc.typ)) {
				if _, ok := dp[name]; !ok {
					t.Errorf("$defs.%s missing field %q", tc.def, name)
				}
			}
			def := defs[tc.def].(map[string]any)
			if def["additionalProperties"] != true {
				t.Errorf("$defs.%s additionalProperties: want true, got %#v", tc.def, def["additionalProperties"])
			}
		})
	}

	// permissionRule is permission.Rule (external package) — check keys by hand.
	pr := defProps(t, defs, "permissionRule")
	for _, name := range []string{"permission", "pattern", "action"} {
		if _, ok := pr[name]; !ok {
			t.Errorf("permissionRule missing %q", name)
		}
	}
}

// TestStrikeConfigSchemaExampleRoundTrip loads the docs-style example shape
// through the real config parser (JSONC + $schema ignored).
func TestStrikeConfigSchemaExampleRoundTrip(t *testing.T) {
	raw := []byte(`{
  // editor hint
  "$schema": "https://raw.githubusercontent.com/jonathanung/strike/main/schemas/strike-config.schema.json",
  "provider": "echo",
  "model": "echo",
  "effort": "low",
  "leanCode": "lite",
  "deferTools": "on",
  "permissionMode": "default",
  "sandbox": "workspace-write",
  "network": { "allow": ["api.github.com"] },
  "permissionPreset": "dev",
  "permissions": [
    { "permission": "bash", "pattern": "go *", "action": "allow" }
  ],
  "hooks": [
    { "event": "pre_tool_use", "matcher": "bash", "action": "log" }
  ],
  "session": {
    "worktree": "off",
    "agentBudget": { "maxToolCalls": 10 }
  },
  "scheduler": {
    "presets": ["cargo"],
    "limits": { "process": 4 }
  },
  "toolRetry": { "maxAttempts": 2 },
  "compactionStrategy": "trim",
  "maxChildDepth": 1,
  "disable-default-providers": false
}`)
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := read(path)
	if err != nil {
		t.Fatalf("load example: %v", err)
	}
	if cfg.Provider != "echo" {
		t.Fatalf("provider = %q", cfg.Provider)
	}
	if cfg.Sandbox != "workspace-write" {
		t.Fatalf("sandbox = %q", cfg.Sandbox)
	}
	if len(cfg.Network.Allow) != 1 || cfg.Network.Allow[0] != "api.github.com" {
		t.Fatalf("network.allow = %#v", cfg.Network.Allow)
	}
	if cfg.DisableDefaultProviders {
		t.Fatal("disable-default-providers false should not enable bulk disable")
	}
}
