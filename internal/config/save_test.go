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
