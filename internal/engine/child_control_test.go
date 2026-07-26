package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func controlToolCall(id, name string, args map[string]any) provider.ToolCall {
	b, _ := json.Marshal(args)
	return provider.ToolCall{ID: id, Name: name, Args: b}
}

func taskControlRegistry(extra ...tool.Tool) *tool.Registry {
	base := []tool.Tool{
		tool.NewTask(),
		tool.NewTaskStatus(),
		tool.NewTaskRead(),
		tool.NewTaskMessage(),
		tool.NewTaskInterrupt(),
	}
	return tool.NewRegistry(append(base, extra...)...)
}

func matchLatestUserText(text string) func(provider.Request) bool {
	return func(req provider.Request) bool {
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == provider.RoleUser {
				return req.Messages[i].Text == text
			}
		}
		return false
	}
}

func drainUntil(t *testing.T, eng *engine.Engine, timeout time.Duration, done func([]protocol.Event) bool) []protocol.Event {
	t.Helper()
	guard := time.NewTimer(timeout)
	defer guard.Stop()
	var events []protocol.Event
	for {
		if done(events) {
			return events
		}
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed early")
			}
			events = append(events, ev)
			if pa, ok := ev.(protocol.PermissionAsked); ok {
				eng.Ops() <- protocol.PermissionReply{RequestID: pa.RequestID, Decision: protocol.DecisionOnce}
			}
			if done(events) {
				return events
			}
		case <-guard.C:
			t.Fatalf("timeout; events=%v", summarizeEvents(events))
		}
	}
}

func toolEndOutput(events []protocol.Event, callID string) (string, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if end, ok := events[i].(protocol.ToolCallEnd); ok && end.CallID == callID {
			return end.Output, true
		}
	}
	return "", false
}

func toolEnd(events []protocol.Event, callID string) (protocol.ToolCallEnd, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if end, ok := events[i].(protocol.ToolCallEnd); ok && end.CallID == callID {
			return end, true
		}
	}
	return protocol.ToolCallEnd{}, false
}

