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
		"google":    "gemini-2.5-pro",
		"gemini":    "gemini-2.5-pro", // alias of google
		"other":     "claude-sonnet-5",
	}
	for p, want := range cases {
		if got := DefaultModel(p); got != want {
			t.Errorf("DefaultModel(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestCanonicalProviderID(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"  ":       "",
		"OpenAI":   "openai",
		" google ": "google",
		"GOOGLE":   "google",
		"gemini":   "google",
		"Gemini":   "google",
		" GEMINI ": "google",
		"echo":     "echo",
	}
	for in, want := range cases {
		if got := CanonicalProviderID(in); got != want {
			t.Errorf("CanonicalProviderID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGlobalRootResolvesSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".strike")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if got := GlobalRoot(); got != target {
		t.Errorf("GlobalRoot() = %q, want resolved target %q", got, target)
	}
	if got := GlobalPath(); got != filepath.Join(target, "config") {
		t.Errorf("GlobalPath() = %q, want path below resolved target", got)
	}
}

func TestGlobalRootMissingIsUnresolved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".strike")
	if got := GlobalRoot(); got != want {
		t.Errorf("GlobalRoot() = %q, want unresolved %q", got, want)
	}
}

func TestProjectRootResolvesSymlink(t *testing.T) {
	work := t.TempDir()
	target := filepath.Join(t.TempDir(), "project-state")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(work, ".strike")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := projectRoot(work); got != target {
		t.Errorf("projectRoot() = %q, want %q", got, target)
	}
	if got := ProjectPath(work); got != filepath.Join(target, "config") {
		t.Errorf("ProjectPath() = %q, want under target", got)
	}
}

func TestAgentDiscoveryRootsUseResolvedStrikeHomes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalTarget := filepath.Join(t.TempDir(), "global-state")
	if err := os.MkdirAll(filepath.Join(globalTarget, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(globalTarget, filepath.Join(home, ".strike")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	work := t.TempDir()
	projectTarget := filepath.Join(t.TempDir(), "project-state")
	if err := os.MkdirAll(filepath.Join(projectTarget, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(projectTarget, filepath.Join(work, ".strike")); err != nil {
		t.Fatal(err)
	}

	roots := agentDiscoveryRoots(work)
	if len(roots) < 2 {
		t.Fatalf("agentDiscoveryRoots len = %d, want at least global+project", len(roots))
	}
	if roots[0].dir != filepath.Join(globalTarget, "agents") {
		t.Errorf("global agents root = %q, want under resolved global", roots[0].dir)
	}
	var foundProject bool
	for _, r := range roots {
		if r.dir == filepath.Join(projectTarget, "agents") {
			foundProject = true
			break
		}
	}
	if !foundProject {
		t.Errorf("project agents root not found in %#v", roots)
	}
}

func TestLoadCanonicalizesProviderGeminiAlias(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"provider":"gemini","model":"gemini-2.5-flash"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "google" {
		t.Errorf("Provider = %q, want google (gemini alias)", cfg.Provider)
	}
	if cfg.Model != "gemini-2.5-flash" {
		t.Errorf("Model = %q, want gemini-2.5-flash (model id unchanged)", cfg.Model)
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

func TestLoadSurfacePresentationMerge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{
		"vimMode": "overlay",
		"nanoMode": "takeover",
		"mdReadMode": "embedded"
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{
		"mdReadMode": "modal",
		"nanoMode": "modal"
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VimMode != "overlay" {
		t.Errorf("vimMode = %q, want overlay from global", cfg.VimMode)
	}
	if cfg.NanoMode != "modal" {
		t.Errorf("nanoMode = %q, want modal from project", cfg.NanoMode)
	}
	if cfg.MdReadMode != "modal" {
		t.Errorf("mdReadMode = %q, want modal from project", cfg.MdReadMode)
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
	if cfg.PermissionAutoApproveSeconds != 0 {
		t.Errorf("auto-approve default = %d, want 0", cfg.PermissionAutoApproveSeconds)
	}
}

func TestClampPermissionAutoApproveSeconds(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, 0},
		{-3, 0},
		{1, 1},
		{15, 15},
		{60, 60},
		{61, 60},
		{999, 60},
	}
	for _, tt := range cases {
		if got := ClampPermissionAutoApproveSeconds(tt.in); got != tt.want {
			t.Errorf("Clamp(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestLoadPermissionAutoApprove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{
		"permissionAutoApproveSeconds": 99,
		"permissionAutoApproveExclude": [" Bash ", "bash", "", "write"]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{
		"permissionAutoApproveSeconds": 8
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PermissionAutoApproveSeconds != 8 {
		t.Errorf("seconds = %d, want 8 (project override, clamped)", cfg.PermissionAutoApproveSeconds)
	}
	if len(cfg.PermissionAutoApproveExclude) != 2 {
		t.Fatalf("exclude = %#v, want bash+write", cfg.PermissionAutoApproveExclude)
	}
	if cfg.PermissionAutoApproveExclude[0] != "bash" || cfg.PermissionAutoApproveExclude[1] != "write" {
		t.Errorf("exclude = %#v", cfg.PermissionAutoApproveExclude)
	}
	if !PermissionAutoApproveExcluded("BASH", cfg.PermissionAutoApproveExclude) {
		t.Error("BASH should be excluded")
	}
	if PermissionAutoApproveExcluded("edit", cfg.PermissionAutoApproveExclude) {
		t.Error("edit should not be excluded")
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

	if err := SetGlobalDefaults("openai", "new-model", "build", protocol.EffortHigh, ""); err != nil {
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
	if err := SetGlobalDefaults("", "", "", "", ""); err != nil {
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
	if err := SetGlobalDefaults("x", "y", "", "", ""); err == nil {
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

func TestSetGlobalDefaultsPersistsPermissionModeAndPreserves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := GlobalPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := Config{
		Provider:       "anthropic",
		Model:          "keep-model",
		SystemPrompt:   "keep me",
		PermissionMode: protocol.PermissionModeDefault,
	}
	raw, _ := json.MarshalIndent(initial, "", "  ")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetGlobalDefaults("", "", "", "", "yolo"); err != nil {
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
	if got.PermissionMode != protocol.PermissionModeYolo {
		t.Errorf("permissionMode = %q, want yolo", got.PermissionMode)
	}
	if got.Provider != "anthropic" || got.Model != "keep-model" || got.SystemPrompt != "keep me" {
		t.Errorf("did not preserve unrelated fields: %#v", got)
	}

	// Empty mode leaves the stored value alone.
	if err := SetGlobalDefaults("", "new-model", "", "", ""); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	_ = json.Unmarshal(data, &got)
	if got.PermissionMode != protocol.PermissionModeYolo {
		t.Errorf("empty mode cleared permissionMode: %q", got.PermissionMode)
	}
	if got.Model != "new-model" {
		t.Errorf("model = %q, want new-model", got.Model)
	}
}

func TestSetGlobalDefaultsRejectsUnknownPermissionMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SetGlobalDefaults("", "", "", "", "turbo"); err == nil {
		t.Fatal("SetGlobalDefaults accepted mode \"turbo\", want an error")
	}
}

func TestLoadPermissionMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{"permissionMode":"plan"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PermissionMode != protocol.PermissionModePlan {
		t.Errorf("permissionMode = %q, want plan", cfg.PermissionMode)
	}

	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{"permissionMode":"accept-edits"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PermissionMode != protocol.PermissionModeAcceptEdits {
		t.Errorf("project permissionMode = %q, want accept-edits", cfg.PermissionMode)
	}
}

func TestLoadRejectsUnknownPermissionMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{"permissionMode":"nope"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(work); err == nil {
		t.Fatal("Load accepted unknown permissionMode")
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

func TestSetGlobalPresentationPersistsAndPreserves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := GlobalPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := Config{
		Provider: "anthropic",
		Theme:    "nord",
		VimMode:  "pane",
	}
	raw, _ := json.MarshalIndent(initial, "", "  ")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetGlobalPresentation("modal", "takeover", "overlay"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.VimMode != "overlay" {
		t.Errorf("vimMode = %q, want overlay", got.VimMode)
	}
	if got.NanoMode != "takeover" {
		t.Errorf("nanoMode = %q, want takeover", got.NanoMode)
	}
	if got.MdReadMode != "modal" {
		t.Errorf("mdReadMode = %q, want modal", got.MdReadMode)
	}
	if got.Provider != "anthropic" || got.Theme != "nord" {
		t.Errorf("unrelated fields changed: %#v", got)
	}

	// Empty fields leave prior values.
	if err := SetGlobalPresentation("", "pane", ""); err != nil {
		t.Fatal(err)
	}
	got, err = ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.VimMode != "overlay" || got.NanoMode != "pane" || got.MdReadMode != "modal" {
		t.Errorf("partial update = vim=%q nano=%q md=%q", got.VimMode, got.NanoMode, got.MdReadMode)
	}
}

func TestSetGlobalPresentationRejectsUnknown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := SetGlobalPresentation("floating", "", ""); err == nil {
		t.Fatal("expected unknown vimMode error")
	}
	if err := SetGlobalPresentation("", "", "side"); err == nil {
		t.Fatal("expected unknown mdReadMode error")
	}
}

func TestReadGlobalDefaultsMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "" || got.Theme != "" {
		t.Errorf("missing config should be zero, got %#v", got)
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

func TestLoadCompactionThresholdKnobs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{
		"compactionThreshold": 0.80,
		"compactionBuffer": 2048,
		"keepUserTurns": 3
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{
		"compactionThreshold": 0.65,
		"compactionBuffer": 8192,
		"keepUserTurns": 1
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CompactionThreshold != 0.65 {
		t.Fatalf("threshold = %v, want 0.65", cfg.CompactionThreshold)
	}
	if cfg.CompactionBuffer != 8192 {
		t.Fatalf("buffer = %d, want 8192", cfg.CompactionBuffer)
	}
	if cfg.KeepUserTurns != 1 {
		t.Fatalf("keepUserTurns = %d, want 1", cfg.KeepUserTurns)
	}
}

func TestClampCompactionKnobs(t *testing.T) {
	if got := ClampCompactionThreshold(-0.5); got != 0 {
		t.Fatalf("neg threshold = %v", got)
	}
	if got := ClampCompactionThreshold(0.7); got != 0.7 {
		t.Fatalf("threshold = %v", got)
	}
	if got := ClampCompactionThreshold(1.5); got != 1.5 {
		t.Fatalf("disable threshold = %v", got)
	}
	if got := ClampCompactionBuffer(-10); got != 0 {
		t.Fatalf("neg buffer = %d", got)
	}
	if got := ClampCompactionBuffer(4096); got != 4096 {
		t.Fatalf("buffer = %d", got)
	}
	if got := ClampKeepUserTurns(-2); got != 0 {
		t.Fatalf("neg keep = %d", got)
	}
	if got := ClampKeepUserTurns(4); got != 4 {
		t.Fatalf("keep = %d", got)
	}
}

func TestLoadPruneKnobs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{
		"pruneProtectTokens": 50000,
		"pruneMinimumTokens": 25000,
		"pruneKeepUserTurns": 3,
		"pruneProtectTools": ["bash", " Bash ", "memory_read"]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{
		"pruneProtectTokens": 10000,
		"pruneMinimumTokens": 5000,
		"pruneKeepUserTurns": 1,
		"pruneProtectTools": ["webfetch"]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PruneProtectTokens != 10000 {
		t.Fatalf("protect = %d, want 10000", cfg.PruneProtectTokens)
	}
	if cfg.PruneMinimumTokens != 5000 {
		t.Fatalf("minimum = %d, want 5000", cfg.PruneMinimumTokens)
	}
	if cfg.PruneKeepUserTurns != 1 {
		t.Fatalf("keep = %d, want 1", cfg.PruneKeepUserTurns)
	}
	if len(cfg.PruneProtectTools) != 1 || cfg.PruneProtectTools[0] != "webfetch" {
		t.Fatalf("tools = %#v, want [webfetch]", cfg.PruneProtectTools)
	}
}

func TestLoadPruneKnobsDefaultsOmit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PruneProtectTokens != 0 || cfg.PruneMinimumTokens != 0 || cfg.PruneKeepUserTurns != 0 {
		t.Fatalf("omit want zeros, got protect=%d min=%d keep=%d",
			cfg.PruneProtectTokens, cfg.PruneMinimumTokens, cfg.PruneKeepUserTurns)
	}
	if cfg.PruneProtectTools != nil {
		t.Fatalf("tools = %#v, want nil", cfg.PruneProtectTools)
	}
}

func TestClampPruneKnobs(t *testing.T) {
	if got := ClampPruneProtectTokens(-1); got != 0 {
		t.Fatalf("neg protect = %d", got)
	}
	if got := ClampPruneProtectTokens(40000); got != 40000 {
		t.Fatalf("protect = %d", got)
	}
	if got := ClampPruneMinimumTokens(-10); got != 0 {
		t.Fatalf("neg minimum = %d", got)
	}
	if got := ClampPruneMinimumTokens(20000); got != 20000 {
		t.Fatalf("minimum = %d", got)
	}
	if got := ClampPruneKeepUserTurns(-2); got != 0 {
		t.Fatalf("neg keep = %d", got)
	}
	if got := ClampPruneKeepUserTurns(4); got != 4 {
		t.Fatalf("keep = %d", got)
	}
}

func TestNormalizePruneProtectTools(t *testing.T) {
	if got := NormalizePruneProtectTools(nil); got != nil {
		t.Fatalf("nil = %#v", got)
	}
	if got := NormalizePruneProtectTools([]string{"", "  "}); got != nil {
		t.Fatalf("blanks = %#v", got)
	}
	got := NormalizePruneProtectTools([]string{" Bash ", "bash", "memory_read", ""})
	if len(got) != 2 || got[0] != "bash" || got[1] != "memory_read" {
		t.Fatalf("got %#v", got)
	}
}

func TestNormalizeLeanCode(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"off":     LeanCodeOff,
		"none":    LeanCodeOff,
		"lite":    LeanCodeLite,
		"light":   LeanCodeLite,
		"full":    LeanCodeFull,
		"on":      LeanCodeFull,
		"FULL":    LeanCodeFull,
		"nope":    "",
		"  lite ": LeanCodeLite,
	}
	for in, want := range cases {
		if got := NormalizeLeanCode(in); got != want {
			t.Errorf("NormalizeLeanCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeNotify(t *testing.T) {
	cases := map[string]string{
		"":               "",
		"on":             NotifyOn,
		"ALWAYS":         NotifyOn,
		"off":            NotifyOff,
		"never":          NotifyOff,
		"unfocused-only": NotifyUnfocusedOnly,
		"unfocused":      NotifyUnfocusedOnly,
		"nope":           "",
	}
	for in, want := range cases {
		if got := NormalizeNotify(in); got != want {
			t.Errorf("NormalizeNotify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoadLeanCodeMerge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{"leanCode": "full"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LeanCode != LeanCodeFull {
		t.Fatalf("LeanCode = %q, want full", cfg.LeanCode)
	}
	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{"leanCode": "off"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LeanCode != LeanCodeOff {
		t.Fatalf("project LeanCode = %q, want off", cfg.LeanCode)
	}
}

func TestNormalizeDeferTools(t *testing.T) {
	cases := map[string]string{
		"on":       DeferToolsOn,
		"true":     DeferToolsOn,
		"enabled":  DeferToolsOn,
		"off":      DeferToolsOff,
		"false":    DeferToolsOff,
		"disabled": DeferToolsOff,
		"":         "",
		"maybe":    "",
		"  ON  ":   DeferToolsOn,
	}
	for in, want := range cases {
		if got := NormalizeDeferTools(in); got != want {
			t.Errorf("NormalizeDeferTools(%q) = %q, want %q", in, got, want)
		}
	}
	if !DeferToolsEnabled("on") || DeferToolsEnabled("off") || DeferToolsEnabled("") {
		t.Fatal("DeferToolsEnabled mismatch")
	}
}

func TestLoadDeferToolsMerge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{"deferTools": "on"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeferTools != DeferToolsOn {
		t.Fatalf("DeferTools = %q, want on", cfg.DeferTools)
	}
	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{"deferTools": "off"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeferTools != DeferToolsOff {
		t.Fatalf("project DeferTools = %q, want off", cfg.DeferTools)
	}
}

func TestLoadNotifyMerge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{"notify": "on"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{"notify": "OFF"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Notify != NotifyOff {
		t.Fatalf("notify = %q, want off", cfg.Notify)
	}

	// Unknown values dropped (empty = TUI default unfocused-only).
	if err := os.WriteFile(project, []byte(`{"notify": "maybe"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(work)
	if err != nil {
		t.Fatal(err)
	}
	// Global "on" remains because project unknown normalizes to empty and
	// merge only applies non-empty layer values — but read() normalizes
	// before merge, so empty layer field does not clear base.
	// Wait: merge is layer-by-layer. project layer after normalize has
	// Notify="". merge skips empty. So global "on" stays.
	if cfg.Notify != NotifyOn {
		t.Fatalf("notify after unknown project = %q, want on (global)", cfg.Notify)
	}
}

func TestLoadMCPMergeReplace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{
		"mcp": {"servers": {
			"global_only": {"command": "echo", "args": ["g"]},
			"shared": {"command": "npx", "args": ["-y", "old"]}
		}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{
		"mcp": {"servers": {
			"shared": {"command": "npx", "args": ["-y", "new"], "env": {"TOKEN": "x"}},
			"project": {"command": "uvx", "args": ["mcp-server"]}
		}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.MCP.Servers["global_only"]; ok {
		t.Fatal("project mcp.servers should replace global map entirely")
	}
	if cfg.MCP.Servers["shared"].Args[1] != "new" {
		t.Fatalf("shared = %#v", cfg.MCP.Servers["shared"])
	}
	if cfg.MCP.Servers["shared"].Env["TOKEN"] != "x" {
		t.Fatalf("env = %#v", cfg.MCP.Servers["shared"].Env)
	}
	if cfg.MCP.Servers["project"].Command != "uvx" {
		t.Fatalf("project server = %#v", cfg.MCP.Servers["project"])
	}
}

func TestLoadMCPHTTPFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{
		"mcp": {"servers": {
			"remote": {
				"type": "http",
				"url": "https://mcp.example.com/mcp",
				"headers": {"Authorization": "Bearer secret"}
			}
		}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.MCP.Servers["remote"]
	if s.Type != "http" || s.URL != "https://mcp.example.com/mcp" {
		t.Fatalf("remote = %#v", s)
	}
	if s.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("headers = %#v", s.Headers)
	}
}
