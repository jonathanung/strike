package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func waitToolCall(id string, events []string, timeout float64, sessionID string) provider.ToolCall {
	args := map[string]any{
		"events":          events,
		"timeout_seconds": timeout,
	}
	if sessionID != "" {
		args["session_id"] = sessionID
	}
	b, _ := json.Marshal(args)
	return provider.ToolCall{ID: id, Name: "wait", Args: b}
}

func waitRegistry(extra ...tool.Tool) *tool.Registry {
	base := []tool.Tool{
		tool.NewTask(),
		tool.NewTaskStatus(),
		tool.NewWait(),
	}
	return tool.NewRegistry(append(base, extra...)...)
}

func findWaitResolved(events []protocol.Event, waitCallID string) (protocol.WaitResolved, bool) {
	// Prefer WaitResolved events; fall back to tool end JSON.
	for i := len(events) - 1; i >= 0; i-- {
		if wr, ok := events[i].(protocol.WaitResolved); ok {
			return wr, true
		}
	}
	_ = waitCallID
	return protocol.WaitResolved{}, false
}

// TestWaitTaskDoneRace: parent waits while child is still running; child
// completes mid-wait; wait returns matched without busy-polling task_status.
func TestWaitTaskDoneRace(t *testing.T) {
	const childPrompt = "wait-done-child"
	taskCall := taskToolCall("task-wait-done", childPrompt)
	waitCall := waitToolCall("wait-1", []string{"task.done"}, 30, "")
	release := make(chan struct{})
	ct := &channelTool{
		executed: make(chan string, 1),
		blocks:   map[string]<-chan struct{}{"hold": release},
	}
	holdCall := toolCall("hold", "channel")

	prov := newScriptedProvider(
		toolCallStep(taskCall, waitCall),
		func() streamStep {
			s := toolCallStep(holdCall)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("child finished for wait")
			s.match = matchToolResult("hold")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent saw wait match")
			s.match = matchToolResult("wait-1")
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-wait-done",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        waitRegistry(ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn and wait"}

	var (
		events      []protocol.Event
		waitBegan   bool
		waitEnded   bool
		childDone   bool
		parentDone  bool
		sawStarted  bool
		sawResolved bool
	)
	guard := time.NewTimer(15 * time.Second)
	defer guard.Stop()
	for !(waitEnded && childDone && parentDone && sawStarted && sawResolved) {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed early")
			}
			events = append(events, ev)
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.ToolCallBegin:
				if ev.Name == "wait" {
					waitBegan = true
					select {
					case <-release:
					default:
						close(release)
					}
				}
			case protocol.ToolCallEnd:
				if ev.CallID == "wait-1" {
					waitEnded = true
					if ev.IsError {
						t.Fatalf("wait error: %s", ev.Output)
					}
					var res tool.WaitResult
					if err := json.Unmarshal([]byte(ev.Output), &res); err != nil {
						t.Fatalf("wait output: %v (%q)", err, ev.Output)
					}
					if res.Outcome != tool.WaitOutcomeMatched || res.Event != tool.WaitEventTaskDone {
						t.Fatalf("wait result = %+v", res)
					}
					if res.SessionID == "" {
						t.Fatal("expected session_id on match")
					}
					if !res.HasHandoff {
						t.Fatal("expected handoff on terminal match")
					}
				}
			case protocol.WaitStarted:
				sawStarted = true
				if len(ev.Events) != 1 || ev.Events[0] != tool.WaitEventTaskDone {
					t.Errorf("WaitStarted events = %v", ev.Events)
				}
			case protocol.WaitResolved:
				sawResolved = true
				if ev.Outcome != protocol.WaitOutcomeMatched || ev.Event != tool.WaitEventTaskDone {
					t.Errorf("WaitResolved = %#v", ev)
				}
			case protocol.ChildCompleted:
				childDone = true
			case protocol.TurnCompleted:
				if ev.ParentSessionID == "" && ev.Depth == 0 && waitEnded {
					parentDone = true
				}
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		case <-guard.C:
			t.Fatalf("timeout waitBegan=%v waitEnded=%v childDone=%v started=%v resolved=%v events=%v",
				waitBegan, waitEnded, childDone, sawStarted, sawResolved, summarizeEvents(events))
		}
	}
}

