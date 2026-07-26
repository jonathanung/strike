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
		"permissions": [{"permission":"bash","pattern":"*","action":"ask"}],
		"hooks": [
			{"event":"pre_tool_use","command":"echo global"},
			{"event":"pre_tool_use","matcher":"*","action":"log"}
		]
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
		"permissions": [{"permission":"bash","pattern":"git *","action":"allow"}],
		"hooks": [
			{"event":"post_tool_use","command":"echo project","matcher":"bash"},
			{"event":"pre_tool_use","matcher":"write","action":"block","message":"no writes"}
		]
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
	if len(cfg.Hooks) != 4 {
		t.Fatalf("hooks = %#v", cfg.Hooks)
	}
	if cfg.Hooks[0].Command != "echo global" || cfg.Hooks[2].Matcher != "bash" {
		t.Errorf("hooks = %#v", cfg.Hooks)
	}
	shell := cfg.ShellHooks()
	if len(shell) != 2 || shell[0].Command != "echo global" {
		t.Fatalf("ShellHooks = %#v", shell)
	}
	rules := cfg.HookRules()
	if len(rules) != 2 || rules[1].Action != permission.HookActionBlock || rules[1].Message != "no writes" {
		t.Fatalf("HookRules = %#v", rules)
	}
}

func TestLoadSessionWorktreeMerge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{
		"session": {"worktree": "auto", "worktreeCleanup": "keep"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{
		"session": {"worktree": "always", "worktreeCleanup": "delete"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Session.Worktree != "always" {
		t.Errorf("worktree = %q", cfg.Session.Worktree)
	}
	if cfg.Session.WorktreeCleanup != "delete" {
		t.Errorf("cleanup = %q", cfg.Session.WorktreeCleanup)
	}
}

func TestLoadDropsInvalidHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	path := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{
		"hooks": [
			{"event":"pre_tool_use","matcher":"write","action":"block","message":"ok"},
			{"event":"nope","action":"log"},
			{"event":"post_tool_use","action":"block"},
			{"event":"pre_tool_use","command":"echo hi","action":"log"},
			{"event":"pre_tool_use"},
			{"event":"pre_tool_use","command":"echo shell"}
		]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Hooks) != 2 {
		t.Fatalf("hooks = %#v, want block rule + shell", cfg.Hooks)
	}
	if !cfg.Hooks[0].IsRule() || cfg.Hooks[0].Message != "ok" {
		t.Errorf("first = %#v", cfg.Hooks[0])
	}
	if !cfg.Hooks[1].IsShell() || cfg.Hooks[1].Command != "echo shell" {
		t.Errorf("second = %#v", cfg.Hooks[1])
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

func TestSetGlobalThemePersistsAndPreserves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := GlobalPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := Config{
		Provider:     "anthropic",
		Model:        "keep-model",
		SystemPrompt: "keep me",
		Theme:        "nord",
	}
	raw, _ := json.MarshalIndent(initial, "", "  ")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetGlobalTheme("  dracula  "); err != nil {
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
	if got.Theme != "dracula" {
		t.Errorf("theme = %q, want dracula", got.Theme)
	}
	if got.Provider != "anthropic" || got.Model != "keep-model" || got.SystemPrompt != "keep me" {
		t.Errorf("did not preserve unrelated fields: %#v", got)
	}
}

func TestSetGlobalThemeRejectsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, id := range []string{"", "   "} {
		if err := SetGlobalTheme(id); err == nil {
			t.Fatalf("SetGlobalTheme(%q) accepted empty id", id)
		}
	}
}

func TestSetGlobalThemeCorrupt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := GlobalPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`not-json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalTheme("dracula"); err == nil {
		t.Fatal("expected corrupt config error")
	}
}

func TestSetGlobalThemeCreatesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := SetGlobalTheme("tokyo-night"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Theme != "tokyo-night" {
		t.Errorf("theme = %q", got.Theme)
	}
}

func TestAppendProjectPermissionCreatesAndPreserves(t *testing.T) {
	work := t.TempDir()
	rule := permission.Rule{Permission: "bash", Pattern: "git *", Action: permission.Allow}
	if err := AppendProjectPermission(work, rule); err != nil {
		t.Fatal(err)
	}
	path := ProjectPath(work)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Permissions) != 1 || got.Permissions[0] != rule {
		t.Fatalf("permissions = %#v, want [%#v]", got.Permissions, rule)
	}

	// Preserve unrelated fields on second append.
	initial := Config{
		Provider:     "openai",
		Model:        "keep-me",
		SystemPrompt: "stay",
		Permissions:  got.Permissions,
	}
	raw, _ := json.MarshalIndent(initial, "", "  ")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	second := permission.Rule{Permission: "edit", Pattern: "*.go", Action: permission.Allow}
	if err := AppendProjectPermission(work, second); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Provider != "openai" || got.Model != "keep-me" || got.SystemPrompt != "stay" {
		t.Errorf("did not preserve unrelated fields: %#v", got)
	}
	if len(got.Permissions) != 2 || got.Permissions[1] != second {
		t.Fatalf("permissions after append = %#v", got.Permissions)
	}

	// Defaults for empty action/pattern.
	if err := AppendProjectPermission(work, permission.Rule{Permission: "write"}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	_ = json.Unmarshal(data, &got)
	last := got.Permissions[len(got.Permissions)-1]
	if last.Action != permission.Allow || last.Pattern != "*" {
		t.Errorf("defaults = %#v, want action=allow pattern=*", last)
	}
}

func TestAppendProjectPermissionRejects(t *testing.T) {
	if err := AppendProjectPermission("", permission.Rule{Permission: "bash"}); err == nil {
		t.Fatal("empty workDir: want error")
	}
	if err := AppendProjectPermission(t.TempDir(), permission.Rule{}); err == nil {
		t.Fatal("empty permission: want error")
	}

	work := t.TempDir()
	path := ProjectPath(work)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`not-json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AppendProjectPermission(work, permission.Rule{Permission: "bash"}); err == nil {
		t.Fatal("corrupt config: want error")
	}
}

func TestLoadCompactionStrategy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{
		"compactionStrategy": "trim",
		"compactionModel": "global-cheap"
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{
		"compactionStrategy": "summary",
		"compactionModel": " project-sum "
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CompactionStrategy != "summarize" {
		t.Fatalf("strategy = %q, want summarize", cfg.CompactionStrategy)
	}
	if cfg.CompactionModel != "project-sum" {
		t.Fatalf("model = %q", cfg.CompactionModel)
	}
	if got := NormalizeCompactionStrategy("nope"); got != "" {
		t.Fatalf("unknown normalize = %q", got)
	}
}
