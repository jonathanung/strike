package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseProvidersFileOpenCodeMap(t *testing.T) {
	raw := []byte(`
// custom gateways
{
  "kimi": {
    "npm": "@ai-sdk/openai-compatible", // optional
    "name": "Kimi",
    "options": {
      "baseURL": "https://api.moonshot.cn/v1",
      "apiKey": "{env:KIMI_API_KEY}"
    },
    "models": ["moonshot-v1"]
  },
  "claude-proxy": {
    "npm": "@ai-sdk/anthropic",
    "options": {
      "baseURL": "$CLAUDE_BASE",
      "apiKey": "${ANTHROPIC_AUTH_TOKEN}"
    }
  }
}
`)
	pf, err := ParseProvidersFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	items := pf.Customs
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	kimi, ok := FindCustom(items, "kimi")
	if !ok || kimi.API != WireOpenAI || kimi.APIKeyEnv != "KIMI_API_KEY" {
		t.Fatalf("kimi = %+v", kimi)
	}
	if kimi.BaseURL != "https://api.moonshot.cn/v1" || len(kimi.Models) != 1 {
		t.Fatalf("kimi fields = %+v", kimi)
	}
	proxy, ok := FindCustom(items, "claude-proxy")
	if !ok || proxy.API != WireAnthropic || proxy.APIKeyEnv != "ANTHROPIC_AUTH_TOKEN" {
		t.Fatalf("claude-proxy = %+v", proxy)
	}
	if proxy.BaseURL != "$CLAUDE_BASE" {
		t.Errorf("proxy base kept as template: %q", proxy.BaseURL)
	}
}

func TestParseProvidersFileArray(t *testing.T) {
	raw := []byte(`[
	  {"name":"ollama","baseURL":"http://localhost:11434/v1","api":"openai"}
	]`)
	pf, err := ParseProvidersFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	items := pf.Customs
	if len(items) != 1 || items[0].Name != "ollama" {
		t.Fatalf("items = %+v", items)
	}
}

func TestLoadMergesProvidersJSONC(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	globalCfg := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(globalCfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalCfg, []byte(`{
		"providers": [
			{"name":"from-config","baseURL":"https://cfg.example/v1","api":"openai"}
		]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	globalFile := filepath.Join(home, ".strike", "providers.jsonc")
	if err := os.WriteFile(globalFile, []byte(`{
		"from-jsonc": {
			"options": {
				"baseURL": "https://jsonc.example/v1",
				"apiKey": "{env:JSONC_KEY}"
			}
		},
		"from-config": {
			"options": { "baseURL": "https://jsonc-override.example/v1" },
			"api": "openai"
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	projectFile := filepath.Join(work, ".strike", "providers.jsonc")
	if err := os.MkdirAll(filepath.Dir(projectFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectFile, []byte(`{
		"project-only": {
			"npm": "@ai-sdk/openai-compatible",
			"options": { "baseURL": "http://127.0.0.1:8080/v1" }
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 3 {
		t.Fatalf("providers = %+v", cfg.Providers)
	}
	fc, ok := FindCustom(cfg.Providers, "from-config")
	if !ok || fc.BaseURL != "https://jsonc-override.example/v1" {
		t.Errorf("from-config override = %+v", fc)
	}
	if _, ok := FindCustom(cfg.Providers, "from-jsonc"); !ok {
		t.Error("from-jsonc missing")
	}
	if _, ok := FindCustom(cfg.Providers, "project-only"); !ok {
		t.Error("project-only missing")
	}
	fj, _ := FindCustom(cfg.Providers, "from-jsonc")
	if fj.APIKeyEnv != "JSONC_KEY" {
		t.Errorf("apiKeyEnv = %q", fj.APIKeyEnv)
	}
}

func TestCustomStoreRemoveFromProvidersJSONC(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	path := filepath.Join(home, ".strike", "providers.jsonc")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{
		"kimi": { "options": { "baseURL": "https://k.example/v1" }, "api": "openai" },
		"keep": { "options": { "baseURL": "https://keep.example/v1" }, "api": "openai" }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	store := NewCustomStore(cfg.Providers, work)
	if err := store.Remove("kimi"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("kimi"); ok {
		t.Fatal("still in memory")
	}
	// Reload from disk — kimi must be gone from jsonc.
	cfg2, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := FindCustom(cfg2.Providers, "kimi"); ok {
		t.Fatal("kimi still on disk after Remove")
	}
	if _, ok := FindCustom(cfg2.Providers, "keep"); !ok {
		t.Fatal("keep was removed")
	}
}

func TestWireFromNPM(t *testing.T) {
	if got := wireFromNPM("@ai-sdk/anthropic"); got != WireAnthropic {
		t.Errorf("anthropic npm = %q", got)
	}
	if got := wireFromNPM("@ai-sdk/openai-compatible"); got != WireOpenAI {
		t.Errorf("openai npm = %q", got)
	}
	if got := wireFromNPM(""); got != WireOpenAI {
		t.Errorf("empty npm = %q", got)
	}
}

func TestStripJSONC(t *testing.T) {
	in := []byte(`{"a":1, // line-gone
	"b": "keep /* star */ here", /* block-gone */ "c":2}`)
	out, err := stripJSONC(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `"a":1`) || !strings.Contains(s, `"c":2`) {
		t.Fatalf("stripped = %s", out)
	}
	if strings.Contains(s, "line-gone") || strings.Contains(s, "block-gone") {
		t.Fatalf("comment leaked: %s", out)
	}
	if !strings.Contains(s, "keep /* star */ here") {
		t.Fatalf("string body altered: %s", out)
	}
}