// TestWaitTimeout: no matching child event before timeout → structured timeout.
func TestWaitTimeout(t *testing.T) {
	waitCall := waitToolCall("wait-to", []string{"task.done"}, 0.15, "")
	prov := newScriptedProvider(
		toolCallStep(waitCall),
		func() streamStep {
			s := completedStep("timed out ok")
			s.match = matchToolResult("wait-to")
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-wait-timeout",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        waitRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "wait nothing"}

	events := drainUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		out, ok := toolEndOutput(evs, "wait-to")
		if !ok {
			return false
		}
		var res tool.WaitResult
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("parse: %v %q", err, out)
		}
		if res.Outcome != tool.WaitOutcomeTimeout {
			t.Fatalf("outcome = %q", res.Outcome)
		}
		_, hasResolved := findWaitResolved(evs, "wait-to")
		return hasResolved
	})
	var started, resolved int
	for _, ev := range events {
		switch ev.(type) {
		case protocol.WaitStarted:
			started++
		case protocol.WaitResolved:
			resolved++
		}
	}
	if started != 1 || resolved != 1 {
		t.Fatalf("started=%d resolved=%d", started, resolved)
	}
}

// TestWaitAlreadyTerminal: waiting after a finished child matches immediately
// (snapshot path — no hang until timeout).
func TestWaitAlreadyTerminal(t *testing.T) {
	const childPrompt = "wait-already-done"
	taskCall := taskToolCall("task-pre", childPrompt)
	release := make(chan struct{})
	close(release) // child does not block
	ct := &channelTool{executed: make(chan string, 1), blocks: map[string]<-chan struct{}{"x": release}}
	hold := toolCall("x", "channel")

	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := toolCallStep(hold)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("quick child done")
			s.match = matchToolResult("x")
			return s
		}(),
		// Parent continues after task tool result: wait any task.done
		// (child may already be terminal — snapshot must match).
		func() streamStep {
			s := toolCallStep(waitToolCall("wait-term", []string{"task.done", "task.failed"}, 10, ""))
			s.match = matchToolResult("task-pre")
			return s
		}(),
		func() streamStep {
			s := completedStep("matched terminal")
			s.match = matchToolResult("wait-term")
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-wait-terminal",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        waitRegistry(ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "spawn then wait"}

	events := drainUntil(t, eng, 15*time.Second, func(evs []protocol.Event) bool {
		out, ok := toolEndOutput(evs, "wait-term")
		if !ok {
			return false
		}
		var res tool.WaitResult
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatalf("%v %q", err, out)
		}
		return res.Outcome == tool.WaitOutcomeMatched && res.Event == tool.WaitEventTaskDone
	})
	if _, ok := findWaitResolved(events, "wait-term"); !ok {
		t.Fatal("missing WaitResolved")
	}
}

// TestWaitBlockedNeedsAttention: child bash permission ask wakes wait(task.blocked).
func TestWaitBlockedNeedsAttention(t *testing.T) {
	const childPrompt = "wait-blocked-child"
	taskCall := taskToolCall("task-block", childPrompt)
	waitCall := waitToolCall("wait-block", []string{"task.blocked"}, 30, "")
	bashCall := bashToolCall("bash-block", "echo blocked-wait")

	var waitMatched atomic.Bool

	prov := newScriptedProvider(
		toolCallStep(taskCall, waitCall),
		func() streamStep {
			s := toolCallStep(bashCall)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("after bash")
			s.match = matchToolResult("bash-block")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent after wait")
			s.match = matchToolResult("wait-block")
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-wait-block",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        waitRegistry(tool.NewBash()),
		WorkDir:         t.TempDir(),
		SandboxMode:     "off",
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "spawn blocked child"}

	var events []protocol.Event
	var parentDone bool
	guard := time.NewTimer(15 * time.Second)
	defer guard.Stop()
	for !parentDone {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed")
			}
			events = append(events, ev)
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				// Hold child bash ask open until wait matches needs_attention.
				if ev.Permission == "bash" && !waitMatched.Load() {
					go func(id string) {
						deadline := time.After(3 * time.Second)
						for !waitMatched.Load() {
							select {
							case <-deadline:
								eng.Ops() <- protocol.PermissionReply{RequestID: id, Decision: protocol.DecisionOnce}
								return
							case <-time.After(15 * time.Millisecond):
							}
						}
						eng.Ops() <- protocol.PermissionReply{RequestID: id, Decision: protocol.DecisionOnce}
					}(ev.RequestID)
				} else {
					eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
				}
			case protocol.ToolCallEnd:
				if ev.CallID == "wait-block" {
					var res tool.WaitResult
					if err := json.Unmarshal([]byte(ev.Output), &res); err != nil {
						t.Fatalf("%v %q", err, ev.Output)
					}
					if res.Outcome != tool.WaitOutcomeMatched || res.Event != tool.WaitEventTaskBlocked {
						t.Fatalf("wait = %+v", res)
					}
					if res.Status != "needs_attention" {
						t.Fatalf("status = %q", res.Status)
					}
					waitMatched.Store(true)
				}
			case protocol.WaitResolved:
				if ev.Outcome == protocol.WaitOutcomeMatched && ev.Event == tool.WaitEventTaskBlocked {
					waitMatched.Store(true)
				}
			case protocol.TurnCompleted:
				if ev.ParentSessionID == "" && ev.Depth == 0 && waitMatched.Load() {
					parentDone = true
				}
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		case <-guard.C:
			t.Fatalf("timeout; events=%v", summarizeEvents(events))
		}
	}
}