// TestTaskControlTwoChildren covers concurrent children, live status, bounded
// read, message, ownership denial, interrupt, and interrupt idempotency.
func TestTaskControlTwoChildren(t *testing.T) {
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})
	ct := &channelTool{
		executed: make(chan string, 4),
		blocks: map[string]<-chan struct{}{
			"hold-a": releaseA,
			"hold-b": releaseB,
		},
	}
	const (
		promptA = "child-a-prompt-ctrl"
		promptB = "child-b-prompt-ctrl"
	)
	taskA := taskToolCall("task-a", promptA)
	taskB := taskToolCall("task-b", promptB)

	var (
		turn2Calls []provider.ToolCall
		turn2Ready = make(chan struct{})
	)

	prov := newScriptedProvider(
		toolCallStep(taskA, taskB),
		func() streamStep {
			s := completedStep("both started")
			s.match = func(req provider.Request) bool {
				return matchToolResult("task-a")(req) && matchToolResult("task-b")(req)
			}
			return s
		}(),
		func() streamStep {
			s := toolCallStep(toolCall("hold-a", "channel"))
			s.match = matchUserText(promptA)
			return s
		}(),
		func() streamStep {
			s := toolCallStep(toolCall("hold-b", "channel"))
			s.match = matchUserText(promptB)
			return s
		}(),
		func() streamStep {
			return streamStep{
				match: matchLatestUserText("control children"),
				stream: func(ctx context.Context) <-chan provider.StreamEvent {
					select {
					case <-turn2Ready:
					case <-ctx.Done():
						ch := make(chan provider.StreamEvent)
						close(ch)
						return ch
					}
					events := make([]provider.StreamEvent, 0, len(turn2Calls)+1)
					for i := range turn2Calls {
						call := turn2Calls[i]
						events = append(events, provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &call})
					}
					events = append(events, provider.StreamEvent{Type: provider.EventDone, StopReason: "tool_use"})
					ch := make(chan provider.StreamEvent, len(events))
					for _, ev := range events {
						ch <- ev
					}
					close(ch)
					return ch
				},
			}
		}(),
		func() streamStep {
			s := completedStep("control done")
			s.match = matchToolResult("st-a")
			return s
		}(),
		// Child completions after release/interrupt (may be unused if interrupted).
		func() streamStep {
			s := completedStep("child A finished")
			s.match = matchToolResult("hold-a")
			return s
		}(),
		func() streamStep {
			s := completedStep("child B finished")
			s.match = matchToolResult("hold-b")
			return s
		}(),
		func() streamStep {
			s := completedStep("nudge")
			s.match = func(req provider.Request) bool {
				if len(req.Messages) == 0 {
					return false
				}
				last := req.Messages[len(req.Messages)-1]
				return last.Role == provider.RoleUser && strings.Contains(last.Text, "[child.completed")
			}
			return s
		}(),
		func() streamStep {
			s := completedStep("nudge2")
			s.match = func(req provider.Request) bool {
				if len(req.Messages) == 0 {
					return false
				}
				last := req.Messages[len(req.Messages)-1]
				return last.Role == provider.RoleUser && strings.Contains(last.Text, "[child.completed")
			}
			return s
		}(),
	)

	eng := engine.New(engine.Options{
		SessionID:       "parent-ctrl",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        taskControlRegistry(ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn two"}
	events := drainUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		return countEvents[protocol.ChildStarted](evs) == 2 &&
			countEvents[protocol.TurnCompleted](evs) >= 1
	})
	idByPrompt := map[string]string{}
	for _, ev := range events {
		if cs, ok := ev.(protocol.ChildStarted); ok {
			idByPrompt[cs.Prompt] = cs.SessionID
		}
	}
	idA, idB := idByPrompt[promptA], idByPrompt[promptB]
	if idA == "" || idB == "" || idA == idB {
		t.Fatalf("ids by prompt = %v", idByPrompt)
	}

	deadline := time.After(5 * time.Second)
	got := map[string]bool{}
	for len(got) < 2 {
		select {
		case id := <-ct.executed:
			got[id] = true
		case <-deadline:
			t.Fatalf("blocked tools = %v", got)
		}
	}

	turn2Calls = []provider.ToolCall{
		controlToolCall("st-a", "task_status", map[string]any{"session_id": idA, "include_recent": true}),
		controlToolCall("rd-a", "task_read", map[string]any{"session_id": idA, "last": 20, "include_tools": true}),
		controlToolCall("msg-b", "task_message", map[string]any{"session_id": idB, "text": "steer once"}),
		controlToolCall("st-unk", "task_status", map[string]any{"session_id": "foreign-session-xyz"}),
		controlToolCall("int-b", "task_interrupt", map[string]any{"session_id": idB}),
		controlToolCall("int-b2", "task_interrupt", map[string]any{"session_id": idB}),
	}
	close(turn2Ready)

	eng.Ops() <- protocol.UserInput{Text: "control children"}
	events = drainUntil(t, eng, 15*time.Second, func(evs []protocol.Event) bool {
		_, ok1 := toolEndOutput(evs, "st-a")
		_, ok2 := toolEndOutput(evs, "rd-a")
		_, ok3 := toolEndOutput(evs, "msg-b")
		_, ok4 := toolEndOutput(evs, "st-unk")
		_, ok5 := toolEndOutput(evs, "int-b")
		_, ok6 := toolEndOutput(evs, "int-b2")
		return ok1 && ok2 && ok3 && ok4 && ok5 && ok6
	})

	out, _ := toolEndOutput(events, "st-a")
	if !strings.Contains(out, "working") && !strings.Contains(out, "needs_attention") && !strings.Contains(out, "starting") {
		t.Fatalf("live status = %s", out)
	}
	if !strings.Contains(out, idA) {
		t.Fatalf("status missing session id: %s", out)
	}

	readOut, _ := toolEndOutput(events, "rd-a")
	var parsed tool.TaskReadResult
	if err := json.Unmarshal([]byte(readOut), &parsed); err != nil {
		t.Fatalf("read parse %s: %v", readOut, err)
	}
	if parsed.Total < 1 || len(parsed.Entries) == 0 {
		t.Fatalf("empty read: %+v", parsed)
	}
	if parsed.Limit > 100 {
		t.Fatalf("unbounded limit %d", parsed.Limit)
	}
	for i := 1; i < len(parsed.Entries); i++ {
		if parsed.Entries[i].Index < parsed.Entries[i-1].Index {
			t.Fatalf("unordered entries: %+v", parsed.Entries)
		}
	}

	msgOut, _ := toolEndOutput(events, "msg-b")
	if !strings.Contains(msgOut, `"status":"queued"`) && !strings.Contains(msgOut, `"status":"accepted"`) {
		t.Fatalf("message = %s", msgOut)
	}

	unk, ok := toolEnd(events, "st-unk")
	if !ok || !unk.IsError || !strings.Contains(strings.ToLower(unk.Output), "unknown") {
		t.Fatalf("unknown status = %#v", unk)
	}

	intOut, _ := toolEndOutput(events, "int-b")
	if !strings.Contains(intOut, "canceled") && !strings.Contains(intOut, "interrupt") {
		t.Fatalf("interrupt = %s", intOut)
	}
	int2, _ := toolEndOutput(events, "int-b2")
	if !strings.Contains(int2, "canceled") && !strings.Contains(int2, "finished") && !strings.Contains(int2, "interrupt") {
		t.Fatalf("interrupt2 = %s", int2)
	}

	// A must still be running; only B canceled.
	for _, ev := range events {
		if cc, ok := ev.(protocol.ChildCompleted); ok && cc.SessionID == idA {
			t.Fatalf("child A completed before release: %#v", cc)
		}
	}

	close(releaseA)
	close(releaseB)
	// Observe A completion (and any residual B) without blocking forever.
	guard := time.NewTimer(10 * time.Second)
	defer guard.Stop()
	var sawA bool
	for !sawA {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed")
			}
			if pa, ok := ev.(protocol.PermissionAsked); ok {
				eng.Ops() <- protocol.PermissionReply{RequestID: pa.RequestID, Decision: protocol.DecisionOnce}
			}
			if cc, ok := ev.(protocol.ChildCompleted); ok && cc.SessionID == idA {
				sawA = true
				if cc.Status != protocol.ChildStatusCompleted {
					t.Fatalf("A status = %s, want completed", cc.Status)
				}
			}
		case <-guard.C:
			t.Fatal("timeout waiting A completed")
		}
	}
	cancel()
}

