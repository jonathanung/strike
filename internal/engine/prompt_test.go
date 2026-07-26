package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/memory"
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
		"NEVER commit unless the user explicitly asks",
		"/help",
		"# Doing tasks",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("SharedSystemPrompt missing %q", want)
		}
	}
	// Static tool inventory moved to the effective registry tools layer.
	if strings.Contains(p, "Available: `read`") || strings.Contains(p, "# Available tools") {
		t.Error("SharedSystemPrompt must not embed static tool inventory")
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
		"hard-denied",
		"MUST NOT run non-readonly",
		"Interview first",
		"Push back on vague scope",
		"Ask before assuming",
		"Prefer the `question` tool",
		"Lead with the recommended path",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("PlanSystemPrompt missing %q", want)
		}
	}
}

func TestLeanCodePromptSelection(t *testing.T) {
	strict := engine.LeanCodePrompt("lite", "build")
	for _, want := range []string{
		"Lean code (implementer)",
		"YAGNI",
		"Skip — does this need to exist",
		"Reuse — already solved",
		"Stdlib",
		"Native platform",
		"Already-installed dependency",
		"One clear line",
		"minimum correct change",
		"Never sacrifice",
		"validation",
		"trust boundaries",
	} {
		if !strings.Contains(strict, want) {
			t.Errorf("strict lean missing %q\n%s", want, strict)
		}
	}
	full := engine.LeanCodePrompt("full", "general")
	if !strings.Contains(full, "Lean code (implementer, full)") {
		t.Fatalf("full mode should use full strict text:\n%s", full)
	}
	if !strings.Contains(full, "speculative layers") {
		t.Fatal("full strict missing stronger guidance")
	}
	strategic := engine.LeanCodePrompt("lite", "plan")
	for _, want := range []string{
		"Lean code (planning)",
		"still scales",
		"smallest-change",
		"Never trade away",
	} {
		if !strings.Contains(strategic, want) {
			t.Errorf("strategic lean missing %q\n%s", want, strategic)
		}
	}
	orch := engine.LeanCodePrompt("lite", "orchestrator")
	if !strings.Contains(orch, "Lean code (planning)") {
		t.Fatal("orchestrator should get strategic lean")
	}
	for _, agent := range []string{"explore", "reviewer", "tester", "validator", "commit", "pr-babysitter"} {
		if got := engine.LeanCodePrompt("lite", agent); got != "" {
			t.Errorf("agent %q must not get lean overlay, got:\n%s", agent, got)
		}
	}
	if got := engine.LeanCodePrompt("off", "build"); got != "" {
		t.Fatalf("leanCode=off must disable overlay, got:\n%s", got)
	}
}

