package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/scheduler"
)

func TestConcurrentSetGlobalDefaults(t *testing.T) {
	// Multiple goroutines each set different provider/model/agent values.
	// The final config must reflect the last write for each field and must
	// never be corrupt (invalid JSON or missing fields written by others).
	home := t.TempDir()
	t.Setenv("HOME", home)

	var wg sync.WaitGroup
	n := 20
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			provider := ""
			model := ""
			agent := ""
			switch i % 5 {
			case 0:
				provider = "openai"
			case 1:
				model = "gpt-5"
			case 2:
				agent = "build"
			case 3:
				provider = "anthropic"
				model = "claude-sonnet-5"
			case 4:
				agent = "orchestrator"
			}
			// Errors are expected when the file is briefly empty (lock created
			// it); we ignore them because what matters is the final state.
			_ = SetGlobalDefaults(provider, model, agent, "", "")
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(GlobalPath())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("final config is not valid JSON: %v\n%s", err, string(data))
	}
	// Every field that was set by at least one goroutine must have been
	// persisted (no lost writes from the set that ran last for that field).
	if got.Provider == "" && got.Model == "" && got.DefaultAgent == "" {
		t.Errorf("expected at least one field to be set, got all empty: %#v", got)
	}
}

func TestConcurrentSetGlobalDefaultsAndTheme(t *testing.T) {
	// Interleaved SetGlobalDefaults and SetGlobalTheme calls must preserve
	// the theme and the model across competing writes.
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed the config with known state so we can verify preservation.
	if err := SetGlobalDefaults("openai", "gpt-4", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalTheme("nord"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				_ = SetGlobalDefaults("", "claude-opus-5", "build", "", "")
			} else {
				_ = SetGlobalTheme("dracula")
			}
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(GlobalPath())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("final config is not valid JSON: %v\n%s", err, string(data))
	}
	// The provider set in the seed must survive.
	if got.Provider != "openai" {
		t.Errorf("provider = %q, want openai (seed value preserved)", got.Provider)
	}
	// The model should be claude-opus-5 (written by even goroutines).
	if got.Model != "claude-opus-5" {
		t.Errorf("model = %q, want claude-opus-5", got.Model)
	}
	// The theme should be dracula (written by odd goroutines).
	if got.Theme != "dracula" {
		t.Errorf("theme = %q, want dracula", got.Theme)
	}
	// The agent should be build (written by even goroutines).
	if got.DefaultAgent != "build" {
		t.Errorf("defaultAgent = %q, want build", got.DefaultAgent)
	}
}

func TestConcurrentUnrelatedFieldsSurvive(t *testing.T) {
	// Prove that systemPrompt and permissions survive concurrent writes
	// to provider/model. Each goroutine writes only provider/model; the
	// unrelated fields must be intact in the final config.
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed with unrelated fields.
	seed := Config{
		Provider:     "anthropic",
		Model:        "claude-sonnet-5",
		SystemPrompt: "you are a helpful assistant",
		Permissions:  []permission.Rule{{Permission: "bash", Pattern: "*", Action: permission.Ask}},
	}
	writeJSON(t, GlobalPath(), seed)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = SetGlobalDefaults("openai", "gpt-5", "", "", "")
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(GlobalPath())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("final config is not valid JSON: %v\n%s", err, string(data))
	}
	if got.Provider != "openai" {
		t.Errorf("provider = %q, want openai", got.Provider)
	}
	if got.Model != "gpt-5" {
		t.Errorf("model = %q, want gpt-5", got.Model)
	}
	if got.SystemPrompt != seed.SystemPrompt {
		t.Errorf("systemPrompt = %q, want %q (unrelated field preserved)", got.SystemPrompt, seed.SystemPrompt)
	}
	if len(got.Permissions) != 1 {
		t.Errorf("permissions = %v, want 1 rule (unrelated field preserved)", got.Permissions)
	}
}

