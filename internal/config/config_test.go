package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestDefaultModel(t *testing.T) {
	cases := map[string]string{
		"openai":    "gpt-5.5",
		"xai":       "grok-4.5",
		"anthropic": "claude-sonnet-5",
		"other":     "claude-sonnet-5",
	}
	for p, want := range cases {
		if got := DefaultModel(p); got != want {
			t.Errorf("DefaultModel(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestLoadMerge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{
		"provider": "openai",
		"model": "gpt-test",
		"theme": "nord",
		"systemPrompt": "global",
		"permissions": [{"permission":"bash","pattern":"*","action":"ask"}]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{
		"model": "project-model",
		"defaultAgent": "plan",
		"theme": "dracula",
		"permissions": [{"permission":"bash","pattern":"git *","action":"allow"}]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "openai" {
		t.Errorf("provider = %q", cfg.Provider)
	}
	if cfg.Model != "project-model" {
		t.Errorf("model = %q", cfg.Model)
	}
	if cfg.Theme != "dracula" {
		t.Errorf("theme = %q", cfg.Theme)
	}
	if cfg.SystemPrompt != "global" {
		t.Errorf("systemPrompt = %q", cfg.SystemPrompt)
	}
	if cfg.DefaultAgent != "plan" {
		t.Errorf("defaultAgent = %q", cfg.DefaultAgent)
	}
	if len(cfg.Permissions) != 2 {
		t.Fatalf("permissions = %#v", cfg.Permissions)
	}
	if cfg.Permissions[1].Action != permission.Allow {
		t.Errorf("second rule = %#v", cfg.Permissions[1])
	}
}

func TestLoadMissingIsOK(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "anthropic" {
		t.Errorf("default provider = %q", cfg.Provider)
	}
}

func TestLoadMalformed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("expected error")
	}
}

func TestSetGlobalDefaultsPreserves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := GlobalPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := Config{
		Provider:     "anthropic",
		Model:        "old",
		SystemPrompt: "keep me",
		Permissions:  permission.Ruleset{{Permission: "bash", Pattern: "*", Action: permission.Ask}},
	}
	raw, _ := json.MarshalIndent(initial, "", "  ")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetGlobalDefaults("openai", "new-model", "build", protocol.EffortHigh); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Provider != "openai" || got.Model != "new-model" || got.DefaultAgent != "build" {
		t.Errorf("got %#v", got)
	}
	if got.SystemPrompt != "keep me" || len(got.Permissions) != 1 {
		t.Errorf("did not preserve unrelated fields: %#v", got)
	}

	// empty fields leave existing values
	if err := SetGlobalDefaults("", "", "", ""); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	_ = json.Unmarshal(data, &got)
	if got.Provider != "openai" || got.Model != "new-model" {
		t.Errorf("empty update changed values: %#v", got)
	}
}

func TestSetGlobalDefaultsCorrupt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := GlobalPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`not-json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalDefaults("x", "y", "", ""); err == nil {
		t.Fatal("expected corrupt config error")
	}
}
