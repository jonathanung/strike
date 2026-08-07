package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestRestoreSessionIncludesChildLineageEvents(t *testing.T) {
	corr := protocol.Correlation{SessionID: "child-1", ParentSessionID: "parent", Depth: 1, TurnID: "t1"}
	events := []protocol.Event{
		protocol.ModelSelected{Correlation: protocol.Correlation{SessionID: "child-1", ParentSessionID: "parent", Depth: 1}, Provider: "echo", Model: "echo"},
		protocol.AgentSelected{Correlation: protocol.Correlation{SessionID: "child-1", ParentSessionID: "parent", Depth: 1}, Name: "explore"},
		protocol.UserMessage{Correlation: corr, Text: "do work"},
		protocol.TextDelta{Correlation: corr, Text: "done"},
		protocol.TurnCompleted{Correlation: corr, StopReason: "end_turn"},
	}
	// Root Restore skips child lineage.
	root := engine.Restore(events)
	if len(root.Messages) != 0 {
		t.Fatalf("Restore(root filter) messages = %#v, want empty", root.Messages)
	}
	got := engine.RestoreSession(events, "child-1")
	if got.Provider != "echo" || got.Model != "echo" || got.Agent != "explore" {
		t.Fatalf("selections = %+v", got)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("messages = %#v", got.Messages)
	}
	if got.Messages[0].Text != "do work" || got.Messages[1].Text != "done" {
		t.Fatalf("messages = %#v", got.Messages)
	}
}

func TestRestoreSessionSettlesIncompleteTools(t *testing.T) {
	corr := protocol.Correlation{SessionID: "c", ParentSessionID: "p", Depth: 1}
	args := json.RawMessage(`{"command":"echo hi"}`)
	events := []protocol.Event{
		protocol.UserMessage{Correlation: corr, Text: "run"},
		protocol.ToolCallBegin{Correlation: corr, CallID: "x1", Name: "bash", Args: args},
		// No ToolCallEnd — incomplete at crash.
	}
	got := engine.RestoreSession(events, "c")
	if len(got.Messages) < 2 {
		t.Fatalf("messages = %#v", got.Messages)
	}
	// Last tool result should be synthetic error so tools are not re-run.
	var found bool
	for _, m := range got.Messages {
		if m.Role == provider.RoleTool && m.ToolResult != nil && m.ToolResult.CallID == "x1" {
			found = true
			if !m.ToolResult.IsError {
				t.Fatalf("want error tool result: %#v", m.ToolResult)
			}
		}
	}
	if !found {
		t.Fatalf("missing synthetic tool result: %#v", got.Messages)
	}
}