func TestConcurrentEffortSurvivesModelUpdate(t *testing.T) {
	// Parallel goroutines: half set effort, half set model. The final
	// config must have the effort and model both set (no lost writes).
	home := t.TempDir()
	t.Setenv("HOME", home)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				_ = SetGlobalDefaults("", "claude-sonnet-5", "", protocol.EffortHigh, "")
			} else {
				_ = SetGlobalDefaults("", "gpt-5", "", "", "")
			}
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(GlobalPath())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("final config is not valid JSON: %v\n%s", err, string(data))
	}
	// Both fields must be set — the model from odd goroutines, effort from even.
	if got.Model == "" {
		t.Errorf("model is empty, expected a value from concurrent writes")
	}
	if got.Effort == "" {
		t.Errorf("effort is empty, expected effort high from even goroutines")
	}
}

func TestTempFileNoPartialWrite(t *testing.T) {
	// Verify that the config file is written atomically: there must be
	// no temp file left behind, and the config must be valid JSON.
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := SetGlobalDefaults("anthropic", "claude-sonnet-5", "build", "", "yolo"); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Dir(GlobalPath())
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}

	data, err := os.ReadFile(GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("config is not valid JSON: %s", string(data))
	}
}

func TestSetGlobalDefaultsWithLockContention(t *testing.T) {
	// Stress test: many goroutines competing to write, each setting
	// a full set of fields. The final config must be consistent.
	home := t.TempDir()
	t.Setenv("HOME", home)

	var wg sync.WaitGroup
	n := 50
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = SetGlobalDefaults(
				"openai",
				"gpt-5",
				"build",
				protocol.EffortHigh,
				"yolo",
			)
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(GlobalPath())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("final config is not valid JSON after %d concurrent writes: %v\n%s", n, err, string(data))
	}
	if got.Provider != "openai" {
		t.Errorf("provider = %q, want openai", got.Provider)
	}
	if got.Model != "gpt-5" {
		t.Errorf("model = %q, want gpt-5", got.Model)
	}
	if got.DefaultAgent != "build" {
		t.Errorf("defaultAgent = %q, want build", got.DefaultAgent)
	}
	if got.Effort != protocol.EffortHigh {
		t.Errorf("effort = %q, want high", got.Effort)
	}
	if got.PermissionMode != protocol.PermissionModeYolo {
		t.Errorf("permissionMode = %q, want yolo", got.PermissionMode)
	}
}

