package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	exampleharnesses "github.com/jonathanung/strike-cli/examples/harnesses"
	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/harness"
	"github.com/jonathanung/strike-cli/internal/harness/external"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestRootAgentHarnessIsNotInvoked(t *testing.T) {
	invoked := false
	registry := harness.NewRegistry()
	registry.Register("child-only", func(harness.Input, harness.Provider, harness.Emit) (harness.Result, error) {
		invoked = true
		return harness.Result{Text: "wrong"}, nil
	})
	eng := engine.New(engine.Options{
		Select: func(string) (provider.Provider, string, error) {
			return newScriptedProvider(completedStep("built-in")), "model", nil
		},
		InitialProvider: "scripted",
		InitialAgent:    "custom",
		Agents:          []engine.Agent{{Name: "custom", Harness: "child-only"}},
		HarnessRegistry: registry,
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "root"}
	_, events := collectThroughTurnCompleted(t, eng.Events())
	if invoked {
		t.Fatal("root agent invoked a task-only harness")
	}
	if countText(events, "built-in") != 1 {
		t.Fatalf("root did not use built-in provider: %#v", events)
	}
}

func TestTaskChildInvokesAgentHarness(t *testing.T) {
	const prompt = "search independently"
	called := make(chan harness.Input, 1)
	registry := harness.NewRegistry()
	registry.Register("search-fn", func(input harness.Input, p harness.Provider, emit harness.Emit) (harness.Result, error) {
		called <- input
		response, err := p.Call(input.Request)
		if err != nil {
			return harness.Result{}, err
		}
		if response.Text != "complete response" || response.StopReason != "end_turn" {
			return harness.Result{}, errors.New("harness received an incomplete provider response")
		}
		emit(json.RawMessage(`{"kind":"searching"}`))
		return harness.Result{Text: "function result", StopReason: "complete"}, nil
	})
	taskCall := taskToolCallWithAgent("task-harness", prompt, "search")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("complete response")
			s.match = matchUserText(prompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent finished")
			s.match = matchToolResult("task-harness")
			return s
		}(),
		childCompletedNudgeStep("parent saw completion"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "parent",
		Agents: []engine.Agent{
			{Name: "parent"},
			{Name: "search", Harness: "search-fn"},
		},
		HarnessRegistry: registry,
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "delegate"}
	events := drainAndReply(t, eng, 10*time.Second)

	select {
	case input := <-called:
		if len(input.Request.Messages) != 1 || input.Request.Messages[0].Text != prompt {
			t.Fatalf("harness request messages = %#v", input.Request.Messages)
		}
	default:
		t.Fatal("task child did not invoke its harness")
	}
	for _, ev := range events {
		if completed, ok := ev.(protocol.ChildCompleted); ok {
			if completed.Status != protocol.ChildStatusCompleted || completed.Summary != "function result" {
				t.Fatalf("ChildCompleted = %#v", completed)
			}
			return
		}
	}
	t.Fatalf("missing ChildCompleted; events=%#v", events)
}

func TestTaskChildHarnessEndToEnd(t *testing.T) {
	tests := []struct {
		name string
		fn   func(*testing.T) harness.Func
	}{
		{name: "go", fn: func(*testing.T) harness.Func { return exampleharnesses.ChooseBest }},
		{name: "go-subprocess", fn: func(t *testing.T) harness.Func { return exampleExternalHarness(t, "choose-best-go-process") }},
		{name: "javascript", fn: func(t *testing.T) harness.Func { return exampleExternalHarness(t, "choose-best-js") }},
		{name: "lean", fn: func(t *testing.T) harness.Func { return exampleExternalHarness(t, "choose-best-lean") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testTaskChildHarnessEndToEnd(t, tt.fn(t))
		})
	}
}

func testTaskChildHarnessEndToEnd(t *testing.T, fn harness.Func) {
	t.Helper()
	registry := harness.NewRegistry()
	registry.Register("choose-best", fn)

	const prompt = "compare candidate answers"
	taskCall := taskToolCallWithAgent("task-external", prompt, "search")
	providerSteps := []streamStep{toolCallStep(taskCall)}
	for _, candidate := range []string{"first", "the longest candidate", "third choice"} {
		step := completedStep(candidate)
		step.match = matchUserText(prompt)
		providerSteps = append(providerSteps, step)
	}
	parentStep := completedStep("parent finished")
	parentStep.match = matchToolResult("task-external")
	providerSteps = append(providerSteps, parentStep, childCompletedNudgeStep("parent saw completion"))
	prov := newScriptedProvider(providerSteps...)
	progress := make(chan protocol.HarnessProgress, 3)

	eng := engine.New(engine.Options{
		SessionID:       "parent",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "parent",
		Agents: []engine.Agent{
			{Name: "parent"},
			{Name: "search", Harness: "choose-best"},
		},
		HarnessRegistry: registry,
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		AppendChildEvent: func(_ string, event protocol.Event) error {
			if event, ok := event.(protocol.HarnessProgress); ok {
				progress <- event
			}
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "delegate"}
	events := drainAndReply(t, eng, 10*time.Second)

	completed := false
	for _, event := range events {
		if event, ok := event.(protocol.ChildCompleted); ok {
			if event.Status != protocol.ChildStatusCompleted || event.Summary != "the longest candidate" {
				t.Fatalf("ChildCompleted = %#v", event)
			}
			completed = true
		}
	}
	if !completed {
		t.Fatalf("missing ChildCompleted; events=%#v", events)
	}
	if len(progress) != 3 {
		t.Fatalf("HarnessProgress events = %d, want 3", len(progress))
	}
	for len(progress) > 0 {
		if event := <-progress; event.Name != "choose-best" {
			t.Fatalf("HarnessProgress.Name = %q", event.Name)
		}
	}
	if calls := prov.callCount(); calls != 6 {
		t.Fatalf("provider calls = %d, want 6", calls)
	}
}

func exampleExternalHarness(t *testing.T, name string) harness.Func {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "examples", "harnesses"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Harnesses map[string]external.Config `json:"harnesses"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	cfg, ok := fixture.Harnesses[name]
	if !ok {
		t.Fatalf("example config does not define harness %q", name)
	}
	command, err := exec.LookPath(cfg.Command)
	if err != nil && cfg.Command == "go" {
		goCommand := "go"
		if runtime.GOOS == "windows" {
			goCommand += ".exe"
		}
		command = filepath.Join(runtime.GOROOT(), "bin", goCommand)
		if _, statErr := os.Stat(command); statErr == nil {
			err = nil
		}
	}
	if err != nil {
		t.Skipf("%s is required to run the %s harness integration test", cfg.Command, name)
	}
	cfg.Command = command
	for i, arg := range cfg.Args {
		const prefix = "./examples/harnesses"
		if strings.HasPrefix(filepath.ToSlash(arg), prefix) {
			relative := strings.TrimPrefix(filepath.ToSlash(arg), prefix)
			cfg.Args[i] = filepath.Join(dir, strings.TrimPrefix(relative, "/"))
		}
	}
	adapter, err := external.Command(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fn, err := external.New(name, adapter)
	if err != nil {
		t.Fatal(err)
	}
	return fn
}

func countText(events []protocol.Event, text string) int {
	count := 0
	for _, ev := range events {
		if delta, ok := ev.(protocol.TextDelta); ok && delta.Text == text {
			count++
		}
	}
	return count
}
