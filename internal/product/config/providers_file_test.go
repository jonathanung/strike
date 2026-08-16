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
  "my-kimi": {
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
	kimi, ok := FindCustom(items, "my-kimi")
	if !ok || kimi.API != WireOpenAI || kimi.APIKeyEnv != "KIMI_API_KEY" {
		t.Fatalf("my-kimi = %+v", kimi)
	}
	if kimi.BaseURL != "https://api.moonshot.cn/v1" || len(kimi.Models) != 1 {
		t.Fatalf("my-kimi fields = %+v", kimi)
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
		"my-kimi": { "options": { "baseURL": "https://k.example/v1" }, "api": "openai" },
		"keep": { "options": { "baseURL": "https://keep.example/v1" }, "api": "openai" }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	store := NewCustomStore(cfg.Providers, work)
	if err := store.Remove("my-kimi"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("my-kimi"); ok {
		t.Fatal("still in memory")
	}
	// Reload from disk — my-kimi must be gone from jsonc.
	cfg2, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := FindCustom(cfg2.Providers, "my-kimi"); ok {
		t.Fatal("my-kimi still on disk after Remove")
	}
	if _, ok := FindCustom(cfg2.Providers, "keep"); !ok {
		t.Fatal("keep was removed")
	}
}

func TestParseProvidersFileGoogleAndGeminiAliasKeys(t *testing.T) {
	// Overlay/endpoint keys under gemini land on canonical google.
	rawGemini := []byte(`{
		"gemini": {
			"options": { "baseURL": "https://gemini-proxy.example/v1beta", "apiKey": "{env:GEMINI_API_KEY}" },
			"models": {
				"gemini-2.5-flash": { "name": "Flash via alias" }
			}
		},
		"disable-default-gemini": true
	}`)
	pf, err := ParseProvidersFile(rawGemini)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Customs) != 0 {
		t.Fatalf("gemini must not become a custom: %+v", pf.Customs)
	}
	ep, ok := pf.Endpoints["google"]
	if !ok {
		t.Fatalf("Endpoints missing google: %+v", pf.Endpoints)
	}
	if _, hasGemini := pf.Endpoints["gemini"]; hasGemini {
		t.Fatalf("Endpoints must not keep gemini key: %+v", pf.Endpoints)
	}
	if ep.BaseURL != "https://gemini-proxy.example/v1beta" {
		t.Errorf("BaseURL = %q", ep.BaseURL)
	}
	if ep.APIKeyEnv != "GEMINI_API_KEY" {
		t.Errorf("APIKeyEnv = %q", ep.APIKeyEnv)
	}
	overlay, ok := pf.Overlays["google"]
	if !ok || len(overlay) != 1 || overlay[0].ID != "gemini-2.5-flash" {
		t.Fatalf("Overlays[google] = %+v", pf.Overlays)
	}
	if _, hasGemini := pf.Overlays["gemini"]; hasGemini {
		t.Fatalf("Overlays must not keep gemini key: %+v", pf.Overlays)
	}
	// disable-default-gemini canonicalizes onto google.
	if pf.DisableDefaultPer["google"] != true {
		t.Errorf("DisableDefaultPer = %+v, want google:true", pf.DisableDefaultPer)
	}
	if _, ok := pf.DisableDefaultPer["gemini"]; ok {
		t.Errorf("DisableDefaultPer must not keep gemini key: %+v", pf.DisableDefaultPer)
	}

	// Canonical google key also lands under google (not a custom).
	rawGoogle := []byte(`{
		"google": {
			"options": { "baseURL": "https://google-proxy.example/v1beta" },
			"models": { "gemini-2.5-pro": { "name": "Pro" } }
		}
	}`)
	pf2, err := ParseProvidersFile(rawGoogle)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf2.Customs) != 0 {
		t.Fatalf("google must not become a custom: %+v", pf2.Customs)
	}
	if ep, ok := pf2.Endpoints["google"]; !ok || ep.BaseURL != "https://google-proxy.example/v1beta" {
		t.Fatalf("Endpoints[google] = %+v ok=%v", ep, ok)
	}
}

func TestLoadProvidersJSONCGeminiEndpointCanonicalizes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".strike", "providers.jsonc"), []byte(`{
		"gemini": {
			"options": { "baseURL": "https://alias-endpoint.example/v1beta" },
			"models": { "gemini-2.5-flash": {} }
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	ep, ok := cfg.EndpointOverlays["google"]
	if !ok || ep.BaseURL != "https://alias-endpoint.example/v1beta" {
		t.Fatalf("EndpointOverlays[google] = %+v ok=%v", ep, ok)
	}
	if _, ok := cfg.EndpointOverlays["gemini"]; ok {
		t.Fatal("EndpointOverlays must not keep gemini key")
	}
	if defs := cfg.ModelOverlays["google"]; len(defs) == 0 {
		t.Fatalf("ModelOverlays[google] empty: %+v", cfg.ModelOverlays)
	}
}

func TestWireFromNPM(t *testing.T) {
	if got := wireFromNPM("@ai-sdk/anthropic"); got != WireAnthropic {
		t.Errorf("anthropic npm = %q", got)
	}
	if got := wireFromNPM("@ai-sdk/openai-compatible"); got != WireOpenAI {
		t.Errorf("openai-compatible npm = %q", got)
	}
	if got := wireFromNPM("@ai-sdk/openai"); got != WireResponses {
		t.Errorf("@ai-sdk/openai npm = %q, want responses", got)
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