func TestSetGlobalDefaultsPreservesConfigFileSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	// Real config lives outside ~/.strike; ~/.strike/config is a file symlink.
	strikeDir := filepath.Join(home, ".strike")
	if err := os.Mkdir(strikeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	referent := filepath.Join(state, "config.json")
	if err := os.WriteFile(referent, []byte(`{"provider":"anthropic","model":"keep-me"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(strikeDir, "config")
	if err := os.Symlink(referent, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := SetGlobalDefaults("openai", "gpt-test", "", "", ""); err != nil {
		t.Fatalf("SetGlobalDefaults: %v", err)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("config path is no longer a symlink after save")
	}
	data, err := os.ReadFile(referent)
	if err != nil {
		t.Fatal(err)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("referent JSON: %v\n%s", err, data)
	}
	if got.Provider != "openai" || got.Model != "gpt-test" {
		t.Errorf("referent provider/model = %q/%q, want openai/gpt-test", got.Provider, got.Model)
	}
}

func TestSetGlobalDefaultsThroughGlobalRootDirSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".strike")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := SetGlobalDefaults("xai", "grok-test", "build", "", ""); err != nil {
		t.Fatalf("SetGlobalDefaults: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(target, "config"))
	if err != nil {
		t.Fatalf("read config under symlink target: %v", err)
	}
	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Provider != "xai" || got.Model != "grok-test" || got.DefaultAgent != "build" {
		t.Errorf("got %+v", got)
	}
}

func TestSetGlobalSchedulerPresetsAtomicAndPreservesCustom(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed global config with custom limits/commands and unrelated fields.
	if err := SetGlobalDefaults("openai", "gpt-test", "build", "", ""); err != nil {
		t.Fatalf("seed defaults: %v", err)
	}
	path := GlobalPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Scheduler = SchedulerConfig{
		Presets: []string{"npm"},
		Limits:  scheduler.Limits{"process": 4},
		Commands: []scheduler.CommandRule{
			{Pattern: "go test *", Class: scheduler.ClassTest},
		},
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	// Apply cargo+cmake out of catalog order; store should reorder.
	if err := SetGlobalSchedulerPresets([]string{"cargo", "cmake", "cargo"}); err == nil {
		t.Fatal("duplicate should be rejected")
	}
	if err := SetGlobalSchedulerPresets([]string{"cargo", "cmake"}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got, err := ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "openai" || got.Model != "gpt-test" {
		t.Fatalf("unrelated fields clobbered: %+v", got)
	}
	// Catalog order: cmake before cargo.
	if len(got.Scheduler.Presets) != 2 || got.Scheduler.Presets[0] != "cmake" || got.Scheduler.Presets[1] != "cargo" {
		t.Fatalf("presets=%v", got.Scheduler.Presets)
	}
	if got.Scheduler.Limits["process"] != 4 {
		t.Fatalf("custom limits lost: %+v", got.Scheduler.Limits)
	}
	if len(got.Scheduler.Commands) != 1 || got.Scheduler.Commands[0].Pattern != "go test *" {
		t.Fatalf("custom commands lost: %+v", got.Scheduler.Commands)
	}

	// Idempotent re-apply.
	if err := SetGlobalSchedulerPresets([]string{"cmake", "cargo"}); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	got2, err := ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if len(got2.Scheduler.Presets) != 2 {
		t.Fatalf("re-apply duplicated: %v", got2.Scheduler.Presets)
	}

	// Clear presets only.
	if err := SetGlobalSchedulerPresets(nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got3, err := ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if len(got3.Scheduler.Presets) != 0 {
		t.Fatalf("clear left presets: %v", got3.Scheduler.Presets)
	}
	if got3.Scheduler.Limits["process"] != 4 || len(got3.Scheduler.Commands) != 1 {
		t.Fatalf("clear clobbered custom: %+v", got3.Scheduler)
	}
}

func TestSetGlobalSchedulerPresetsRejectsUnknown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := SetGlobalSchedulerPresets([]string{"msbuild"}); err == nil {
		t.Fatal("unknown preset accepted")
	}
	got, err := ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Scheduler.Presets) != 0 {
		t.Fatalf("failed write still mutated: %v", got.Scheduler.Presets)
	}
}

func TestSetGlobalConfigDials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed unrelated fields so dials do not clobber them.
	if err := SetGlobalDefaults("echo", "echo", "build", protocol.EffortHigh, "default"); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalTheme("dracula"); err != nil {
		t.Fatal(err)
	}

	if err := SetGlobalConfigDials("read-only", "on", "full", "on", "auto"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.Sandbox != "read-only" {
		t.Errorf("sandbox = %q", got.Sandbox)
	}
	if got.Notify != NotifyOn {
		t.Errorf("notify = %q", got.Notify)
	}
	if got.LeanCode != LeanCodeFull {
		t.Errorf("leanCode = %q", got.LeanCode)
	}
	if got.DeferTools != DeferToolsOn {
		t.Errorf("deferTools = %q", got.DeferTools)
	}
	if got.Session.Worktree != "auto" {
		t.Errorf("session.worktree = %q", got.Session.Worktree)
	}
	if got.Provider != "echo" || got.Theme != "dracula" {
		t.Errorf("clobbered unrelated: provider=%q theme=%q", got.Provider, got.Theme)
	}

	// Partial update leaves other dials.
	if err := SetGlobalConfigDials("off", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	got, err = ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.Sandbox != "off" || got.Notify != NotifyOn || got.Session.Worktree != "auto" {
		t.Fatalf("partial dials = sandbox=%q notify=%q wt=%q", got.Sandbox, got.Notify, got.Session.Worktree)
	}
}

func TestSetGlobalConfigDialsRejectsUnknown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := []struct {
		name string
		fn   func() error
	}{
		{"sandbox", func() error { return SetGlobalConfigDials("nope", "", "", "", "") }},
		{"notify", func() error { return SetGlobalConfigDials("", "sometimes", "", "", "") }},
		{"leanCode", func() error { return SetGlobalConfigDials("", "", "maybe", "", "") }},
		{"deferTools", func() error { return SetGlobalConfigDials("", "", "", "maybe", "") }},
		{"worktree", func() error { return SetGlobalConfigDials("", "", "", "", "sometimes") }},
	}
	for _, tc := range cases {
		if err := tc.fn(); err == nil {
			t.Errorf("%s: accepted unknown", tc.name)
		}
	}
	got, err := ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.Sandbox != "" || got.Notify != "" || got.LeanCode != "" || got.DeferTools != "" || got.Session.Worktree != "" {
		t.Fatalf("reject mutated config: %+v", got)
	}
}

func TestSetGlobalCompactionDials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := SetGlobalDefaults("echo", "echo", "build", protocol.EffortHigh, "default"); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalCompactionDials(CompactionDials{
		Strategy:           "summarize",
		Model:              "gpt-compact",
		Threshold:          "0.80",
		Buffer:             "2048",
		KeepUserTurns:      "3",
		PruneProtectTokens: "10000",
		PruneMinimumTokens: "5000",
		PruneKeepUserTurns: "1",
		PruneProtectTools:  "webfetch, Bash",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.CompactionStrategy != "summarize" {
		t.Errorf("strategy = %q", got.CompactionStrategy)
	}
	if got.CompactionModel != "gpt-compact" {
		t.Errorf("model = %q", got.CompactionModel)
	}
	if got.CompactionThreshold != 0.80 {
		t.Errorf("threshold = %v", got.CompactionThreshold)
	}
	if got.CompactionBuffer != 2048 {
		t.Errorf("buffer = %d", got.CompactionBuffer)
	}
	if got.KeepUserTurns != 3 {
		t.Errorf("keepUserTurns = %d", got.KeepUserTurns)
	}
	if got.PruneProtectTokens != 10000 || got.PruneMinimumTokens != 5000 || got.PruneKeepUserTurns != 1 {
		t.Errorf("prune ints = protect=%d min=%d keep=%d", got.PruneProtectTokens, got.PruneMinimumTokens, got.PruneKeepUserTurns)
	}
	if len(got.PruneProtectTools) != 2 || got.PruneProtectTools[0] != "webfetch" || got.PruneProtectTools[1] != "bash" {
		t.Errorf("tools = %#v", got.PruneProtectTools)
	}
	if got.Provider != "echo" {
		t.Errorf("clobbered provider: %q", got.Provider)
	}

	// Partial update + clear tokens.
	if err := SetGlobalCompactionDials(CompactionDials{
		Strategy:          "trim",
		Model:             "-",
		Threshold:         "default",
		PruneProtectTools: "-",
	}); err != nil {
		t.Fatal(err)
	}
	got, err = ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.CompactionStrategy != "trim" {
		t.Errorf("strategy after partial = %q", got.CompactionStrategy)
	}
	if got.CompactionModel != "" {
		t.Errorf("model clear = %q", got.CompactionModel)
	}
	if got.CompactionThreshold != 0 {
		t.Errorf("threshold default = %v", got.CompactionThreshold)
	}
	if got.CompactionBuffer != 2048 {
		t.Errorf("buffer should remain 2048, got %d", got.CompactionBuffer)
	}
	if got.PruneProtectTools != nil {
		t.Errorf("tools clear = %#v", got.PruneProtectTools)
	}
}

func TestSetGlobalCompactionDialsRejectsUnknown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed a valid value so reject must not clobber.
	if err := SetGlobalCompactionDials(CompactionDials{Strategy: "trim", Buffer: "4096"}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		d    CompactionDials
	}{
		{"strategy", CompactionDials{Strategy: "nope"}},
		{"threshold", CompactionDials{Threshold: "abc"}},
		{"threshold-neg", CompactionDials{Threshold: "-1"}},
		{"buffer", CompactionDials{Buffer: "x"}},
		{"buffer-neg", CompactionDials{Buffer: "-4"}},
		{"keep", CompactionDials{KeepUserTurns: "nope"}},
		{"pruneProtect", CompactionDials{PruneProtectTokens: "bad"}},
		{"pruneMin", CompactionDials{PruneMinimumTokens: "bad"}},
		{"pruneKeep", CompactionDials{PruneKeepUserTurns: "bad"}},
	}
	for _, tc := range cases {
		if err := SetGlobalCompactionDials(tc.d); err == nil {
			t.Errorf("%s: accepted invalid", tc.name)
		}
	}
	got, err := ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.CompactionStrategy != "trim" || got.CompactionBuffer != 4096 {
		t.Fatalf("reject mutated config: strategy=%q buffer=%d", got.CompactionStrategy, got.CompactionBuffer)
	}
}

func TestSetGlobalAutoApproveDials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := SetGlobalDefaults("echo", "echo", "build", protocol.EffortHigh, "default"); err != nil {
		t.Fatal(err)
	}

	exclude := []string{" Bash ", "bash", "write", ""}
	if err := SetGlobalAutoApproveDials("15", &exclude, "2"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.PermissionAutoApproveSeconds != 15 {
		t.Errorf("seconds = %d", got.PermissionAutoApproveSeconds)
	}
	if len(got.PermissionAutoApproveExclude) != 2 || got.PermissionAutoApproveExclude[0] != "bash" || got.PermissionAutoApproveExclude[1] != "write" {
		t.Errorf("exclude = %#v", got.PermissionAutoApproveExclude)
	}
	if got.MaxChildDepth != 2 {
		t.Errorf("maxChildDepth = %d", got.MaxChildDepth)
	}
	if got.Provider != "echo" {
		t.Errorf("clobbered provider = %q", got.Provider)
	}

	// Partial: seconds only; exclude nil leaves list; depth empty leaves.
	if err := SetGlobalAutoApproveDials("off", nil, ""); err != nil {
		t.Fatal(err)
	}
	got, err = ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.PermissionAutoApproveSeconds != 0 {
		t.Errorf("seconds after off = %d", got.PermissionAutoApproveSeconds)
	}
	if len(got.PermissionAutoApproveExclude) != 2 {
		t.Errorf("exclude cleared unexpectedly: %#v", got.PermissionAutoApproveExclude)
	}
	if got.MaxChildDepth != 2 {
		t.Errorf("depth cleared unexpectedly: %d", got.MaxChildDepth)
	}

	// Clear exclude with empty slice pointer.
	empty := []string{}
	if err := SetGlobalAutoApproveDials("", &empty, "default"); err != nil {
		t.Fatal(err)
	}
	got, err = ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.PermissionAutoApproveExclude != nil {
		t.Errorf("exclude not cleared: %#v", got.PermissionAutoApproveExclude)
	}
	if got.MaxChildDepth != 0 {
		t.Errorf("depth default = %d", got.MaxChildDepth)
	}

	// Clamp high values.
	if err := SetGlobalAutoApproveDials("99", nil, "20"); err != nil {
		t.Fatal(err)
	}
	got, err = ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.PermissionAutoApproveSeconds != 60 {
		t.Errorf("seconds clamp = %d", got.PermissionAutoApproveSeconds)
	}
	if got.MaxChildDepth != MaxChildDepthCeiling {
		t.Errorf("depth clamp = %d", got.MaxChildDepth)
	}
}

func TestSetGlobalAutoApproveDialsRejectsUnknown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := SetGlobalAutoApproveDials("maybe", nil, ""); err == nil {
		t.Fatal("accepted unknown seconds")
	}
	if err := SetGlobalAutoApproveDials("", nil, "deep"); err == nil {
		t.Fatal("accepted unknown depth")
	}
	got, err := ReadGlobalDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got.PermissionAutoApproveSeconds != 0 || got.MaxChildDepth != 0 {
		t.Fatalf("reject mutated config: %+v", got)
	}
}

func TestClampMaxChildDepth(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{-1, 0},
		{0, 0},
		{1, 1},
		{8, 8},
		{9, 8},
		{100, 8},
	}
	for _, tt := range cases {
		if got := ClampMaxChildDepth(tt.in); got != tt.want {
			t.Errorf("ClampMaxChildDepth(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
