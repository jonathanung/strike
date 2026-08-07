package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/fault"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/session"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// dropStep is an incomplete provider stream (no terminal event).
func dropStep() streamStep {
	return streamStep{events: []provider.StreamEvent{
		{Type: provider.EventTextDelta, Text: "partial-before-drop"},
	}}
}

// Chaos: provider.stream_drop until retries exhaust — turn ends with error
// and EngineError; session tee remains loadable (or CorruptError, never garbage).
func TestChaosProviderStreamDrop(t *testing.T) {
	t.Cleanup(fault.Reset)
	prov := newScriptedProvider(dropStep(), dropStep(), dropStep())
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	info, err := mgr.Create(session.CreateOptions{ID: "chaos-stream-drop"})
	if err != nil {
		t.Fatal(err)
	}

	eng := engine.New(engine.Options{
		SessionID:          info.ID,
		Select:             func(string) (provider.Provider, string, error) { return prov, "test-model", nil },
		InitialProvider:    "scripted",
		Registry:           tool.NewRegistry(),
		WorkDir:            t.TempDir(),
		Rules:              []permission.Ruleset{permission.Defaults()},
		MaxStreamAttempts:  3,
		StreamRetryBackoff: func(int) time.Duration { return 0 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "stream-drop"}

	var (
		failure   protocol.EngineError
		completed protocol.TurnCompleted
		events    []protocol.Event
	)
	deadline := time.After(5 * time.Second)
	for completed.StopReason == "" {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("events closed")
			}
			events = append(events, ev)
			// Tee like cmd/strike so durability is exercised under failure.
			if err := mgr.Append(info.ID, ev); err != nil {
				t.Fatalf("session append during chaos: %v", err)
			}
			switch e := ev.(type) {
			case protocol.EngineError:
				failure = e
			case protocol.TurnCompleted:
				completed = e
			}
		case <-deadline:
			t.Fatal("timeout waiting for stream-drop turn end")
		}
	}
	if completed.StopReason != "error" {
		t.Fatalf("stopReason = %q, want error", completed.StopReason)
	}
	if failure.Message == "" {
		t.Fatal("expected EngineError")
	}
	// Exhausted incomplete streams surface ErrIncompleteStream (or wording).
	if failure.Message != provider.ErrIncompleteStream.Error() &&
		!strings.Contains(failure.Message, "stream") &&
		!strings.Contains(failure.Message, "closed") {
		t.Fatalf("EngineError message = %q, want incomplete-stream wording", failure.Message)
	}
	if err := mgr.Close(info.ID); err != nil {
		t.Fatal(err)
	}

	path := session.LogPath(dir, info.ID)
	got, rerr := session.Replay(path)
	if rerr != nil {
		var ce *session.CorruptError
		if !errors.As(rerr, &ce) {
			t.Fatalf("Replay = %v, want nil or CorruptError", rerr)
		}
	} else if len(got) == 0 && len(events) > 0 {
		t.Fatal("session empty after teed events")
	}
	// No secret-shaped garbage required here; ensure JSONL is not empty trash.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		// Trailing partial is OK only if Replay handled it; already checked.
		t.Logf("log ends without newline (%d bytes) — Replay must still be safe", len(raw))
	}
}

// Chaos: cancel during an incomplete/hanging provider stream ends interrupted.
func TestChaosCancelDuringProviderStream(t *testing.T) {
	t.Cleanup(fault.Reset)
	unblock := make(chan struct{})
	prov := newScriptedProvider(streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
		ch := make(chan provider.StreamEvent)
		go func() {
			ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: "hi"}
			select {
			case <-ctx.Done():
			case <-unblock:
			}
			close(ch)
		}()
		return ch
	}})
	eng := newTestEngine(t, prov)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "hang-stream"}
	_ = receiveRequest(t, prov.requests)
	_ = receiveEvent(t, eng.Events(), func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TextDelta)
		return ok
	})
	eng.Ops() <- protocol.Interrupt{}
	completed := waitForTurnCompleted(t, eng.Events())
	if completed.StopReason != "interrupted" {
		t.Fatalf("stopReason = %q, want interrupted", completed.StopReason)
	}
	close(unblock)
}

