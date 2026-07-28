package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/harness"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

type testHarness struct {
	name string
	run  func(context.Context, harness.Request) (harness.Result, error)
}

func (h *testHarness) Name() string { return h.name }

func (h *testHarness) Run(ctx context.Context, req harness.Request) (harness.Result, error) {
	return h.run(ctx, req)
}

func TestHarnessFailureEmitsOneErrorThenCompletion(t *testing.T) {
	registry := harness.NewRegistry()
	registry.Register(&testHarness{name: "crash", run: func(context.Context, harness.Request) (harness.Result, error) {
		return harness.Result{}, errors.New("harness crashed")
	}})
	eng := newHarnessEngine(t, registry, &scriptedProvider{}, "crash")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "hello"}
	completed, events := collectThroughTurnCompleted(t, eng.Events())
	if completed.StopReason != "error" {
		t.Fatalf("StopReason = %q", completed.StopReason)
	}
	errorsSeen := 0
	for i, ev := range events {
		if errEvent, ok := ev.(protocol.EngineError); ok {
			errorsSeen++
			if errEvent.Message != "harness crashed" || i != len(events)-2 {
				t.Fatalf("EngineError = %#v at %d; events=%#v", errEvent, i, events)
			}
		}
	}
	if errorsSeen != 1 {
		t.Fatalf("EngineError count = %d", errorsSeen)
	}
}

func TestHarnessFinalTextPersistedWithoutSpeculativeMessages(t *testing.T) {
	prov := newScriptedProvider(completedStep("candidate"))
	var mu sync.Mutex
	var starts [][]provider.Message
	registry := harness.NewRegistry()
	registry.Register(&testHarness{name: "choose", run: func(ctx context.Context, req harness.Request) (harness.Result, error) {
		mu.Lock()
		starts = append(starts, append([]provider.Message(nil), req.Request.Messages...))
		turn := len(starts)
		mu.Unlock()
		if turn == 1 {
			stream, err := req.Provider(ctx, req.Request)
			if err != nil {
				return harness.Result{}, err
			}
			for range stream {
			}
			return harness.Result{Text: "selected", StopReason: "end_turn"}, nil
		}
		return harness.Result{Text: "again", StopReason: "end_turn"}, nil
	}})
	eng := newHarnessEngine(t, registry, prov, "choose")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "first"}
	_, firstEvents := collectThroughTurnCompleted(t, eng.Events())
	if countText(firstEvents, "selected") != 1 {
		t.Fatalf("selected final text events = %#v", firstEvents)
	}
	eng.Ops() <- protocol.UserInput{Text: "second"}
	_ = waitForTurnCompleted(t, eng.Events())
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 2 || len(starts[1]) != 3 {
		t.Fatalf("second turn messages = %#v", starts)
	}
	if starts[1][1].Role != provider.RoleAssistant || starts[1][1].Text != "selected" {
		t.Fatalf("persisted assistant = %#v", starts[1][1])
	}
	for _, message := range starts[1] {
		if message.Text == "candidate" {
			t.Fatalf("speculative candidate committed: %#v", starts[1])
		}
	}
}

func TestHarnessToolExecuteUsesPermissionPipelineAndPreservesID(t *testing.T) {
	executed := make(chan string, 1)
	registry := harness.NewRegistry()
	registry.Register(&testHarness{name: "tools", run: func(ctx context.Context, req harness.Request) (harness.Result, error) {
		message := req.Execute(ctx, toolCall("external-7", "permission"))
		if message.ToolResult == nil || message.ToolResult.CallID != "external-7" {
			return harness.Result{}, errors.New("tool result did not preserve call ID")
		}
		return harness.Result{Text: message.ToolResult.Output, StopReason: "end_turn"}, nil
	}})
	eng := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return &scriptedProvider{}, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "custom",
		Agents:          []engine.Agent{{Name: "custom", Harness: "tools"}},
		HarnessRegistry: registry,
		Registry:        tool.NewRegistry(&permissionTool{executed: executed}),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "run"}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.TurnCompleted:
				if got := <-executed; got != "external-7" {
					t.Fatalf("executed ID = %q", got)
				}
				return
			case protocol.EngineError:
				t.Fatalf("EngineError: %s", ev.Message)
			}
		case <-deadline:
			t.Fatal("timed out waiting for harness tool completion")
		}
	}
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

func newHarnessEngine(t *testing.T, registry *harness.Registry, prov provider.Provider, harnessName string) *engine.Engine {
	t.Helper()
	return engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "custom",
		Agents:          []engine.Agent{{Name: "custom", Harness: harnessName}},
		HarnessRegistry: registry,
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
}
