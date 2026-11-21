package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

// scopeTool records whether it ran.
type scopeTool struct {
	name string
	mu   sync.Mutex
	ran  bool
}

func (t *scopeTool) Name() string            { return t.name }
func (t *scopeTool) Description() string     { return "scope test tool " + t.name }
func (t *scopeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *scopeTool) Execute(context.Context, json.RawMessage, *tool.Context) (tool.Result, error) {
	t.mu.Lock()
	t.ran = true
	t.mu.Unlock()
	return tool.Result{Output: "ran"}, nil
}

func (t *scopeTool) didRun() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ran
}

// sessionSkillTool is a skill tool whose description depends on the session.
type sessionSkillTool struct{ scopeTool }

func (t *sessionSkillTool) Description() string { return "Load a skill.\n\nAvailable skills: base" }
func (t *sessionSkillTool) DescriptionFor(root string) string {
	return "Load a skill.\n\nAvailable skills: base, pack-" + root
}

// Powers (#1943): Options.ToolVisible scopes the shared registry per root
// session. A hidden tool is absent from the provider tools and refused at
// dispatch as unknown; a SessionDescriber's description is per session.
func TestToolVisibleScopesRegistryPerSession(t *testing.T) {
	hidden := &scopeTool{name: "mcp_pack_ping"}
	shown := &scopeTool{name: "visible_tool"}
	skill := &sessionSkillTool{scopeTool{name: "skill"}}
	prov := newScriptedProvider(
		toolCallStep(toolCall("c1", hidden.name)),
		completedStep("done"),
	)
	var mu sync.Mutex
	var roots []string
	eng := engine.New(engine.Options{
		SessionID:       "root-1",
		Select:          func(string) (provider.Provider, string, error) { return prov, "test-model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(hidden, shown, skill),
		ToolVisible: func(root, name string) bool {
			mu.Lock()
			roots = append(roots, root)
			mu.Unlock()
			return name != hidden.name
		},
		WorkDir:             t.TempDir(),
		Rules:               []permission.Ruleset{permission.Defaults()},
		SandboxAllowDegrade: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "ping"}

	var end protocol.ToolCallEnd
	var sawEnd bool
	deadline := time.After(10 * time.Second)
	for done := false; !done; {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for turn")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.ToolCallEnd:
				end, sawEnd = ev, true
			case protocol.TurnCompleted:
				done = true
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
	if !sawEnd || !end.IsError || !strings.Contains(end.Output, "unknown tool") {
		t.Fatalf("hidden tool call end = %+v (saw=%v), want unknown tool error", end, sawEnd)
	}
	if hidden.didRun() {
		t.Fatal("hidden tool executed")
	}

	req := <-prov.requests
	names := map[string]string{}
	for _, s := range req.Tools {
		names[s.Name] = s.Description
	}
	if _, ok := names[hidden.name]; ok {
		t.Errorf("hidden tool sent to provider: %v", names)
	}
	if _, ok := names[shown.name]; !ok {
		t.Errorf("visible tool missing from provider tools: %v", names)
	}
	if d := names["skill"]; !strings.Contains(d, "pack-root-1") {
		t.Errorf("skill description = %q, want the session's list", d)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(roots) == 0 {
		t.Fatal("ToolVisible never called")
	}
	for _, r := range roots {
		if r != "root-1" {
			t.Fatalf("ToolVisible root = %q, want root-1", r)
		}
	}
}