// Chaos: permission.flip_mid_turn — reject after PermissionAsked yields
// permission_denied tool end and a defined turn stop (interrupted).
func TestChaosPermissionFlipMidTurn(t *testing.T) {
	t.Cleanup(fault.Reset)
	call := provider.ToolCall{ID: "flip-1", Name: "bash", Args: json.RawMessage(`{"command":"echo hi"}`)}
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &call},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		// Model may continue after reject depending on stop reason; allow a finish.
		completedStep("ack deny"),
	)
	eng := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return prov, "test-model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewBash()),
		WorkDir:         t.TempDir(),
		SandboxMode:     "off",
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "need bash"}

	var (
		end       protocol.ToolCallEnd
		completed protocol.TurnCompleted
		sawAsk    bool
	)
	deadline := time.After(5 * time.Second)
	for completed.StopReason == "" {
		select {
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.PermissionAsked:
				sawAsk = true
				// Mid-turn flip: reject instead of allow.
				eng.Ops() <- protocol.PermissionReply{
					RequestID: e.RequestID,
					Decision:  protocol.DecisionReject,
					Message:   "chaos deny",
				}
			case protocol.ToolCallEnd:
				end = e
			case protocol.TurnCompleted:
				completed = e
			case protocol.EngineError:
				t.Fatalf("engine error: %s", e.Message)
			}
		case <-deadline:
			t.Fatal("timeout")
		}
	}
	if !sawAsk {
		t.Fatal("expected PermissionAsked")
	}
	if end.CallID != call.ID || !end.IsError {
		t.Fatalf("ToolCallEnd = %#v", end)
	}
	if end.ErrorCode != protocol.ErrorCodePermissionDenied && end.ErrorCode != "permission_denied" {
		t.Fatalf("ErrorCode = %q, want permission_denied", end.ErrorCode)
	}
	// Reject historically interrupts the turn; accept interrupted or end_turn
	// if the engine continues after feeding deny to the model.
	switch completed.StopReason {
	case "interrupted", "end_turn":
	default:
		t.Fatalf("stopReason = %q", completed.StopReason)
	}
}

// Chaos: hard-deny ruleset mid-profile (no ask) still settles permission_denied.
func TestChaosPermissionHardDenyMidTurn(t *testing.T) {
	t.Cleanup(fault.Reset)
	call := provider.ToolCall{ID: "deny-chaos", Name: "bash", Args: json.RawMessage(`{"command":"echo hi"}`)}
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("ok"),
	)
	eng := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return prov, "test-model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewBash()),
		WorkDir:         t.TempDir(),
		SandboxMode:     "off",
		Rules: []permission.Ruleset{{
			{Permission: "bash", Pattern: "*", Action: permission.Deny},
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "deny bash"}
	_ = receiveRequest(t, prov.requests)
	completed, events := collectThroughTurnCompleted(t, eng.Events())
	if completed.StopReason != "end_turn" {
		t.Fatalf("stopReason = %q, want end_turn (model continues after deny feedback)", completed.StopReason)
	}
	var end protocol.ToolCallEnd
	for _, ev := range events {
		if e, ok := ev.(protocol.ToolCallEnd); ok {
			end = e
		}
		if _, ok := ev.(protocol.PermissionAsked); ok {
			t.Fatal("hard deny must not ask")
		}
	}
	if !end.IsError || (end.ErrorCode != protocol.ErrorCodePermissionDenied && end.ErrorCode != "permission_denied") {
		t.Fatalf("ToolCallEnd = %#v", end)
	}
}

// Chaos: cancel + tool inject — interrupt while a long tool runs yields canceled
// code and interrupted turn (session optional).
func TestChaosCancelDuringToolWithSession(t *testing.T) {
	t.Cleanup(fault.Reset)
	pt := &partialCancelTool{started: make(chan struct{})}
	call := toolCall("chaos-c1", "partial")
	prov := newScriptedProvider(toolCallStep(call), completedStep("after"))
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	info, err := mgr.Create(session.CreateOptions{ID: "chaos-cancel-tool"})
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(engine.Options{
		SessionID:       info.ID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "test-model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(pt),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "cancel-tool"}
	_ = receiveRequest(t, prov.requests)
	select {
	case <-pt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool never started")
	}
	eng.Ops() <- protocol.Interrupt{}

	var (
		end       protocol.ToolCallEnd
		completed protocol.TurnCompleted
	)
	deadline := time.After(5 * time.Second)
	for completed.StopReason == "" {
		select {
		case ev := <-eng.Events():
			_ = mgr.Append(info.ID, ev)
			switch e := ev.(type) {
			case protocol.ToolCallEnd:
				end = e
			case protocol.TurnCompleted:
				completed = e
			}
		case <-deadline:
			t.Fatal("timeout")
		}
	}
	if completed.StopReason != "interrupted" {
		t.Fatalf("stopReason = %q", completed.StopReason)
	}
	if end.ErrorCode != protocol.ErrorCodeCanceled {
		t.Fatalf("ErrorCode = %q, want canceled", end.ErrorCode)
	}
	if err := mgr.Close(info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Replay(session.LogPath(dir, info.ID)); err != nil {
		t.Fatalf("session must remain loadable: %v", err)
	}
}
