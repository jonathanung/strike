package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func writeAgent(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, ".strike", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAgentFrontmatterEffortIsParsed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	writeAgent(t, work, "deep", "---\ndescription: deep thinker\neffort: Max\n---\nYou think hard.\n")

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatalf("LoadAgentsWithError: %v", err)
	}
	got, ok := lookupAgent(agents, "deep")
	if !ok {
		t.Fatalf("missing deep among %v", agentNames(agents))
	}
	if got.Effort != protocol.EffortMax {
		t.Errorf("effort = %q, want max", got.Effort)
	}
}

// TestAgentWithoutEffortIsUnset: omitting the key must not invent a level.
func TestAgentWithoutEffortIsUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	writeAgent(t, work, "plain", "---\ndescription: plain\n---\nBody.\n")

	agents, err := LoadAgentsWithError(work)
	if err != nil {
		t.Fatalf("LoadAgentsWithError: %v", err)
	}
	got, ok := lookupAgent(agents, "plain")
	if !ok {
		t.Fatalf("missing plain among %v", agentNames(agents))
	}
	if got.Effort != protocol.EffortDefault {
		t.Errorf("effort = %q, want unset", got.Effort)
	}
}

// TestAgentWithBadEffortIsRejected surfaces a typo at load time rather than
// letting it reach a provider that would 400.
func TestAgentWithBadEffortIsRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	writeAgent(t, work, "oops", "---\neffort: turbo\n---\nBody.\n")

	if _, err := LoadAgentsWithError(work); err == nil {
		t.Fatal("LoadAgentsWithError accepted effort \"turbo\", want an error")
	}
}

func TestSetGlobalDefaultsPersistsEffortAndPreservesTheRest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := SetGlobalDefaults("anthropic", "claude-opus-5", "build", protocol.EffortXHigh, ""); err != nil {
		t.Fatalf("SetGlobalDefaults: %v", err)
	}
	// A later save that touches only the model must leave effort alone.
	if err := SetGlobalDefaults("", "claude-sonnet-5", "", "", ""); err != nil {
		t.Fatalf("SetGlobalDefaults: %v", err)
	}

	data, err := os.ReadFile(GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Effort != protocol.EffortXHigh {
		t.Errorf("effort = %q, want xhigh preserved across the second save", cfg.Effort)
	}
	if cfg.Model != "claude-sonnet-5" {
		t.Errorf("model = %q, want claude-sonnet-5", cfg.Model)
	}
	if cfg.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic preserved", cfg.Provider)
	}
}

func TestSetGlobalDefaultsRejectsUnknownEffort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := SetGlobalDefaults("", "", "", protocol.Effort("turbo"), ""); err == nil {
		t.Fatal("SetGlobalDefaults accepted effort \"turbo\", want an error")
	}
}

// TestProjectConfigEffortOverridesGlobal follows the documented layering:
// defaults <- global <- project.
func TestProjectConfigEffortOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	writeJSON(t, GlobalPath(), Config{Effort: protocol.EffortLow})
	writeJSON(t, filepath.Join(work, ".strike", "config"), Config{Effort: protocol.EffortMax})

	cfg, err := Load(work)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Effort != protocol.EffortMax {
		t.Errorf("effort = %q, want the project layer's max", cfg.Effort)
	}
}

func writeJSON(t *testing.T, path string, cfg Config) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
