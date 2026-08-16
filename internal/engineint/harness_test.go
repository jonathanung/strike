package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	exampleharnesses "github.com/jonathanung/strike-cli/examples/harnesses"
	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/fn"
	"github.com/jonathanung/strike-cli/harness/fn/external"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/internal/enginebind"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestRootAgentHarnessIsNotInvoked(t *testing.T) {
	invoked := false
	registry := fn.NewRegistry()
	registry.Register("child-only", func(fn.Input, fn.Provider, fn.Emit) (fn.Result, error) {
		invoked = true
		return fn.Result{Text: "wrong"}, nil
	})
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
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
	called := make(chan fn.Input, 1)
	registry := fn.NewRegistry()
	registry.Register("search-fn", func(input fn.Input, p fn.Provider, emit fn.Emit) (fn.Result, error) {
		called <- input
		response, err := p.Call(input.Request)
		if err != nil {
			return fn.Result{}, err
		}
		if response.Text != "complete response" || response.StopReason != "end_turn" {
			return fn.Result{}, errors.New("harness received an incomplete provider response")
		}
		emit(json.RawMessage(`{"kind":"searching"}`))
		return fn.Result{Text: "function result", StopReason: "complete"}, nil
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
		BuildDiagnostic: enginebind.Diagnostic(),
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

func TestTaskChildHarnessRejectsToolCalls(t *testing.T) {
	registry := fn.NewRegistry()
	registry.Register("invalid", func(fn.Input, fn.Provider, fn.Emit) (fn.Result, error) {
		return fn.Result{
			Calls:      []provider.ToolCall{{ID: "call-1", Name: "read"}},
			StopReason: "tool_use",
		}, nil
	})
	prov := newScriptedProvider(toolCallStep(taskToolCallWithAgent("task-invalid", "delegate", "worker")))
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "parent",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "parent",
		Agents: []engine.Agent{
			{Name: "parent"},
			{Name: "worker", Harness: "invalid"},
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

	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-eng.Events():
			if completed, ok := event.(protocol.ChildCompleted); ok {
				if completed.Status == protocol.ChildStatusCompleted {
					t.Fatalf("ChildCompleted = %#v, want failure", completed)
				}
				if !strings.Contains(completed.Summary, "cannot execute") {
					t.Fatalf("ChildCompleted summary = %q", completed.Summary)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for failed ChildCompleted")
		}
	}
}

func TestTaskChildHarnessEndToEnd(t *testing.T) {
	tests := []struct {
		name string
		fn   func(*testing.T) fn.Func
	}{
		{name: "go", fn: func(*testing.T) fn.Func { return exampleharnesses.ChooseBest }},
		{name: "go-subprocess", fn: func(t *testing.T) fn.Func { return exampleExternalHarness(t, "choose-best-go-process") }},
		{name: "javascript", fn: func(t *testing.T) fn.Func { return exampleExternalHarness(t, "choose-best-js") }},
		{name: "lean", fn: func(t *testing.T) fn.Func { return exampleExternalHarness(t, "choose-best-lean") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testTaskChildHarnessEndToEnd(t, tt.fn(t))
		})
	}
}

func testTaskChildHarnessEndToEnd(t *testing.T, run fn.Func) {
	t.Helper()
	registry := fn.NewRegistry()
	registry.Register("choose-best", run)

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
		BuildDiagnostic: enginebind.Diagnostic(),
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
	// 5 = parent tool + 3 harness candidates + parent finish with mid-turn
	// child.completed inject (no idle nudge stream). 6 = same plus idle
	// auto-nudge after the parent turn. Both are valid; race/CPU timing
	// (e.g. tool-output redaction) picks the path.
	if calls := prov.callCount(); calls != 5 && calls != 6 {
		t.Fatalf("provider calls = %d, want 5 (inject) or 6 (nudge)", calls)
	}
}

func exampleExternalHarness(t *testing.T, name string) fn.Func {
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

func TestTaskChildHarnessExecutesTool(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("hello-from-disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := fn.NewRegistry()
	registry.Register("reader", func(input fn.Input, _ fn.Provider, _ fn.Emit) (fn.Result, error) {
		if input.Tools.Execute == nil {
			return fn.Result{}, errors.New("tools.execute unavailable")
		}
		res, err := input.Tools.Execute(provider.ToolCall{
			ID:   "read-1",
			Name: "read",
			Args: json.RawMessage(`{"filePath":"note.txt"}`),
		})
		if err != nil {
			return fn.Result{}, err
		}
		if res.IsError {
			return fn.Result{}, fmt.Errorf("tool error: %s (%s)", res.Output, res.ErrorCode)
		}
		if !strings.Contains(res.Output, "hello-from-disk") {
			return fn.Result{}, fmt.Errorf("unexpected tool output %q", res.Output)
		}
		return fn.Result{Text: "read ok", StopReason: "end_turn"}, nil
	})
	prov := newScriptedProvider(
		toolCallStep(taskToolCallWithAgent("task-read", "read file", "worker")),
		func() streamStep {
			s := completedStep("parent finished")
			s.match = matchToolResult("task-read")
			return s
		}(),
		childCompletedNudgeStep("parent saw completion"),
	)
	childEvents := make(chan protocol.Event, 32)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "parent",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "parent",
		Agents: []engine.Agent{
			{Name: "parent"},
			{Name: "worker", Harness: "reader"},
		},
		HarnessRegistry: registry,
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewRead()),
		WorkDir:         dir,
		Rules:           []permission.Ruleset{permission.Defaults()},
		AppendChildEvent: func(_ string, event protocol.Event) error {
			select {
			case childEvents <- event:
			default:
			}
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "delegate"}
	events := drainAndReply(t, eng, 10*time.Second)

	var began, ended, completed bool
	for _, ev := range events {
		if c, ok := ev.(protocol.ChildCompleted); ok {
			if c.Status != protocol.ChildStatusCompleted || c.Summary != "read ok" {
				t.Fatalf("ChildCompleted = %#v", c)
			}
			completed = true
		}
	}
	if !completed {
		t.Fatalf("missing ChildCompleted; events=%#v", events)
	}
	// Drain child session timeline events (tool begin/end live there).
	deadline := time.After(2 * time.Second)
	for !began || !ended {
		select {
		case ev := <-childEvents:
			switch e := ev.(type) {
			case protocol.ToolCallBegin:
				if e.CallID == "read-1" && e.Name == "read" {
					began = true
				}
			case protocol.ToolCallEnd:
				if e.CallID == "read-1" && !e.IsError && strings.Contains(e.Output, "hello-from-disk") {
					ended = true
				}
			}
		case <-deadline:
			t.Fatalf("tool events missing begin=%v end=%v", began, ended)
		}
	}
}

func TestTaskChildHarnessToolDenial(t *testing.T) {
	registry := fn.NewRegistry()
	registry.Register("denied", func(input fn.Input, _ fn.Provider, _ fn.Emit) (fn.Result, error) {
		res, err := input.Tools.Execute(provider.ToolCall{
			ID:   "bash-1",
			Name: "bash",
			Args: json.RawMessage(`{"command":"echo hi"}`),
		})
		if err != nil {
			return fn.Result{}, err
		}
		if !res.IsError || res.ErrorCode != protocol.ErrorCodePermissionDenied {
			return fn.Result{}, fmt.Errorf("want permission_denied, got %#v", res)
		}
		return fn.Result{Text: "denied-ok", StopReason: "end_turn"}, nil
	})
	denyBash := permission.Ruleset{
		{Permission: "bash", Pattern: "*", Action: permission.Deny},
	}
	prov := newScriptedProvider(
		toolCallStep(taskToolCallWithAgent("task-deny", "try bash", "worker")),
		func() streamStep {
			s := completedStep("parent finished")
			s.match = matchToolResult("task-deny")
			return s
		}(),
		childCompletedNudgeStep("parent saw completion"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "parent",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "parent",
		Agents: []engine.Agent{
			{Name: "parent"},
			{Name: "worker", Harness: "denied"},
		},
		HarnessRegistry: registry,
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewBash()),
		WorkDir:         t.TempDir(),
		SandboxMode:     "off",
		Rules:           []permission.Ruleset{permission.Defaults(), denyBash},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "delegate"}
	events := drainAndReply(t, eng, 10*time.Second)
	for _, ev := range events {
		if c, ok := ev.(protocol.ChildCompleted); ok {
			if c.Status != protocol.ChildStatusCompleted || c.Summary != "denied-ok" {
				t.Fatalf("ChildCompleted = %#v", c)
			}
			return
		}
	}
	t.Fatalf("missing ChildCompleted; events=%#v", events)
}

func TestTaskChildHarnessUnknownAndMalformedTool(t *testing.T) {
	registry := fn.NewRegistry()
	registry.Register("bad-tools", func(input fn.Input, _ fn.Provider, _ fn.Emit) (fn.Result, error) {
		unknown, err := input.Tools.Execute(provider.ToolCall{ID: "u1", Name: "no_such_tool", Args: json.RawMessage(`{}`)})
		if err != nil {
			return fn.Result{}, err
		}
		if !unknown.IsError {
			return fn.Result{}, fmt.Errorf("unknown tool should error: %#v", unknown)
		}
		malformed, err := input.Tools.Execute(provider.ToolCall{ID: "m1", Name: "", Args: json.RawMessage(`{}`)})
		if err != nil {
			return fn.Result{}, err
		}
		if !malformed.IsError || malformed.ErrorCode != protocol.ErrorCodeInvalidArgs {
			return fn.Result{}, fmt.Errorf("malformed want invalid_args, got %#v", malformed)
		}
		badJSON, err := input.Tools.Execute(provider.ToolCall{ID: "j1", Name: "read", Args: json.RawMessage(`not-json`)})
		if err != nil {
			return fn.Result{}, err
		}
		if !badJSON.IsError || badJSON.ErrorCode != protocol.ErrorCodeInvalidArgs {
			return fn.Result{}, fmt.Errorf("bad json want invalid_args, got %#v", badJSON)
		}
		return fn.Result{Text: "validated", StopReason: "end_turn"}, nil
	})
	prov := newScriptedProvider(
		toolCallStep(taskToolCallWithAgent("task-bad", "validate", "worker")),
		func() streamStep {
			s := completedStep("parent finished")
			s.match = matchToolResult("task-bad")
			return s
		}(),
		childCompletedNudgeStep("parent saw completion"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "parent",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "parent",
		Agents: []engine.Agent{
			{Name: "parent"},
			{Name: "worker", Harness: "bad-tools"},
		},
		HarnessRegistry: registry,
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewRead()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "delegate"}
	events := drainAndReply(t, eng, 10*time.Second)
	for _, ev := range events {
		if c, ok := ev.(protocol.ChildCompleted); ok {
			if c.Status != protocol.ChildStatusCompleted || c.Summary != "validated" {
				t.Fatalf("ChildCompleted = %#v", c)
			}
			return
		}
	}
	t.Fatalf("missing ChildCompleted; events=%#v", events)
}

func TestTaskChildHarnessToolCancellation(t *testing.T) {
	registry := fn.NewRegistry()
	registry.Register("sleeper", func(input fn.Input, _ fn.Provider, _ fn.Emit) (fn.Result, error) {
		res, err := input.Tools.Execute(provider.ToolCall{
			ID:   "sleep-1",
			Name: "sleep",
			Args: json.RawMessage(`{"seconds":60}`),
		})
		if err != nil {
			return fn.Result{}, err
		}
		if res.IsError && res.ErrorCode == protocol.ErrorCodeCanceled {
			return fn.Result{}, context.Canceled
		}
		if input.Context.Err() != nil {
			return fn.Result{}, input.Context.Err()
		}
		return fn.Result{Text: "should-not-complete", StopReason: "end_turn"}, nil
	})

	var childMu sync.Mutex
	var childSession string
	setChild := func(id string) {
		childMu.Lock()
		childSession = id
		childMu.Unlock()
	}
	getChild := func() string {
		childMu.Lock()
		defer childMu.Unlock()
		return childSession
	}

	prov := newScriptedProvider(
		toolCallStep(taskToolCallWithAgent("task-sleep", "sleep", "worker")),
		streamStep{
			match: matchToolResult("task-sleep"),
			stream: func(ctx context.Context) <-chan provider.StreamEvent {
				// ChildStarted is emitted before task returns; wait briefly if needed.
				deadline := time.After(5 * time.Second)
				for getChild() == "" {
					select {
					case <-ctx.Done():
						ch := make(chan provider.StreamEvent)
						close(ch)
						return ch
					case <-deadline:
						ch := make(chan provider.StreamEvent, 1)
						ch <- provider.StreamEvent{Type: provider.EventError, Err: errors.New("child session id not observed")}
						close(ch)
						return ch
					case <-time.After(5 * time.Millisecond):
					}
				}
				args, _ := json.Marshal(map[string]any{"session_id": getChild()})
				ch := make(chan provider.StreamEvent, 2)
				ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID: "int-1", Name: "task_interrupt", Args: args,
				}}
				ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "tool_use"}
				close(ch)
				return ch
			},
		},
		func() streamStep {
			s := completedStep("parent finished")
			s.match = matchToolResult("int-1")
			return s
		}(),
	)
	childEvents := make(chan protocol.Event, 32)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "parent",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "parent",
		Agents: []engine.Agent{
			{Name: "parent"},
			{Name: "worker", Harness: "sleeper"},
		},
		HarnessRegistry: registry,
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewTaskInterrupt(), tool.NewSleep()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		AppendChildEvent: func(_ string, event protocol.Event) error {
			select {
			case childEvents <- event:
			default:
			}
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "delegate"}

	deadline := time.After(10 * time.Second)
	var sawBegin, sawChildDone bool
	for !sawChildDone {
		select {
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.ChildStarted:
				setChild(e.Correlation.SessionID)
			case protocol.ChildCompleted:
				sawChildDone = true
				if e.Status == protocol.ChildStatusCompleted {
					t.Fatalf("expected interrupted child, got %#v", e)
				}
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: e.RequestID, Decision: protocol.DecisionOnce}
			}
		case ev := <-childEvents:
			if b, ok := ev.(protocol.ToolCallBegin); ok && b.CallID == "sleep-1" {
				sawBegin = true
			}
			if e, ok := ev.(protocol.ToolCallEnd); ok && e.CallID == "sleep-1" && !e.IsError {
				t.Fatalf("sleep ToolCallEnd = %#v, want canceled error", e)
			}
		case <-deadline:
			t.Fatalf("timed out begin=%v childSession=%q", sawBegin, getChild())
		}
	}
	if !sawBegin {
		t.Fatal("sleep tool never began before cancel")
	}
}

