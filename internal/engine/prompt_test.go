package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestSharedBaselineHasADHDAndTools(t *testing.T) {
	p := engine.SharedSystemPrompt
	for _, want := range []string{
		"You are strike",
		"Response contract (ADHD-shaped, always on)",
		"Lead with the next action",
		"Available: `read`, `glob`, `grep`, `edit`, `write`, `apply_patch`, `bash`, `task`, `webfetch`, `todowrite`, `todoread`, `notebook_edit`, `sleep`, `skill`, `question`, `enter_plan_mode`, `exit_plan_mode`, `toolsearch`",
		"NEVER commit unless the user explicitly asks",
		"/help",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("SharedSystemPrompt missing %q", want)
		}
	}
	if strings.Contains(p, "stop adhd mode") {
		t.Error("SharedSystemPrompt must not offer stop-adhd toggle")
	}
}

func TestProviderSystemPromptSelection(t *testing.T) {
	cases := []struct {
		provider, model, wantSnippet string
	}{
		{"anthropic", "claude-sonnet-5", "Provider notes (Anthropic / Claude)"},
		{"openai", "gpt-5.5", "Provider notes (OpenAI / GPT)"},
		{"chatgpt", "gpt-5.5", "Provider notes (OpenAI / GPT)"},
		{"xai", "grok-4.5", "Provider notes (xAI / Grok)"},
		{"echo", "echo", "Provider notes (default)"},
		{"", "claude-opus", "Provider notes (Anthropic / Claude)"},
		{"", "grok-beta", "Provider notes (xAI / Grok)"},
		{"", "o3-mini", "Provider notes (OpenAI / GPT)"},
		{"", "unknown-model", "Provider notes (default)"},
	}
	for _, tt := range cases {
		got := engine.ProviderSystemPrompt(tt.provider, tt.model)
		if !strings.Contains(got, tt.wantSnippet) {
			t.Errorf("ProviderSystemPrompt(%q,%q) missing %q\n%s", tt.provider, tt.model, tt.wantSnippet, got)
		}
	}
}

func TestDefaultSystemPromptStacksSharedAndDefaultProvider(t *testing.T) {
	p := engine.DefaultSystemPrompt
	if !strings.Contains(p, "Response contract (ADHD-shaped, always on)") {
		t.Fatal("DefaultSystemPrompt missing shared baseline")
	}
	if !strings.Contains(p, "Provider notes (default)") {
		t.Fatal("DefaultSystemPrompt missing default provider layer")
	}
	sharedIdx := strings.Index(p, "You are strike")
	provIdx := strings.Index(p, "Provider notes (default)")
	if !(sharedIdx >= 0 && provIdx > sharedIdx) {
		t.Fatalf("layer order wrong: shared=%d provider=%d", sharedIdx, provIdx)
	}
}

func TestPlanSystemPromptIsReadOnly(t *testing.T) {
	p := engine.PlanSystemPrompt
	for _, want := range []string{
		"Plan mode (read-only)",
		"MUST NOT edit",
		"Lead with the recommended path",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("PlanSystemPrompt missing %q", want)
		}
	}
}

func TestSystemPromptComposesProviderForBuild(t *testing.T) {
	sys := captureSystemPrompt(t, engine.Options{
		WorkDir: t.TempDir(),
		Agents:  []engine.Agent{{Name: "build"}},
	}, "anthropic", "claude-sonnet-5")

	for _, want := range []string{
		"Response contract (ADHD-shaped, always on)",
		"Provider notes (Anthropic / Claude)",
		"You are powered by the model named claude-sonnet-5",
		"anthropic/claude-sonnet-5",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system missing %q\n---\n%s", want, sys)
		}
	}
	if strings.Contains(sys, "Plan mode (read-only)") {
		t.Error("build agent must not include plan overlay")
	}
}

func TestSystemPromptSwitchesProviderOverlay(t *testing.T) {
	sys := captureSystemPrompt(t, engine.Options{
		WorkDir: t.TempDir(),
		Agents:  []engine.Agent{{Name: "build"}},
	}, "xai", "grok-4.5")
	if !strings.Contains(sys, "Provider notes (xAI / Grok)") {
		t.Fatalf("expected xai overlay:\n%s", sys)
	}
	if strings.Contains(sys, "Provider notes (Anthropic / Claude)") {
		t.Fatal("anthropic overlay leaked into xai request")
	}
}

