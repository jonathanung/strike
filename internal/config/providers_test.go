package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCustomProviderValidate(t *testing.T) {
	valid := CustomProvider{
		Name:    "acme",
		BaseURL: "https://api.acme.example/v1",
		API:     WireOpenAI,
		Models:  []string{"acme-v1"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}

	cases := []struct {
		name string
		p    CustomProvider
	}{
		{"empty name", CustomProvider{BaseURL: "https://x.com", API: WireOpenAI}},
		{"builtin name", CustomProvider{Name: "openai", BaseURL: "https://x.com", API: WireOpenAI}},
		{"bad api", CustomProvider{Name: "acme", BaseURL: "https://x.com", API: "gemini"}},
		{"bad url", CustomProvider{Name: "acme", BaseURL: "not-a-url", API: WireOpenAI}},
		{"ftp url", CustomProvider{Name: "acme", BaseURL: "ftp://x.com", API: WireOpenAI}},
		{"uppercase name", CustomProvider{Name: "Acme", BaseURL: "https://x.com", API: WireOpenAI}},
	}
	for _, tc := range cases {
		p := NormalizeCustomProvider(tc.p)
		// uppercase becomes lowercase via normalize — re-test raw where needed
		if tc.name == "uppercase name" {
			if err := tc.p.Validate(); err == nil {
				t.Errorf("%s: expected error before normalize", tc.name)
			}
			continue
		}
		if err := p.Validate(); err == nil {
			t.Errorf("%s: expected error", tc.name)
		}
	}
}

func TestCustomStoreRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store := NewCustomStore(nil)
	p := CustomProvider{
		Name:    "acme",
		BaseURL: "https://api.acme.example/v1",
		API:     WireOpenAI,
		Models:  []string{"acme-v1-8k", "acme-v1-32k"},
	}
	if err := store.Upsert(p); err != nil {
		t.Fatal(err)
	}
	got, ok := store.Get("acme")
	if !ok || got.BaseURL != p.BaseURL || got.API != WireOpenAI || len(got.Models) != 2 {
		t.Fatalf("Get = %+v ok=%v", got, ok)
	}

	data, err := os.ReadFile(GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "acme" {
		t.Fatalf("persisted = %+v", cfg.Providers)
	}
	// Secrets must not appear in config.
	if raw := string(data); containsAny(raw, "sk-", "apiKey", "secret") {
		t.Errorf("config must not hold secrets: %s", raw)
	}

	if err := store.Remove("acme"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("acme"); ok {
		t.Fatal("expected removed")
	}
}

func TestLoadMergesProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{
		"providers": [
			{"name":"acme","baseURL":"https://global.example/v1","api":"openai"},
			{"name":"ollama","baseURL":"http://localhost:11434/v1","api":"openai"}
		]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{
		"providers": [
			{"name":"acme","baseURL":"https://project.example/v1","api":"openai","models":["acme-v2"]}
		]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("providers = %+v", cfg.Providers)
	}
	acme, ok := FindCustom(cfg.Providers, "acme")
	if !ok || acme.BaseURL != "https://project.example/v1" || len(acme.Models) != 1 {
		t.Errorf("acme project override = %+v", acme)
	}
	if _, ok := FindCustom(cfg.Providers, "ollama"); !ok {
		t.Error("ollama missing after merge")
	}
}

func TestCustomStoreList(t *testing.T) {
	items := []CustomProvider{
		{Name: "acme", BaseURL: "https://a.example/v1", API: WireOpenAI},
		{Name: "ollama", BaseURL: "http://localhost:11434/v1", API: WireOpenAI},
	}
	store := NewCustomStore(items)
	got := store.List()
	if len(got) != 2 || got[0].Name != "acme" || got[1].Name != "ollama" {
		t.Fatalf("List = %+v", got)
	}
	got[0].Name = "mutated"
	again := store.List()
	if again[0].Name != "acme" {
		t.Errorf("List snapshot mutated store: %+v", again)
	}
	empty := NewCustomStore(nil).List()
	if empty == nil {
		t.Fatal("List on empty store returned nil slice")
	}
	if len(empty) != 0 {
		t.Fatalf("empty List = %+v", empty)
	}
}

func TestDefaultModelCustom(t *testing.T) {
	if got := DefaultModelCustom(CustomProvider{Models: []string{"a", "b"}}); got != "a" {
		t.Errorf("first model = %q, want a", got)
	}
	if got := DefaultModelCustom(CustomProvider{}); got != "" {
		t.Errorf("empty models = %q, want empty", got)
	}
	if got := DefaultModelCustom(CustomProvider{Models: nil}); got != "" {
		t.Errorf("nil models = %q, want empty", got)
	}
}

func TestLoadDropsInvalidProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{
		"providers": [
			{"name":"openai","baseURL":"https://x.com","api":"openai"},
			{"name":"good","baseURL":"https://x.com/v1","api":"openai"}
		]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "good" {
		t.Fatalf("got %+v", cfg.Providers)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && containsFold(s, sub) {
			// apiKey as JSON key would be a problem; "api" alone is fine
			if sub == "apiKey" && containsFold(s, `"apiKey"`) {
				return true
			}
			if sub != "apiKey" && containsFold(s, sub) {
				return true
			}
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && (indexString(s, sub) >= 0)))
}

func indexString(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
