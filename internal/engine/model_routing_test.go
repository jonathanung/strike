package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// multiProviderSelect returns a SelectFunc that hands out distinct scripted
// providers with stable default model names. Defaults are intentionally
// provider-specific so tests can assert routing without sharing ids.
func multiProviderSelect(providers map[string]*scriptedProvider, defaults map[string]string) engine.SelectFunc {
	return func(name string) (provider.Provider, string, error) {
		p, ok := providers[name]
		if !ok {
			return nil, "", fmt.Errorf("unknown provider %q", name)
		}
		def := defaults[name]
		if def == "" {
			def = name + "-default"
		}
		return p, def, nil
	}
}

func taskToolCallWithAgent(id, prompt, agent string) provider.ToolCall {
	args, _ := json.Marshal(map[string]any{
		"prompt": prompt,
		"agent":  agent,
	})
	return provider.ToolCall{ID: id, Name: "task", Args: args}
}

// TestInitialModelForeignPrefixUsesProviderDefault: startup with
// InitialProvider "xai" and InitialModel "openai/gpt-5.6-sol" must not keep
// the foreign prefixed id. resolveSelectModel discards the foreign prefix and
// adopts the xai Select default. Bare foreign ids (e.g. "gpt-5.6-sol" with no
// prefix) are left as-is — without a catalog we cannot know they are foreign.
func TestInitialModelForeignPrefixUsesProviderDefault(t *testing.T) {
	xaiProv := newScriptedProvider(completedStep("xai ok"))
	providers := map[string]*scriptedProvider{
		"xai": xaiProv,
	}
	defaults := map[string]string{
		"xai": "xai-default",
	}

	eng := engine.New(engine.Options{
		Select:          multiProviderSelect(providers, defaults),
		InitialProvider: "xai",
		InitialModel:    "openai/gpt-5.6-sol",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	selected := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.ModelSelected)
		return ok
	}).(protocol.ModelSelected)
	if selected.Provider != "xai" {
		t.Fatalf("ModelSelected.Provider = %q, want xai", selected.Provider)
	}
	if selected.Model != "xai-default" {
		t.Fatalf("ModelSelected.Model = %q, want xai-default (foreign prefix must not stick on startup)", selected.Model)
	}
	if selected.Model == "openai/gpt-5.6-sol" || strings.HasPrefix(selected.Model, "openai/") {
		t.Fatalf("startup kept foreign prefixed id: %+v", selected)
	}

	eng.Ops() <- protocol.UserInput{Text: "hello"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})

	req := receiveRequest(t, xaiProv.requests)
	if req.Model != "xai-default" {
		t.Errorf("Stream Model = %q, want xai-default (not foreign prefixed id)", req.Model)
	}
	if req.Model == "openai/gpt-5.6-sol" {
		t.Errorf("Stream still carries foreign prefixed id %q", req.Model)
	}
}

// TestProviderSwitchEmptyModelResetsToDefault pins the provider-switch path:
// after openai + explicit model, SelectModel{xai, Model:""} must adopt the
// xai Select default — not retain the previous provider's model id.
func TestProviderSwitchEmptyModelResetsToDefault(t *testing.T) {
	openaiProv := newScriptedProvider(completedStep("openai ok"))
	xaiProv := newScriptedProvider(completedStep("xai ok"))
	providers := map[string]*scriptedProvider{
		"openai": openaiProv,
		"xai":    xaiProv,
	}
	defaults := map[string]string{
		"openai": "openai-default",
		"xai":    "xai-default",
	}

	eng := engine.New(engine.Options{
		Select:          multiProviderSelect(providers, defaults),
		InitialProvider: "openai",
		InitialModel:    "gpt-5.6-sol",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Startup ModelSelected: openai / gpt-5.6-sol
	_ = waitForEvent(t, eng, func(ev protocol.Event) bool {
		ms, ok := ev.(protocol.ModelSelected)
		return ok && ms.Provider == "openai" && ms.Model == "gpt-5.6-sol"
	})

	// Switch to xai with empty model → must use Select default, not gpt-5.6-sol.
	eng.Ops() <- protocol.SelectModel{Provider: "xai", Model: ""}
	switched := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.ModelSelected)
		return ok
	}).(protocol.ModelSelected)
	if switched.Provider != "xai" {
		t.Fatalf("ModelSelected.Provider = %q, want xai", switched.Provider)
	}
	if switched.Model != "xai-default" {
		t.Fatalf("ModelSelected.Model = %q, want xai-default (empty Model must reset; must not keep gpt-5.6-sol)", switched.Model)
	}

	eng.Ops() <- protocol.UserInput{Text: "hello xai"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})

	req := receiveRequest(t, xaiProv.requests)
	if req.Model != "xai-default" {
		t.Errorf("xai Stream Model = %q, want xai-default (not retained openai model)", req.Model)
	}
	if req.Model == "gpt-5.6-sol" || strings.Contains(req.Model, "openai") {
		t.Errorf("xai Stream Model = %q carries foreign openai id", req.Model)
	}
	// openai must not have been called for this turn.
	select {
	case req := <-openaiProv.requests:
		t.Errorf("openai received unexpected request after switch: model=%q", req.Model)
	default:
	}
}