func TestLeanCodeStrength(t *testing.T) {
	cases := []struct {
		agent, want string
	}{
		{"build", "strict"},
		{"general", "strict"},
		{"debugger", "strict"},
		{"plan", "strategic"},
		{"orchestrator", "strategic"},
		{"explore", ""},
		{"reviewer", ""},
		{"tester", ""},
		{"validator", ""},
		{"commit", ""},
		{"custom", ""},
	}
	for _, tt := range cases {
		if got := engine.LeanCodeStrength(tt.agent); got != tt.want {
			t.Errorf("LeanCodeStrength(%q) = %q, want %q", tt.agent, got, tt.want)
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
		"Lean code (implementer)",
		"YAGNI",
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
	provIdx := strings.Index(sys, "Provider notes (Anthropic / Claude)")
	leanIdx := strings.Index(sys, "Lean code (implementer)")
	envIdx := strings.Index(sys, "<env>")
	if !(provIdx >= 0 && leanIdx > provIdx && envIdx > leanIdx) {
		t.Fatalf("lean layer order wrong: provider=%d lean=%d env=%d", provIdx, leanIdx, envIdx)
	}
}

func TestSystemPromptLeanCodeAgentScoped(t *testing.T) {
	dir := t.TempDir()
	buildSys := captureSystemPrompt(t, engine.Options{
		WorkDir: dir,
		Agents:  []engine.Agent{{Name: "build"}},
	}, "echo", "echo")
	if !strings.Contains(buildSys, "Lean code (implementer)") || !strings.Contains(buildSys, "YAGNI") {
		t.Fatalf("build missing strict lean ladder:\n%s", buildSys)
	}

	planSys := captureSystemPrompt(t, engine.Options{
		WorkDir:      dir,
		Agents:       []engine.Agent{{Name: "plan"}},
		InitialAgent: "plan",
	}, "echo", "echo")
	if !strings.Contains(planSys, "Lean code (planning)") {
		t.Fatalf("plan missing strategic lean:\n%s", planSys)
	}
	if strings.Contains(planSys, "Lean code (implementer)") {
		t.Fatal("plan must not get implementer lean block")
	}

	orchSys := captureSystemPrompt(t, engine.Options{
		WorkDir: dir,
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "orchestrator", Prompt: "ORCH_PERSONA"},
		},
		InitialAgent: "orchestrator",
	}, "echo", "echo")
	if !strings.Contains(orchSys, "Lean code (planning)") {
		t.Fatalf("orchestrator missing strategic lean:\n%s", orchSys)
	}

	for _, agent := range []string{"explore", "reviewer"} {
		sys := captureSystemPrompt(t, engine.Options{
			WorkDir: dir,
			Agents: []engine.Agent{
				{Name: "build"},
				{Name: agent, Prompt: "PERSONA_" + strings.ToUpper(agent)},
			},
			InitialAgent: agent,
		}, "echo", "echo")
		if strings.Contains(sys, "Lean code (implementer)") || strings.Contains(sys, "YAGNI") {
			t.Fatalf("%s must not get strict implementer lean:\n%s", agent, sys)
		}
		if strings.Contains(sys, "Lean code (planning)") {
			t.Fatalf("%s must not get strategic lean:\n%s", agent, sys)
		}
	}

	offSys := captureSystemPrompt(t, engine.Options{
		WorkDir:  dir,
		LeanCode: "off",
		Agents:   []engine.Agent{{Name: "build"}},
	}, "echo", "echo")
	if strings.Contains(offSys, "Lean code") {
		t.Fatalf("leanCode=off must omit lean layer:\n%s", offSys)
	}

	fullSys := captureSystemPrompt(t, engine.Options{
		WorkDir:  dir,
		LeanCode: "full",
		Agents:   []engine.Agent{{Name: "build"}},
	}, "echo", "echo")
	if !strings.Contains(fullSys, "Lean code (implementer, full)") {
		t.Fatalf("leanCode=full should use full strict text:\n%s", fullSys)
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
		"hard-denied",
		"MUST NOT run non-readonly",
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
	leanIdx := strings.Index(sys, "Lean code (implementer)")
	envIdx := strings.Index(sys, "<env>")
	instIdx := strings.Index(sys, "Instructions from:")
	if !(baseIdx >= 0 && personaIdx > baseIdx && leanIdx > personaIdx && envIdx > leanIdx && instIdx > envIdx) {
		t.Fatalf("layer order wrong: shared=%d persona=%d lean=%d env=%d inst=%d", baseIdx, personaIdx, leanIdx, envIdx, instIdx)
	}
}

func openTestMemory(t *testing.T) *memory.Store {
	t.Helper()
	s, err := memory.Open(t.TempDir(), "engine-prompt-mem")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSystemPromptAutoLoadsTaggedMemory(t *testing.T) {
	store := openTestMemory(t)
	if err := store.Put("test.priority", "Always run make test first.", []string{"preference"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("scratch", "do not inject me", nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("api.url", "https://example.invalid", []string{"config"}); err != nil {
		t.Fatal(err)
	}

	sys := captureSystemPrompt(t, engine.Options{
		WorkDir: t.TempDir(),
		Memory:  store,
		Agents:  []engine.Agent{{Name: "build"}},
	}, "scripted", "model-a")

	for _, want := range []string{
		"# Project memory (untrusted)",
		"## test.priority",
		"Always run make test first.",
		"tags: preference",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system missing %q\n---\n%s", want, sys)
		}
	}
	if strings.Contains(sys, "do not inject me") {
		t.Fatal("untagged memory must not auto-load")
	}
	if strings.Contains(sys, "https://example.invalid") {
		t.Fatal("non-autoload tag must not inject")
	}
	// Issues must never appear via memory path (no issue store wired).
	if strings.Contains(sys, "issue #") || strings.Contains(strings.ToLower(sys), "open issues") {
		t.Fatal("issues must not be auto-injected")
	}

	instIdx := strings.Index(sys, "You are powered by")
	memIdx := strings.Index(sys, "# Project memory (untrusted)")
	if !(instIdx >= 0 && memIdx > instIdx) {
		t.Fatalf("memory layer should follow env/instructions: env=%d mem=%d", instIdx, memIdx)
	}
}

func TestSystemPromptMemoryWriteVisibleSameSession(t *testing.T) {
	store := openTestMemory(t)
	prov := newScriptedProvider(
		streamStep{
			events: []provider.StreamEvent{
				{Type: provider.EventTextDelta, Text: "one"},
				{Type: provider.EventDone, StopReason: "end_turn"},
			},
		},
		streamStep{
			events: []provider.StreamEvent{
				{Type: provider.EventTextDelta, Text: "two"},
				{Type: provider.EventDone, StopReason: "end_turn"},
			},
		},
	)
	eng := engine.New(engine.Options{
		SessionID: "s-mem-refresh",
		WorkDir:   t.TempDir(),
		Memory:    store,
		Agents:    []engine.Agent{{Name: "build"}},
		Registry:  tool.NewRegistry(),
		Select: func(string) (provider.Provider, string, error) {
			return prov, "model-a", nil
		},
		InitialProvider: "scripted",
		InitialModel:    "model-a",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "first"}
	req1 := waitStreamRequest(t, eng, prov)
	if strings.Contains(req1.System, "SAME_SESSION_PREF") {
		t.Fatal("preference should not exist before write")
	}
	waitTurnCompleted(t, eng)

	if err := store.Put("live.pref", "SAME_SESSION_PREF", []string{"instruction"}); err != nil {
		t.Fatal(err)
	}

	eng.Ops() <- protocol.UserInput{Text: "second"}
	req2 := waitStreamRequest(t, eng, prov)
	if !strings.Contains(req2.System, "SAME_SESSION_PREF") {
		t.Fatalf("tagged memory_write should appear on next turn:\n%s", req2.System)
	}
}

func TestSystemPromptNoMemoryLayerWhenEmpty(t *testing.T) {
	store := openTestMemory(t)
	if err := store.Put("note-only", "scratch", nil); err != nil {
		t.Fatal(err)
	}
	sys := captureSystemPrompt(t, engine.Options{
		WorkDir: t.TempDir(),
		Memory:  store,
		Agents:  []engine.Agent{{Name: "build"}},
	}, "scripted", "model-a")
	if strings.Contains(sys, "# Project memory (untrusted)") {
		t.Fatal("empty auto-load set must omit project memory layer")
	}
}

func TestAgentSwitchReplacesPersonaLayer(t *testing.T) {
	prov := newScriptedProvider(
		streamStep{
			events: []provider.StreamEvent{
				{Type: provider.EventTextDelta, Text: "one"},
				{Type: provider.EventDone, StopReason: "end_turn"},
			},
		},
		streamStep{
			events: []provider.StreamEvent{
				{Type: provider.EventTextDelta, Text: "two"},
				{Type: provider.EventDone, StopReason: "end_turn"},
			},
		},
	)
	eng := engine.New(engine.Options{
		SessionID: "s-persona-switch",
		WorkDir:   t.TempDir(),
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "reviewer", Prompt: "PERSONA_REVIEWER_ONLY"},
			{Name: "tester", Prompt: "PERSONA_TESTER_ONLY"},
		},
		InitialAgent:    "reviewer",
		InitialProvider: "echo",
		InitialModel:    "echo",
		Registry:        tool.NewRegistry(),
		Select: func(string) (provider.Provider, string, error) {
			return prov, "echo", nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "first"}
	req1 := waitStreamRequest(t, eng, prov)
	if !strings.Contains(req1.System, "PERSONA_REVIEWER_ONLY") {
		t.Fatalf("first stream missing reviewer persona:\n%s", req1.System)
	}
	if strings.Contains(req1.System, "PERSONA_TESTER_ONLY") {
		t.Fatal("tester persona leaked into reviewer stream")
	}
	waitTurnCompleted(t, eng)

	eng.Ops() <- protocol.SelectAgent{Name: "tester"}
	waitAgentSelected(t, eng, "tester")

	eng.Ops() <- protocol.UserInput{Text: "second"}
	req2 := waitStreamRequest(t, eng, prov)
	if !strings.Contains(req2.System, "PERSONA_TESTER_ONLY") {
		t.Fatalf("second stream missing tester persona:\n%s", req2.System)
	}
	if strings.Contains(req2.System, "PERSONA_REVIEWER_ONLY") {
		t.Fatal("prior reviewer persona duplicated after agent switch")
	}
	if strings.Contains(req2.System, "Provider notes") {
		t.Fatal("provider overlay should yield to persona")
	}
}

func TestInspectEffectivePromptMatchesStream(t *testing.T) {
	prov := newScriptedProvider(streamStep{
		events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		},
	})
	secret := "sk-ant-api03-SUPERSECRETKEYVALUE99"
	eng := engine.New(engine.Options{
		SessionID: "s-inspect",
		WorkDir:   t.TempDir(),
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "reviewer", Prompt: "PERSONA_WITH_KEY " + secret},
		},
		InitialAgent:    "reviewer",
		InitialProvider: "anthropic",
		InitialModel:    "claude-sonnet-5",
		Instructions:    []string{"Instructions from: /tmp/AGENTS.md\nUse make test. key=" + secret},
		Registry:        tool.NewRegistry(),
		Select: func(string) (provider.Provider, string, error) {
			return prov, "claude-sonnet-5", nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hi"}
	req := waitStreamRequest(t, eng, prov)
	waitTurnCompleted(t, eng)

	eng.Ops() <- protocol.InspectEffectivePrompt{}
	ev := waitEffectivePrompt(t, eng)
	if !ev.FromLastStream {
		t.Fatal("expected FromLastStream after a completed Stream")
	}
	if ev.SystemChars == 0 {
		t.Fatal("SystemChars = 0")
	}
	if ev.MessageCount < 1 {
		t.Fatalf("MessageCount = %d, want >= 1", ev.MessageCount)
	}

	kinds := make([]string, len(ev.Layers))
	for i, layer := range ev.Layers {
		kinds[i] = layer.Kind
		if layer.Source == "" || layer.Mode == "" {
			t.Fatalf("layer %d missing source/mode: %+v", i, layer)
		}
		if strings.Contains(layer.Source, secret) || strings.Contains(layer.Preview, secret) {
			t.Fatalf("secret leaked in layer %d: %+v", i, layer)
		}
		if strings.Contains(layer.Preview, "sk-ant-") {
			t.Fatalf("api key prefix leaked in preview: %q", layer.Preview)
		}
	}
	wantOrder := []string{
		protocol.PromptLayerShared,
		protocol.PromptLayerPersona,
		protocol.PromptLayerEnvironment,
		protocol.PromptLayerInstruction,
	}
	if len(kinds) < len(wantOrder) {
		t.Fatalf("kinds = %v, want prefix %v", kinds, wantOrder)
	}
	for i, want := range wantOrder {
		if kinds[i] != want {
			t.Fatalf("kinds[%d] = %q, want %q (full %v)", i, kinds[i], want, kinds)
		}
	}
	if kinds[1] != protocol.PromptLayerPersona {
		t.Fatal("persona layer missing")
	}
	// Inspect sizes must describe the same system string Stream received.
	if got := len([]rune(strings.TrimSpace(req.System))); got != ev.SystemChars {
		t.Fatalf("SystemChars = %d, stream system runes = %d", ev.SystemChars, got)
	}
	if !strings.Contains(req.System, "PERSONA_WITH_KEY") {
		t.Fatal("stream system missing persona marker")
	}
	if !strings.Contains(req.System, secret) {
		// Stream may still carry secrets (real request); inspect must not.
		t.Fatal("stream system should still contain the raw persona key for this fixture")
	}
}

func TestInspectEffectivePromptBeforeStreamUsesCurrent(t *testing.T) {
	eng := engine.New(engine.Options{
		SessionID: "s-inspect-current",
		WorkDir:   t.TempDir(),
		Agents:    []engine.Agent{{Name: "build", Prompt: "BUILD_PERSONA"}},
		Registry:  tool.NewRegistry(),
		Select: func(string) (provider.Provider, string, error) {
			return newScriptedProvider(), "m", nil
		},
		InitialProvider: "echo",
		InitialModel:    "echo",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Drain startup selection events.
	deadline := time.After(2 * time.Second)
drain:
	for {
		select {
		case ev := <-eng.Events():
			if _, ok := ev.(protocol.EngineError); ok {
				t.Fatalf("engine error: %#v", ev)
			}
		case <-time.After(50 * time.Millisecond):
			break drain
		case <-deadline:
			break drain
		}
	}

	eng.Ops() <- protocol.InspectEffectivePrompt{}
	ev := waitEffectivePrompt(t, eng)
	if ev.FromLastStream {
		t.Fatal("expected current composition before any Stream")
	}
	foundPersona := false
	for _, layer := range ev.Layers {
		if layer.Kind == protocol.PromptLayerPersona && strings.Contains(layer.Source, "build") {
			foundPersona = true
		}
	}
	if !foundPersona {
		t.Fatalf("persona layer missing: %+v", ev.Layers)
	}
}

func TestPhaseContextDoesNotEnterHistory(t *testing.T) {
	prov := newScriptedProvider(streamStep{
		events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		},
	})
	eng := engine.New(engine.Options{
		SessionID: "s-phase-hist",
		WorkDir:   t.TempDir(),
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
		},
		InitialAgent:    "plan",
		InitialProvider: "echo",
		InitialModel:    "echo",
		Registry:        tool.NewRegistry(),
		Select: func(string) (provider.Provider, string, error) {
			return prov, "echo", nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "plan something"}
	req := waitStreamRequest(t, eng, prov)
	waitTurnCompleted(t, eng)

	if !strings.Contains(req.System, "Plan mode (read-only)") {
		t.Fatalf("plan overlay missing from system:\n%s", req.System)
	}
	for _, msg := range eng.Messages() {
		blob := msg.Text
		if msg.ToolResult != nil {
			blob += msg.ToolResult.Output
		}
		if strings.Contains(blob, "Plan mode (read-only)") {
			t.Fatalf("phase/plan overlay leaked into history role=%s: %q", msg.Role, blob)
		}
	}
}

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		in, banned string
	}{
		{"key sk-ant-api03-ABCDEFGHIJKLMNOP secret", "sk-ant-api03-ABCDEFGHIJKLMNOP"},
		{"Authorization: Bearer tok_abc1234567890", "tok_abc1234567890"},
		{"OPENAI_API_KEY=sk-proj-hello-world-99", "sk-proj-hello-world-99"},
		{"api_key: super-secret-value-here", "super-secret-value-here"},
		{"ghp_abcdefghijklmnopqrstuvwx", "ghp_abcdefghijklmnopqrstuvwx"},
	}
	for _, tt := range cases {
		got := engine.RedactSecrets(tt.in)
		if strings.Contains(got, tt.banned) {
			t.Errorf("RedactSecrets(%q) still contains %q → %q", tt.in, tt.banned, got)
		}
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("RedactSecrets(%q) = %q, want [REDACTED]", tt.in, got)
		}
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
	return waitStreamRequest(t, eng, prov).System
}

func waitStreamRequest(t *testing.T, eng *engine.Engine, prov *scriptedProvider) provider.Request {
	t.Helper()
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
	return req
}

func waitTurnCompleted(t *testing.T, eng *engine.Engine) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-eng.Events():
			if _, ok := ev.(protocol.TurnCompleted); ok {
				return
			}
			if err, ok := ev.(protocol.EngineError); ok {
				t.Fatalf("engine error: %s", err.Message)
			}
		case <-deadline:
			t.Fatal("timeout waiting for TurnCompleted")
		}
	}
}

func waitAgentSelected(t *testing.T, eng *engine.Engine, name string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-eng.Events():
			if a, ok := ev.(protocol.AgentSelected); ok && a.Name == name {
				return
			}
			if err, ok := ev.(protocol.EngineError); ok {
				t.Fatalf("engine error: %s", err.Message)
			}
		case <-deadline:
			t.Fatalf("timeout waiting for AgentSelected %q", name)
		}
	}
}

func waitEffectivePrompt(t *testing.T, eng *engine.Engine) protocol.EffectivePrompt {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-eng.Events():
			if p, ok := ev.(protocol.EffectivePrompt); ok {
				return p
			}
			if err, ok := ev.(protocol.EngineError); ok {
				t.Fatalf("engine error: %s", err.Message)
			}
		case <-deadline:
			t.Fatal("timeout waiting for EffectivePrompt")
		}
	}
}