type holdTool struct {
	mu        sync.Mutex
	active    int
	maxActive int
	entered   chan struct{}
	release   chan struct{}
}

func (h *holdTool) Name() string            { return "hold" }
func (h *holdTool) Description() string     { return "test hold tool" }
func (h *holdTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (h *holdTool) Execute(ctx context.Context, _ json.RawMessage, _ *tool.Context) (tool.Result, error) {
	h.mu.Lock()
	h.active++
	if h.active > h.maxActive {
		h.maxActive = h.active
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.active--
		h.mu.Unlock()
	}()
	select {
	case h.entered <- struct{}{}:
	default:
	}
	select {
	case <-h.release:
		return tool.Result{Output: "released"}, nil
	case <-ctx.Done():
		return tool.Result{}, ctx.Err()
	}
}

func TestTaskChildHarnessConcurrentToolsSerialized(t *testing.T) {
	hold := &holdTool{
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	registry := fn.NewRegistry()
	registry.Register("parallel", func(input fn.Input, _ fn.Provider, _ fn.Emit) (fn.Result, error) {
		type outcome struct {
			err error
		}
		done := make(chan outcome, 2)
		for i := 0; i < 2; i++ {
			id := fmt.Sprintf("hold-%d", i)
			go func() {
				_, err := input.Tools.Execute(provider.ToolCall{
					ID:   id,
					Name: "hold",
					Args: json.RawMessage(`{}`),
				})
				done <- outcome{err: err}
			}()
		}
		// First tool should enter; second must wait on the host mutex.
		select {
		case <-hold.entered:
		case <-time.After(3 * time.Second):
			return fn.Result{}, errors.New("first hold did not start")
		}
		time.Sleep(50 * time.Millisecond)
		hold.mu.Lock()
		peakDuringHold := hold.maxActive
		hold.mu.Unlock()
		close(hold.release)
		for range 2 {
			if o := <-done; o.err != nil {
				return fn.Result{}, o.err
			}
		}
		if peakDuringHold != 1 {
			return fn.Result{}, fmt.Errorf("overlapping Execute peak = %d, want 1", peakDuringHold)
		}
		return fn.Result{Text: "serialized", StopReason: "end_turn"}, nil
	})
	prov := newScriptedProvider(
		toolCallStep(taskToolCallWithAgent("task-par", "parallel tools", "worker")),
		func() streamStep {
			s := completedStep("parent finished")
			s.match = matchToolResult("task-par")
			return s
		}(),
		childCompletedNudgeStep("parent saw completion"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "parent",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "parent",
		Agents: []engine.Agent{
			{Name: "parent"},
			{Name: "worker", Harness: "parallel"},
		},
		HarnessRegistry: registry,
		Registry:        tool.NewRegistry(tool.NewTask(), hold),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "delegate"}
	events := drainAndReply(t, eng, 10*time.Second)
	for _, ev := range events {
		if c, ok := ev.(protocol.ChildCompleted); ok {
			if c.Status != protocol.ChildStatusCompleted || c.Summary != "serialized" {
				t.Fatalf("ChildCompleted = %#v", c)
			}
			return
		}
	}
	t.Fatalf("missing ChildCompleted; events=%#v", events)
}

func TestTaskChildHarnessExternalToolExecute(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data.txt")
	if err := os.WriteFile(target, []byte("external-tool-body"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Embedded harness using Tools.Execute (external JSONL path covered in
	// internal/fn/external).
	registry := fn.NewRegistry()
	registry.Register("ext-like", func(input fn.Input, _ fn.Provider, _ fn.Emit) (fn.Result, error) {
		res, err := input.Tools.Execute(provider.ToolCall{
			Name: "read",
			Args: json.RawMessage(`{"filePath":"data.txt"}`),
		})
		if err != nil {
			return fn.Result{}, err
		}
		if res.IsError || !strings.Contains(res.Output, "external-tool-body") {
			return fn.Result{}, fmt.Errorf("tool result = %#v", res)
		}
		return fn.Result{Text: res.Output, StopReason: "end_turn"}, nil
	})
	prov := newScriptedProvider(
		toolCallStep(taskToolCallWithAgent("task-ext-tool", "read", "worker")),
		func() streamStep {
			s := completedStep("parent finished")
			s.match = matchToolResult("task-ext-tool")
			return s
		}(),
		childCompletedNudgeStep("parent saw completion"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "parent",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		InitialAgent:    "parent",
		Agents: []engine.Agent{
			{Name: "parent"},
			{Name: "worker", Harness: "ext-like"},
		},
		HarnessRegistry: registry,
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewRead()),
		WorkDir:         dir,
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "delegate"}
	events := drainAndReply(t, eng, 10*time.Second)
	for _, ev := range events {
		if c, ok := ev.(protocol.ChildCompleted); ok {
			if c.Status != protocol.ChildStatusCompleted || !strings.Contains(c.Summary, "external-tool-body") {
				t.Fatalf("ChildCompleted = %#v", c)
			}
			return
		}
	}
	t.Fatalf("missing ChildCompleted; events=%#v", events)
}