// TestSelectModelStripsMatchingPrefixUsesDefaultForForeignPrefix encodes the
// preferred product behavior for provider/model ids:
//
//   - model "provider/id" where provider matches op.Provider → bare "id"
//   - model "other/id" where other ≠ op.Provider → provider default (do not
//     send the foreign id on this provider)
//   - bare model ids still work as today
//
// (Option C / preferred routing — implementer matches this contract.)
func TestSelectModelStripsMatchingPrefixUsesDefaultForForeignPrefix(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		model       string
		wantModel   string
		wantOnProv  string // which scripted provider must receive the stream
		desc        string
	}{
		{
			name:       "matching_prefix_stripped_to_bare_id",
			provider:   "xai",
			model:      "xai/grok-3",
			wantModel:  "grok-3",
			wantOnProv: "xai",
			desc:       "xai/grok-3 on xai → bare grok-3",
		},
		{
			name:       "foreign_prefix_uses_provider_default",
			provider:   "xai",
			model:      "openai/gpt-5.6-sol",
			wantModel:  "xai-default",
			wantOnProv: "xai",
			desc:       "openai/gpt-5.6-sol on xai → xai default, not foreign id",
		},
		{
			name:       "bare_model_id_unchanged",
			provider:   "xai",
			model:      "grok-4",
			wantModel:  "grok-4",
			wantOnProv: "xai",
			desc:       "bare ids still work",
		},
		{
			name:       "matching_openai_prefix_stripped",
			provider:   "openai",
			model:      "openai/gpt-5.6-sol",
			wantModel:  "gpt-5.6-sol",
			wantOnProv: "openai",
			desc:       "openai/gpt-5.6-sol on openai → bare gpt-5.6-sol",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			openaiProv := newScriptedProvider(completedStep("ok"))
			xaiProv := newScriptedProvider(completedStep("ok"))
			providers := map[string]*scriptedProvider{
				"openai": openaiProv,
				"xai":    xaiProv,
			}
			defaults := map[string]string{
				"openai": "openai-default",
				"xai":    "xai-default",
			}

			eng := engine.New(engine.Options{
				Select:   multiProviderSelect(providers, defaults),
				Registry: tool.NewRegistry(),
				WorkDir:  t.TempDir(),
				Rules:    []permission.Ruleset{permission.Defaults()},
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go eng.Run(ctx)

			eng.Ops() <- protocol.SelectModel{Provider: tt.provider, Model: tt.model}
			selected := waitForEvent(t, eng, func(ev protocol.Event) bool {
				_, ok := ev.(protocol.ModelSelected)
				return ok
			}).(protocol.ModelSelected)

			if selected.Provider != tt.provider {
				t.Fatalf("ModelSelected.Provider = %q, want %q (%s)", selected.Provider, tt.provider, tt.desc)
			}
			if selected.Model != tt.wantModel {
				t.Fatalf("ModelSelected.Model = %q, want %q (%s)", selected.Model, tt.wantModel, tt.desc)
			}
			// Never leave a foreign provider/ prefix on the active model string.
			if strings.Contains(selected.Model, "/") {
				other, _, ok := strings.Cut(selected.Model, "/")
				if ok && other != "" && other != selected.Provider {
					t.Fatalf("ModelSelected.Model = %q still has foreign provider prefix", selected.Model)
				}
			}

			eng.Ops() <- protocol.UserInput{Text: "ping"}
			waitForEvent(t, eng, func(ev protocol.Event) bool {
				_, ok := ev.(protocol.TurnCompleted)
				return ok
			})

			var req provider.Request
			switch tt.wantOnProv {
			case "xai":
				req = receiveRequest(t, xaiProv.requests)
			case "openai":
				req = receiveRequest(t, openaiProv.requests)
			default:
				t.Fatalf("bad wantOnProv %q", tt.wantOnProv)
			}
			if req.Model != tt.wantModel {
				t.Errorf("Stream Model = %q, want %q (%s)", req.Model, tt.wantModel, tt.desc)
			}
			if req.Model == "openai/gpt-5.6-sol" {
				t.Errorf("Stream still carries foreign prefixed id %q", req.Model)
			}
		})
	}
}