func TestResumeChildWorkingRestoresAndContinues(t *testing.T) {
	// Persist a working child log (no ChildCompleted), then resume with a
	// continuation prompt that finishes end_turn.
	parentID := "parent-sess"
	childID := "child-sess"
	corr := protocol.Correlation{SessionID: childID, ParentSessionID: parentID, Depth: 1, TurnID: "t1"}
	childEvents := []protocol.Event{
		protocol.ChildStarted{
			Correlation: corr,
			Agent:       "build",
			Prompt:      "original task",
			Name:        "worker",
		},
		protocol.ModelSelected{Correlation: corr, Provider: "scripted", Model: "m"},
		protocol.AgentSelected{Correlation: corr, Name: "build"},
		protocol.UserMessage{Correlation: corr, Text: "original task"},
		protocol.TextDelta{Correlation: corr, Text: "partial progress"},
		protocol.TurnCompleted{Correlation: corr, StopReason: "end_turn"},
	}

	var mu sync.Mutex
	store := map[string][]protocol.Event{childID: append([]protocol.Event(nil), childEvents...)}

	// Shared scripted provider: parent tool-call then child completion text.
	// Order of Stream calls: parent turn (tool call), parent follow-up, child turn.
	prov := newScriptedProvider(
		toolCallStep(provider.ToolCall{
			ID:   "r1",
			Name: "task",
			Args: json.RawMessage(`{"action":"resume","id":"child-sess","prompt":"continue please"}`),
		}),
		completedStep("parent done"),
		completedStep(`{"summary":"finished after resume","files_changed":[],"verification":"ok"}`),
	)

	eng := engine.New(engine.Options{
		SessionID:       parentID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		MaxChildDepth:   1,
		Agents:          []engine.Agent{{Name: "build", Description: "build"}},
		InitialAgent:    "build",
		LoadChildSession: func(id string) (engine.ChildSessionSnapshot, error) {
			mu.Lock()
			defer mu.Unlock()
			evs, ok := store[id]
			if !ok {
				return engine.ChildSessionSnapshot{}, errors.New("not found")
			}
			return engine.ChildSessionSnapshot{
				SessionID:       id,
				ParentSessionID: parentID,
				LeadSessionID:   parentID,
				Events:          append([]protocol.Event(nil), evs...),
			}, nil
		},
		ReopenChildSession: func(string) error { return nil },
		AppendChildEvent: func(id string, ev protocol.Event) error {
			mu.Lock()
			store[id] = append(store[id], ev)
			mu.Unlock()
			return nil
		},
		CloseChildSession: func(string) error { return nil },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "resume the child"}
	_ = receiveRequest(t, prov.requests)

	// Wait for child completion on parent stream.
	deadline := time.After(5 * time.Second)
	var (
		started   int
		completed int
		taskEnd   protocol.ToolCallEnd
		failMsgs  []string
	)
	for completed < 1 || taskEnd.CallID == "" {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("events closed")
			}
			switch e := ev.(type) {
			case protocol.ChildStarted:
				if e.SessionID == childID {
					started++
					if e.PolicyReason != "resumed" {
						t.Fatalf("PolicyReason = %q", e.PolicyReason)
					}
				}
			case protocol.ChildCompleted:
				if e.SessionID == childID {
					completed++
					if e.Status != protocol.ChildStatusCompleted {
						t.Fatalf("status = %s summary=%q fails=%v", e.Status, e.Summary, failMsgs)
					}
				}
			case protocol.EngineError:
				failMsgs = append(failMsgs, e.Message+"/"+e.Code)
			case protocol.ToolCallEnd:
				if e.CallID == "r1" {
					taskEnd = e
				}
			}
		case <-deadline:
			t.Fatalf("timeout started=%d completed=%d taskEnd=%q fails=%v", started, completed, taskEnd.CallID, failMsgs)
		}
	}
	if started < 1 {
		t.Fatal("expected ChildStarted on resume")
	}
	if !strings.Contains(taskEnd.Output, "Resumed child session") {
		t.Fatalf("task output = %q", taskEnd.Output)
	}
	if completed != 1 {
		t.Fatalf("ChildCompleted count = %d", completed)
	}
}