func TestSystemPromptPlanAgentAddsOverlay(t *testing.T) {
	sys := captureSystemPrompt(t, engine.Options{
		WorkDir:      t.TempDir(),
		Agents:       []engine.Agent{{Name: "plan"}},
		InitialAgent: "plan",
	}, "openai", "gpt-5.5")

	for _, want := range []string{
		"Response contract (ADHD-shaped, always on)",
		"Provider notes (OpenAI / GPT)",
		"Plan mode (read-only)",
		"MUST NOT edit",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("plan system missing %q\n---\n%s", want, sys)
		}
	}
}

func TestSystemPromptConfigOverridesProviderForBuild(t *testing.T) {
	sys := captureSystemPrompt(t, engine.Options{
		WorkDir:      t.TempDir(),
		Agents:       []engine.Agent{{Name: "build"}},
		SystemPrompt: "CUSTOM_SYSTEM_OVERLAY",
	}, "anthropic", "claude-sonnet-5")

	if !strings.Contains(sys, "CUSTOM_SYSTEM_OVERLAY") {
		t.Fatal("config systemPrompt not applied")
	}
	if strings.Contains(sys, "Provider notes (Anthropic / Claude)") {
		t.Fatal("provider overlay should be replaced by config systemPrompt")
	}
	if !strings.Contains(sys, "Response contract (ADHD-shaped, always on)") {
		t.Fatal("shared baseline must remain")
	}
}

func TestSystemPromptCustomAgentPersona(t *testing.T) {
	sys := captureSystemPrompt(t, engine.Options{
		WorkDir: t.TempDir(),
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "reviewer", Prompt: "PERSONA_REVIEWER"},
		},
		InitialAgent: "reviewer",
	}, "openai", "gpt-5.5")

	if !strings.Contains(sys, "PERSONA_REVIEWER") {
		t.Fatal("custom agent persona missing")
	}
	if strings.Contains(sys, "Provider notes (OpenAI / GPT)") {
		t.Fatal("provider overlay should yield to custom agent prompt")
	}
	if !strings.Contains(sys, "Response contract (ADHD-shaped, always on)") {
		t.Fatal("shared baseline must remain for custom agents")
	}
}

func TestSystemPromptComposesEnvironmentAndInstructions(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "pkg")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}

	sys := captureSystemPrompt(t, engine.Options{
		WorkDir:      work,
		ProjectRoot:  root,
		Instructions: []string{"Instructions from: /tmp/AGENTS.md\nUse make test."},
		Agents:       []engine.Agent{{Name: "build", Prompt: "BASE_PROMPT_MARKER"}},
	}, "scripted", "model-a")

	for _, want := range []string{
		"Response contract (ADHD-shaped, always on)",
		"BASE_PROMPT_MARKER",
		"Working directory: " + work,
		"Workspace root folder: " + root,
		"Is directory a git repo: yes",
		"You are powered by the model named model-a",
		"scripted/model-a",
		"Instructions from: /tmp/AGENTS.md",
		"Use make test.",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing %q\n---\n%s", want, sys)
		}
	}
	baseIdx := strings.Index(sys, "You are strike")
	personaIdx := strings.Index(sys, "BASE_PROMPT_MARKER")
	envIdx := strings.Index(sys, "<env>")
	instIdx := strings.Index(sys, "Instructions from:")
	if !(baseIdx >= 0 && personaIdx > baseIdx && envIdx > personaIdx && instIdx > envIdx) {
		t.Fatalf("layer order wrong: shared=%d persona=%d env=%d inst=%d", baseIdx, personaIdx, envIdx, instIdx)
	}
}

// captureSystemPrompt starts an engine, selects provider/model, sends one
// user turn, and returns the System string from the first Stream request.
func captureSystemPrompt(t *testing.T, opts engine.Options, providerName, model string) string {
	t.Helper()
	prov := newScriptedProvider(streamStep{
		events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		},
	})
	opts.SessionID = "s-prompt"
	opts.Select = func(string) (provider.Provider, string, error) {
		return prov, model, nil
	}
	if opts.Registry == nil {
		opts.Registry = tool.NewRegistry()
	}
	opts.InitialProvider = providerName
	opts.InitialModel = model

	eng := engine.New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hi"}

	var req provider.Request
	deadline := time.After(5 * time.Second)
	for req.System == "" {
		select {
		case req = <-prov.requests:
		case ev := <-eng.Events():
			if err, ok := ev.(protocol.EngineError); ok {
				t.Fatalf("engine error: %s", err.Message)
			}
		case <-deadline:
			t.Fatal("timeout waiting for Stream request")
		}
	}
	return req.System
}