// TestAgentModelOnlyPrefixedPinDoesNotPoisonProvider: an agent pin of
// Model "openai/gpt-5.6-luna" with no Provider field must not set e.model to
// that foreign string while the engine stays on xai.
//
// Preferred behavior: treat "provider/id" as an implicit provider pin + bare
// model pin — Select("openai") with model "gpt-5.6-luna".
func TestAgentModelOnlyPrefixedPinDoesNotPoisonProvider(t *testing.T) {
	openaiProv := newScriptedProvider(completedStep("openai agent ok"))
	xaiProv := newScriptedProvider(completedStep("xai ok"))
	providers := map[string]*scriptedProvider{
		"openai": openaiProv,
		"xai":    xaiProv,
	}
	defaults := map[string]string{
		"openai": "openai-default",
		"xai":    "xai-default",
	}

	eng := engine.New(engine.Options{
		Select:          multiProviderSelect(providers, defaults),
		InitialProvider: "xai",
		Registry: tool.NewRegistry(),
		WorkDir:  t.TempDir(),
		Rules:    []permission.Ruleset{permission.Defaults()},
		Agents: []engine.Agent{
			{Name: "build", Description: "default"},
			{Name: "explorer", Model: "openai/gpt-5.6-luna"}, // model-only pin, prefixed
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	_ = waitForEvent(t, eng, func(ev protocol.Event) bool {
		ms, ok := ev.(protocol.ModelSelected)
		return ok && ms.Provider == "xai"
	})

	eng.Ops() <- protocol.SelectAgent{Name: "explorer"}
	// Drain until we see AgentSelected, then the routing ModelSelected that
	// must follow for a prefixed model-only pin.
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		a, ok := ev.(protocol.AgentSelected)
		return ok && a.Name == "explorer"
	})

	// Collect ModelSelected events after agent select (may already be buffered).
	deadline := time.After(2 * time.Second)
	var afterAgent []protocol.ModelSelected
collect:
	for {
		select {
		case ev := <-eng.Events():
			if ms, ok := ev.(protocol.ModelSelected); ok {
				afterAgent = append(afterAgent, ms)
				// Preferred: switched to openai with bare id.
				if ms.Provider == "openai" && ms.Model == "gpt-5.6-luna" {
					break collect
				}
				// Bug shape: stayed on xai with foreign prefixed id.
				if ms.Provider == "xai" && ms.Model == "openai/gpt-5.6-luna" {
					t.Fatalf("agent model-only pin poisoned xai: ModelSelected=%+v", ms)
				}
			}
			if err, ok := ev.(protocol.EngineError); ok {
				t.Fatalf("engine error after SelectAgent: %s", err.Message)
			}
		case <-deadline:
			break collect
		}
	}

	eng.Ops() <- protocol.UserInput{Text: "explore"}
	var events []protocol.Event
	turnDeadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-eng.Events():
			events = append(events, ev)
			if _, ok := ev.(protocol.TurnCompleted); ok {
				goto doneTurn
			}
			if err, ok := ev.(protocol.EngineError); ok {
				t.Fatalf("engine error on turn: %s", err.Message)
			}
		case <-turnDeadline:
			t.Fatalf("timed out waiting for TurnCompleted; events=%v modelSelected=%v", summarizeEvents(events), afterAgent)
		}
	}
doneTurn:

	// Must not stream the foreign prefixed id on xai.
	select {
	case req := <-xaiProv.requests:
		if req.Model == "openai/gpt-5.6-luna" || strings.HasPrefix(req.Model, "openai/") {
			t.Errorf("xai Stream Model = %q; agent pin must not poison current provider", req.Model)
		}
	default:
	}

	// Preferred: openai receives bare model id.
	req := receiveRequest(t, openaiProv.requests)
	if req.Model != "gpt-5.6-luna" {
		t.Errorf("openai Stream Model = %q, want bare gpt-5.6-luna (prefixed pin → provider+model)", req.Model)
	}
	if req.Model == "openai/gpt-5.6-luna" {
		t.Errorf("openai Stream still has provider prefix in model id: %q", req.Model)
	}
}

