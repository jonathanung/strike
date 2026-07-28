package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseProvidersFileDisableDefaultFlags(t *testing.T) {
	raw := []byte(`{
		"disable-default-providers": true,
		"disable-default-openai": false,
		"disable-default-anthropic": true,
		"acme": {
			"options": { "baseURL": "https://api.example.com/v1" },
			"api": "openai",
			"models": ["m1"]
		}
	}`)
	pf, err := ParseProvidersFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if pf.DisableDefaultAll == nil || !*pf.DisableDefaultAll {
		t.Fatalf("DisableDefaultAll = %v", pf.DisableDefaultAll)
	}
	if pf.DisableDefaultPer["openai"] != false {
		t.Fatalf("openai override = %v", pf.DisableDefaultPer["openai"])
	}
	if pf.DisableDefaultPer["anthropic"] != true {
		t.Fatalf("anthropic override = %v", pf.DisableDefaultPer["anthropic"])
	}
	if len(pf.Customs) != 1 || pf.Customs[0].Name != "acme" {
		t.Fatalf("customs = %+v", pf.Customs)
	}
}

func TestParseProvidersFileDisableDefaultBadBool(t *testing.T) {
	_, err := ParseProvidersFile([]byte(`{"disable-default-providers": "yes"}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestIsBuiltinProviderDisabledPrecedence(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		prov string
		want bool
	}{
		{"default off", Config{}, "anthropic", false},
		{"bulk on", Config{DisableDefaultProviders: true}, "anthropic", true},
		{"bulk on echo", Config{DisableDefaultProviders: true}, "echo", true},
		{"bulk on custom never", Config{DisableDefaultProviders: true}, "acme", false},
		{"per disable only", Config{DisableDefaultPer: map[string]bool{"xai": true}}, "xai", true},
		{"per disable other ok", Config{DisableDefaultPer: map[string]bool{"xai": true}}, "openai", false},
		{"override re-enable", Config{
			DisableDefaultProviders: true,
			DisableDefaultPer:       map[string]bool{"openai": false},
		}, "openai", false},
		{"override re-enable others still off", Config{
			DisableDefaultProviders: true,
			DisableDefaultPer:       map[string]bool{"openai": false},
		}, "anthropic", true},
		{"override force disable google", Config{
			DisableDefaultPer: map[string]bool{"google": true},
		}, "google", true},
		// Lookup name is canonicalized: gemini alias sees a google-keyed disable.
		{"gemini alias sees google disable", Config{
			DisableDefaultPer: map[string]bool{"google": true},
		}, "gemini", true},
		{"unknown not builtin", Config{DisableDefaultProviders: true}, "not-a-provider", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.IsBuiltinProviderDisabled(tc.prov); got != tc.want {
				t.Fatalf("IsBuiltinProviderDisabled(%q) = %v, want %v", tc.prov, got, tc.want)
			}
		})
	}
}

func TestLoadDisableDefaultMergePrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	// Global config: disable all.
	globalCfg := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(globalCfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalCfg, []byte(`{
		"disable-default-providers": true,
		"disable-default-openai": false
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Global providers.jsonc: re-disable openai, keep anthropic disabled via bulk.
	globalPF := filepath.Join(home, ".strike", "providers.jsonc")
	if err := os.WriteFile(globalPF, []byte(`{
		"disable-default-openai": true,
		"acme": {
			"options": { "baseURL": "https://acme.example/v1" },
			"api": "openai",
			"models": ["a"]
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Project providers.jsonc: re-enable openai; turn bulk off would be stronger
	// — here only override openai back to false.
	projectRoot := filepath.Join(work, ".strike")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "providers.jsonc"), []byte(`{
		"disable-default-openai": false
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DisableDefaultProviders {
		t.Fatal("expected bulk disable from global config")
	}
	if cfg.IsBuiltinProviderDisabled("anthropic") != true {
		t.Error("anthropic should stay disabled (bulk)")
	}
	if cfg.IsBuiltinProviderDisabled("openai") != false {
		t.Error("openai should be re-enabled by project override")
	}
	if cfg.IsBuiltinProviderDisabled("echo") != true {
		t.Error("echo should be disabled by bulk")
	}
	if _, ok := FindCustom(cfg.Providers, "acme"); !ok {
		t.Fatal("custom acme missing")
	}

	store := NewCustomStoreWithOverlays(cfg.Providers, cfg.ModelOverlays, cfg.EndpointOverlays, work)
	store.SetDisableDefault(cfg.DisableDefaultProviders, cfg.DisableDefaultPer)
	if store.IsBuiltinDisabled("openai") {
		t.Error("store: openai should be enabled")
	}
	if !store.IsBuiltinDisabled("xai") {
		t.Error("store: xai should be disabled")
	}
	if store.IsBuiltinDisabled("acme") {
		t.Error("store: custom never disabled")
	}
}

func TestLoadDisableDefaultGeminiAliasCanonicalizes(t *testing.T) {
	// disable-default-gemini in config/providers.jsonc is stored under google.
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".strike", "config"), []byte(`{
		"disable-default-gemini": true
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.DisableDefaultPer["gemini"]; ok {
		t.Fatalf("DisableDefaultPer kept gemini key: %+v", cfg.DisableDefaultPer)
	}
	if cfg.DisableDefaultPer["google"] != true {
		t.Fatalf("DisableDefaultPer[google] = %v, want true", cfg.DisableDefaultPer["google"])
	}
	if !cfg.IsBuiltinProviderDisabled("google") || !cfg.IsBuiltinProviderDisabled("gemini") {
		t.Fatal("google and gemini alias should both report disabled")
	}
	if cfg.IsBuiltinProviderDisabled("openai") {
		t.Fatal("openai should remain enabled")
	}
}

func TestLoadDisableDefaultFromConfigOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalCfg := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(globalCfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalCfg, []byte(`{
		"disable-default-anthropic": true
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DisableDefaultProviders {
		t.Fatal("bulk should be off")
	}
	if !cfg.IsBuiltinProviderDisabled("anthropic") {
		t.Fatal("anthropic should be disabled")
	}
	if cfg.IsBuiltinProviderDisabled("openai") {
		t.Fatal("openai should remain enabled")
	}
}

func TestLoadProjectClearsBulkDisable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".strike", "providers.jsonc"), []byte(`{
		"disable-default-providers": true
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".strike", "config"), []byte(`{
		"disable-default-providers": false
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DisableDefaultProviders {
		t.Fatal("project config should clear bulk disable")
	}
	if cfg.IsBuiltinProviderDisabled("openai") {
		t.Fatal("openai should be available after bulk false")
	}
}