func TestResumeChildTerminalRefusedWithoutContinue(t *testing.T) {
	parentID := "p"
	childID := "c"
	corr := protocol.Correlation{SessionID: childID, ParentSessionID: parentID, Depth: 1}
	childEvents := []protocol.Event{
		protocol.ChildStarted{Correlation: corr, Agent: "build", Prompt: "x"},
		protocol.UserMessage{Correlation: corr, Text: "x"},
		protocol.TextDelta{Correlation: corr, Text: "done"},
		protocol.TurnCompleted{Correlation: corr, StopReason: "end_turn"},
		protocol.ChildCompleted{Correlation: corr, Status: protocol.ChildStatusCompleted, Summary: "done"},
	}
	parentProv := newScriptedProvider(
		toolCallStep(provider.ToolCall{
			ID:   "r1",
			Name: "task",
			Args: json.RawMessage(`{"action":"resume","id":"c"}`),
		}),
		completedStep("ok"),
	)
	eng := engine.New(engine.Options{
		SessionID:       parentID,
		Select:          func(string) (provider.Provider, string, error) { return parentProv, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		MaxChildDepth:   1,
		Agents:          []engine.Agent{{Name: "build"}},
		InitialAgent:    "build",
		LoadChildSession: func(id string) (engine.ChildSessionSnapshot, error) {
			return engine.ChildSessionSnapshot{
				SessionID: id, ParentSessionID: parentID, LeadSessionID: parentID,
				Events: childEvents,
			}, nil
		},
		ReopenChildSession: func(string) error { return nil },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "try resume"}
	_ = receiveRequest(t, parentProv.requests)
	_, events := collectThroughTurnCompleted(t, eng.Events())
	var end protocol.ToolCallEnd
	for _, ev := range events {
		if e, ok := ev.(protocol.ToolCallEnd); ok && e.CallID == "r1" {
			end = e
		}
	}
	if end.CallID == "" {
		t.Fatal("missing tool end")
	}
	if !end.IsError || !strings.Contains(end.Output, "terminal") {
		t.Fatalf("want terminal refuse, got %#v", end)
	}
	// No ChildCompleted from refused resume.
	for _, ev := range events {
		if _, ok := ev.(protocol.ChildCompleted); ok {
			t.Fatal("must not emit ChildCompleted on refused resume")
		}
	}
}

func TestResumeChildForeignOwnershipFails(t *testing.T) {
	parentProv := newScriptedProvider(
		toolCallStep(provider.ToolCall{
			ID:   "r1",
			Name: "task",
			Args: json.RawMessage(`{"action":"resume","id":"foreign"}`),
		}),
		completedStep("ok"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "my-parent",
		Select:          func(string) (provider.Provider, string, error) { return parentProv, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		MaxChildDepth:   1,
		Agents:          []engine.Agent{{Name: "build"}},
		InitialAgent:    "build",
		LoadChildSession: func(id string) (engine.ChildSessionSnapshot, error) {
			return engine.ChildSessionSnapshot{
				SessionID: id, ParentSessionID: "other-parent", LeadSessionID: "other-root",
				Events: []protocol.Event{
					protocol.ChildStarted{Correlation: protocol.Correlation{SessionID: id, ParentSessionID: "other-parent", Depth: 1}, Agent: "build", Prompt: "x"},
				},
			}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "resume foreign"}
	_ = receiveRequest(t, parentProv.requests)
	_, events := collectThroughTurnCompleted(t, eng.Events())
	var end protocol.ToolCallEnd
	for _, ev := range events {
		if e, ok := ev.(protocol.ToolCallEnd); ok && e.CallID == "r1" {
			end = e
		}
	}
	if end.CallID == "" || !end.IsError || !strings.Contains(strings.ToLower(end.Output), "not owned") {
		t.Fatalf("want ownership failure, got %#v", end)
	}
}

func TestResumeChildMissingFailsSafely(t *testing.T) {
	parentProv := newScriptedProvider(
		toolCallStep(provider.ToolCall{
			ID:   "r1",
			Name: "task",
			Args: json.RawMessage(`{"action":"resume","id":"missing"}`),
		}),
		completedStep("ok"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "p",
		Select:          func(string) (provider.Provider, string, error) { return parentProv, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		MaxChildDepth:   1,
		Agents:          []engine.Agent{{Name: "build"}},
		InitialAgent:    "build",
		LoadChildSession: func(string) (engine.ChildSessionSnapshot, error) {
			return engine.ChildSessionSnapshot{}, errors.New("not found")
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "resume missing"}
	_ = receiveRequest(t, parentProv.requests)
	_, events := collectThroughTurnCompleted(t, eng.Events())
	var end protocol.ToolCallEnd
	for _, ev := range events {
		if e, ok := ev.(protocol.ToolCallEnd); ok && e.CallID == "r1" {
			end = e
		}
	}
	if end.CallID == "" || !end.IsError || !strings.Contains(end.Output, "not found") {
		t.Fatalf("want not found, got %#v", end)
	}
}