// TestAgentExplicitProviderAndModelPinsStillWork: Agent{Provider, Model}
// without a prefix continues to Select that provider and use the bare model.
func TestAgentExplicitProviderAndModelPinsStillWork(t *testing.T) {
	openaiProv := newScriptedProvider(completedStep("pinned ok"))
	xaiProv := newScriptedProvider(completedStep("xai unused"))
	providers := map[string]*scriptedProvider{
		"openai": openaiProv,
		"xai":    xaiProv,
	}
	defaults := map[string]string{
		"openai": "openai-default",
		"xai":    "xai-default",
	}

	eng := engine.New(engine.Options{
		Select:          multiProviderSelect(providers, defaults),
		InitialProvider: "xai",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "coder", Provider: "openai", Model: "gpt-5.6-sol"},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	_ = waitForEvent(t, eng, func(ev protocol.Event) bool {
		ms, ok := ev.(protocol.ModelSelected)
		return ok && ms.Provider == "xai"
	})

	eng.Ops() <- protocol.SelectAgent{Name: "coder"}
	selected := waitForEvent(t, eng, func(ev protocol.Event) bool {
		ms, ok := ev.(protocol.ModelSelected)
		return ok && ms.Provider == "openai"
	}).(protocol.ModelSelected)
	if selected.Model != "gpt-5.6-sol" {
		t.Fatalf("ModelSelected.Model = %q, want gpt-5.6-sol", selected.Model)
	}

	eng.Ops() <- protocol.UserInput{Text: "code"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})

	req := receiveRequest(t, openaiProv.requests)
	if req.Model != "gpt-5.6-sol" {
		t.Errorf("openai Stream Model = %q, want gpt-5.6-sol", req.Model)
	}
}

// TestTaskChildPrefixedAgentModelPinDoesNotStreamForeignIdOnParentProvider:
// parent on xai (default model) spawns a child whose agent has only
// Model: "openai/gpt-5.6-luna". The child must not Stream that foreign id on
// the inherited xai provider.
//
// Preferred: child treats the pin as openai + bare gpt-5.6-luna.
func TestTaskChildPrefixedAgentModelPinDoesNotStreamForeignIdOnParentProvider(t *testing.T) {
	const (
		taskPrompt = "child explore work"
		foreignID  = "openai/gpt-5.6-luna"
		bareID     = "gpt-5.6-luna"
	)

	taskCall := taskToolCallWithAgent("task-route", taskPrompt, "explorer")
	// xai: parent tool-use, optional buggy child stream (same inherited
	// provider), parent final. openai: preferred child stream after pin parse.
	xaiProv := newScriptedProvider(
		toolCallStep(taskCall),
		completedStep("buggy-child-or-parent-followup"),
		completedStep("parent after child"),
	)
	openaiProv := newScriptedProvider(
		completedStep("child finished on openai"),
	)
	providers := map[string]*scriptedProvider{
		"openai": openaiProv,
		"xai":    xaiProv,
	}
	defaults := map[string]string{
		"openai": "openai-default",
		"xai":    "xai-default",
	}

	eng := engine.New(engine.Options{
		SessionID:       "parent-route",
		Select:          multiProviderSelect(providers, defaults),
		InitialProvider: "xai",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "explorer", Model: foreignID}, // model-only prefixed pin
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	_ = waitForEvent(t, eng, func(ev protocol.Event) bool {
		ms, ok := ev.(protocol.ModelSelected)
		return ok && ms.Provider == "xai" && ms.Model == "xai-default"
	})

	eng.Ops() <- protocol.UserInput{Text: "spawn explorer child"}
	events := drainAndReply(t, eng, 10*time.Second)

	if n := countEvents[protocol.ChildStarted](events); n != 1 {
		t.Fatalf("ChildStarted = %d, want 1; events=%v", n, summarizeEvents(events))
	}

	// Drain all xai requests. Child currently inherits the parent provider
	// instance, so a poisoned pin shows up as a Stream on xai with foreignID.
	var xaiReqs []provider.Request
drainXai:
	for {
		select {
		case req := <-xaiProv.requests:
			xaiReqs = append(xaiReqs, req)
		case <-time.After(100 * time.Millisecond):
			break drainXai
		}
	}
	if len(xaiReqs) < 1 {
		t.Fatal("xai received no requests")
	}
	var sawForeignOnXai bool
	for i, req := range xaiReqs {
		if req.Model == foreignID || strings.HasPrefix(req.Model, "openai/") {
			sawForeignOnXai = true
			t.Errorf("xai request[%d] Model = %q; child must not Stream foreign agent pin on xai", i, req.Model)
		}
	}
	if sawForeignOnXai {
		// Preferred path never ran; still try openai to document the contract.
	}

	// Preferred: child Select("openai") + bare model, not foreign id on xai.
	select {
	case childReq := <-openaiProv.requests:
		if childReq.Model != bareID {
			t.Errorf("child openai Stream Model = %q, want bare %q", childReq.Model, bareID)
		}
		if len(childReq.Messages) < 1 || childReq.Messages[0].Text != taskPrompt {
			t.Errorf("child request messages = %#v, want user prompt %q", childReq.Messages, taskPrompt)
		}
	case <-time.After(500 * time.Millisecond):
		t.Errorf("openai received no child Stream; want child to honor prefixed pin as openai/%s", bareID)
	}
}