func TestTaskStatusTerminalAfterComplete(t *testing.T) {
	const childPrompt = "fast-child-term"
	taskCall := taskToolCall("task-term", childPrompt)
	var (
		childID string
		idReady = make(chan string, 1)
	)
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("parent after spawn")
			s.match = matchToolResult("task-term")
			return s
		}(),
		func() streamStep {
			s := completedStep("child terminal summary text")
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("nudge")
			s.match = func(req provider.Request) bool {
				if len(req.Messages) == 0 {
					return false
				}
				last := req.Messages[len(req.Messages)-1]
				return last.Role == provider.RoleUser && strings.Contains(last.Text, "[child.completed")
			}
			return s
		}(),
		func() streamStep {
			return streamStep{
				match: matchLatestUserText("check terminal"),
				stream: func(ctx context.Context) <-chan provider.StreamEvent {
					select {
					case childID = <-idReady:
					case <-ctx.Done():
						ch := make(chan provider.StreamEvent)
						close(ch)
						return ch
					case <-time.After(5 * time.Second):
						ch := make(chan provider.StreamEvent)
						close(ch)
						return ch
					}
					call := controlToolCall("st-done", "task_status", map[string]any{"session_id": childID})
					ch := make(chan provider.StreamEvent, 3)
					ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &call}
					ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "tool_use"}
					close(ch)
					return ch
				},
			}
		}(),
		func() streamStep {
			s := completedStep("saw terminal")
			s.match = matchToolResult("st-done")
			return s
		}(),
	)

	eng := engine.New(engine.Options{
		SessionID:       "parent-term",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        taskControlRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}
	events := drainAndReply(t, eng, 10*time.Second)
	for _, ev := range events {
		if cs, ok := ev.(protocol.ChildStarted); ok {
			childID = cs.SessionID
		}
	}
	if childID == "" {
		t.Fatal("missing child id")
	}
	idReady <- childID

	eng.Ops() <- protocol.UserInput{Text: "check terminal"}
	events = drainUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		_, ok := toolEndOutput(evs, "st-done")
		return ok
	})
	out, _ := toolEndOutput(events, "st-done")
	if !strings.Contains(out, `"state":"completed"`) {
		t.Fatalf("status = %s", out)
	}
	if strings.Contains(out, `"terminal_summary":null`) {
		t.Fatalf("want terminal summary: %s", out)
	}
	if !strings.Contains(out, "child terminal summary text") && !strings.Contains(out, "task completed") {
		t.Fatalf("summary missing: %s", out)
	}
}

