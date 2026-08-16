package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMCPFileWrappedServers(t *testing.T) {
	raw := []byte(`
// global mcps
{
  "servers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": { "TOKEN": "x" }
    }
  }
}
`)
	mc, err := ParseMCPFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := mc.Servers["github"]
	if !ok || s.Command != "npx" || len(s.Args) != 2 || s.Env["TOKEN"] != "x" {
		t.Fatalf("github = %#v ok=%v", s, ok)
	}
}

func TestParseMCPFileBareMap(t *testing.T) {
	raw := []byte(`{
  "remote": {
    "type": "http",
    "url": "https://mcp.example.com/mcp",
    "headers": {"Authorization": "Bearer secret"}
  },
  "local": { "command": "uvx", "args": ["mcp-server"] }
}`)
	mc, err := ParseMCPFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(mc.Servers) != 2 {
		t.Fatalf("servers = %#v", mc.Servers)
	}
	r := mc.Servers["remote"]
	if r.Type != "http" || r.URL != "https://mcp.example.com/mcp" {
		t.Fatalf("remote = %#v", r)
	}
	if r.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("headers = %#v", r.Headers)
	}
	if mc.Servers["local"].Command != "uvx" {
		t.Fatalf("local = %#v", mc.Servers["local"])
	}
}

func TestParseMCPFileEmptyObjectClears(t *testing.T) {
	mc, err := ParseMCPFile([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if mc.Servers == nil {
		t.Fatal("empty {} should set empty servers map, not nil")
	}
	if len(mc.Servers) != 0 {
		t.Fatalf("servers = %#v", mc.Servers)
	}
}

func TestParseMCPFileEmptyInputNoop(t *testing.T) {
	mc, err := ParseMCPFile([]byte(`  // only comments
`))
	if err != nil {
		t.Fatal(err)
	}
	if mc.Servers != nil {
		t.Fatalf("comment-only should be nil Servers, got %#v", mc.Servers)
	}
}

func TestParseMCPFileRejectsArray(t *testing.T) {
	if _, err := ParseMCPFile([]byte(`[]`)); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadMergesMCPJSONC(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	globalCfg := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(globalCfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalCfg, []byte(`{
		"mcp": {"servers": {
			"from-config": {"command": "echo", "args": ["cfg"]},
			"shared": {"command": "npx", "args": ["-y", "old"]}
		}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	globalFile := filepath.Join(home, ".strike", "mcp.jsonc")
	if err := os.WriteFile(globalFile, []byte(`{
		// overrides config in same root
		"shared": {"command": "npx", "args": ["-y", "new"], "env": {"TOKEN": "x"}},
		"from-jsonc": {"command": "uvx", "args": ["a"]}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	projectFile := filepath.Join(work, ".strike", "mcp.jsonc")
	if err := os.MkdirAll(filepath.Dir(projectFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectFile, []byte(`{
		"servers": {
			"project-only": {"command": "true"}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.MCP.Servers["from-config"]; ok {
		t.Fatal("project mcp.jsonc should replace global map entirely")
	}
	if _, ok := cfg.MCP.Servers["from-jsonc"]; ok {
		t.Fatal("project mcp.jsonc should replace global map entirely")
	}
	if _, ok := cfg.MCP.Servers["shared"]; ok {
		t.Fatal("project mcp.jsonc should replace global map entirely")
	}
	if cfg.MCP.Servers["project-only"].Command != "true" {
		t.Fatalf("project-only = %#v", cfg.MCP.Servers["project-only"])
	}
}

func TestLoadMCPJSONCOverridesConfigSameRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	if err := os.MkdirAll(filepath.Join(home, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".strike", "config"), []byte(`{
		"mcp": {"servers": {"a": {"command": "from-config"}}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".strike", "mcp.jsonc"), []byte(`{
		"a": {"command": "from-jsonc"},
		"b": {"command": "also-jsonc"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCP.Servers["a"].Command != "from-jsonc" {
		t.Fatalf("a = %#v", cfg.MCP.Servers["a"])
	}
	if cfg.MCP.Servers["b"].Command != "also-jsonc" {
		t.Fatalf("b = %#v", cfg.MCP.Servers["b"])
	}
}

func TestLoadMCPJSONClearsWithEmptyServers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	if err := os.MkdirAll(filepath.Join(home, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".strike", "mcp.jsonc"), []byte(`{
		"keep": {"command": "echo"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".strike", "mcp.json"), []byte(`{"servers": {}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCP.Servers) != 0 {
		t.Fatalf("project empty servers should clear: %#v", cfg.MCP.Servers)
	}
}

func TestGlobalMCPFilePathPrefersJSONC(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".strike")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonc := filepath.Join(root, "mcp.jsonc")
	json := filepath.Join(root, "mcp.json")
	if err := os.WriteFile(json, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := GlobalMCPFilePath(); got != json {
		t.Fatalf("only json present: got %q want %q", got, json)
	}
	if err := os.WriteFile(jsonc, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := GlobalMCPFilePath(); got != jsonc {
		t.Fatalf("jsonc preferred: got %q want %q", got, jsonc)
	}
}