// TestWaitUnknownChildRejected: cannot wait on a foreign/unknown session.
func TestWaitUnknownChildRejected(t *testing.T) {
	waitCall := waitToolCall("wait-unk", []string{"task.done"}, 5, "not-a-real-child")
	prov := newScriptedProvider(
		toolCallStep(waitCall),
		func() streamStep {
			s := completedStep("rejected")
			s.match = matchToolResult("wait-unk")
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-wait-unk",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        waitRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "wait unknown"}

	drainUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		end, ok := toolEnd(evs, "wait-unk")
		if !ok {
			return false
		}
		if !end.IsError {
			t.Fatalf("expected error, got %q", end.Output)
		}
		if !strings.Contains(end.Output, "unknown") && !strings.Contains(end.Output, "inaccessible") {
			t.Fatalf("output = %q", end.Output)
		}
		return true
	})
}

// TestWaitCancelViaInterrupt: parent interrupt cancels an in-flight wait.
func TestWaitCancelViaInterrupt(t *testing.T) {
	waitCall := waitToolCall("wait-cancel", []string{"task.done"}, 30, "")
	prov := newScriptedProvider(
		toolCallStep(waitCall),
		// May or may not reach a follow-up depending on interrupt timing.
		func() streamStep {
			s := completedStep("after interrupt")
			s.match = matchToolResult("wait-cancel")
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-wait-cancel",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        waitRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "wait then interrupt"}

	var sawWaitBegin bool
	guard := time.NewTimer(10 * time.Second)
	defer guard.Stop()
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed")
			}
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.ToolCallBegin:
				if ev.Name == "wait" && !sawWaitBegin {
					sawWaitBegin = true
					eng.Ops() <- protocol.Interrupt{}
				}
			case protocol.ToolCallEnd:
				if ev.CallID == "wait-cancel" {
					// Either structured canceled outcome or canceled tool feedback.
					if strings.Contains(ev.Output, tool.WaitOutcomeCanceled) ||
						strings.Contains(strings.ToLower(ev.Output), "cancel") ||
						ev.IsError {
						return
					}
					var res tool.WaitResult
					if err := json.Unmarshal([]byte(ev.Output), &res); err == nil && res.Outcome == tool.WaitOutcomeCanceled {
						return
					}
					t.Fatalf("unexpected wait end: err=%v out=%q", ev.IsError, ev.Output)
				}
			case protocol.TurnCompleted:
				if sawWaitBegin {
					// Interrupt may complete turn without tool end in some paths.
					return
				}
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		case <-guard.C:
			t.Fatal("timeout waiting for interrupt of wait")
		}
	}
}

func TestLeafRegistryStripsWait(t *testing.T) {
	// Depth-capped leaves must not keep wait (parent-control like task_*).
	reg := tool.NewRegistry(tool.NewTask(), tool.NewWait(), tool.NewAgentMessage())
	leaf := reg.CloneWithout("task", "task_status", "task_read", "task_message", "task_interrupt", "wait")
	if _, ok := leaf.Get("wait"); ok {
		t.Fatal("wait should be stripped from leaf")
	}
	if _, ok := leaf.Get("agent_message"); !ok {
		t.Fatal("agent_message should remain")
	}
}