func TestTaskMessageRejectedWhenCompleted(t *testing.T) {
	const childPrompt = "quick-child"
	taskCall := taskToolCall("task-msg", childPrompt)
	var childID string
	childIDCh := make(chan string, 1)
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("parent done")
			s.match = matchToolResult("task-msg")
			return s
		}(),
		func() streamStep {
			s := completedStep("child quick done")
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("nudge")
			s.match = func(req provider.Request) bool {
				if len(req.Messages) == 0 {
					return false
				}
				last := req.Messages[len(req.Messages)-1]
				return last.Role == provider.RoleUser && strings.Contains(last.Text, "[child.completed")
			}
			return s
		}(),
		func() streamStep {
			return streamStep{
				match: matchLatestUserText("message late"),
				stream: func(ctx context.Context) <-chan provider.StreamEvent {
					select {
					case childID = <-childIDCh:
					case <-ctx.Done():
						ch := make(chan provider.StreamEvent)
						close(ch)
						return ch
					case <-time.After(5 * time.Second):
						ch := make(chan provider.StreamEvent)
						close(ch)
						return ch
					}
					call := controlToolCall("msg-late", "task_message", map[string]any{
						"session_id": childID,
						"text":       "too late",
					})
					ch := make(chan provider.StreamEvent, 3)
					ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &call}
					ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "tool_use"}
					close(ch)
					return ch
				},
			}
		}(),
		func() streamStep {
			s := completedStep("after late message")
			s.match = matchToolResult("msg-late")
			return s
		}(),
	)

	eng := engine.New(engine.Options{
		SessionID:       "parent-msg",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        taskControlRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}
	events := drainAndReply(t, eng, 10*time.Second)
	var id string
	for _, ev := range events {
		if cs, ok := ev.(protocol.ChildStarted); ok {
			id = cs.SessionID
		}
	}
	if id == "" {
		t.Fatal("no child id")
	}
	childIDCh <- id

	eng.Ops() <- protocol.UserInput{Text: "message late"}
	events = drainUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		_, ok := toolEndOutput(evs, "msg-late")
		return ok
	})
	end, ok := toolEnd(events, "msg-late")
	if !ok || !end.IsError || !strings.Contains(end.Output, "closed") {
		t.Fatalf("late message = %#v", end)
	}
}

func TestTaskReadBoundedOrdering(t *testing.T) {
	const childPrompt = "read-child"
	taskCall := taskToolCall("task-rd", childPrompt)
	var childID string
	idCh := make(chan string, 1)
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("parent")
			s.match = matchToolResult("task-rd")
			return s
		}(),
		func() streamStep {
			s := completedStep("child says hello world")
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("nudge")
			s.match = func(req provider.Request) bool {
				if len(req.Messages) == 0 {
					return false
				}
				last := req.Messages[len(req.Messages)-1]
				return last.Role == provider.RoleUser && strings.Contains(last.Text, "[child.completed")
			}
			return s
		}(),
		func() streamStep {
			return streamStep{
				match: matchLatestUserText("read child"),
				stream: func(ctx context.Context) <-chan provider.StreamEvent {
					select {
					case childID = <-idCh:
					case <-ctx.Done():
						ch := make(chan provider.StreamEvent)
						close(ch)
						return ch
					}
					call := controlToolCall("rd1", "task_read", map[string]any{
						"session_id": childID,
						"last":       5,
					})
					ch := make(chan provider.StreamEvent, 3)
					ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &call}
					ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "tool_use"}
					close(ch)
					return ch
				},
			}
		}(),
		func() streamStep {
			s := completedStep("read ok")
			s.match = matchToolResult("rd1")
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-rd",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        taskControlRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}
	events := drainAndReply(t, eng, 10*time.Second)
	for _, ev := range events {
		if cs, ok := ev.(protocol.ChildStarted); ok {
			childID = cs.SessionID
		}
	}
	if childID == "" {
		t.Fatal("missing child")
	}
	idCh <- childID

	eng.Ops() <- protocol.UserInput{Text: "read child"}
	events = drainUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		_, ok := toolEndOutput(evs, "rd1")
		return ok
	})
	out, _ := toolEndOutput(events, "rd1")
	var parsed tool.TaskReadResult
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse %s: %v", out, err)
	}
	if parsed.Total < 1 || len(parsed.Entries) == 0 {
		t.Fatalf("empty read: %+v", parsed)
	}
	if parsed.Limit > 100 {
		t.Fatalf("limit not bounded: %d", parsed.Limit)
	}
	for i := 1; i < len(parsed.Entries); i++ {
		if parsed.Entries[i].Index < parsed.Entries[i-1].Index {
			t.Fatalf("entries not ordered: %+v", parsed.Entries)
		}
	}
}
