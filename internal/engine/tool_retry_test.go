package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// flakySafeTool is safe-retry and fails transiently N times then succeeds.
type flakySafeTool struct {
	failsLeft *atomic.Int32
	runs      *atomic.Int32
}

func (f *flakySafeTool) Name() string        { return "flaky_read" }
func (f *flakySafeTool) Description() string { return "safe-retry flaky" }
func (f *flakySafeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (f *flakySafeTool) Contract() tool.Contract {
	return tool.Contract{
		Version:     tool.ContractVersion,
		SideEffect:  tool.SideEffectRead,
		Idempotency: tool.IdempotencySafeRetry,
	}
}
func (f *flakySafeTool) Execute(context.Context, json.RawMessage, *tool.Context) (tool.Result, error) {
	f.runs.Add(1)
	if f.failsLeft.Add(-1) >= 0 {
		return tool.Result{}, tool.ErrTransient("connection reset")
	}
	return tool.Result{Title: "ok", Output: "recovered"}, nil
}

// mutativeOnceTool is apply_patch-shaped: conditional + workspace-mutative.
// Counts Execute calls; always returns transient so a buggy retry would double-apply.
type mutativeOnceTool struct {
	runs *atomic.Int32
}

func (m *mutativeOnceTool) Name() string        { return "apply_patch" }
func (m *mutativeOnceTool) Description() string { return "mutative" }
func (m *mutativeOnceTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (m *mutativeOnceTool) Contract() tool.Contract {
	return tool.LookupContract(tool.NewApplyPatch())
}
func (m *mutativeOnceTool) Execute(context.Context, json.RawMessage, *tool.Context) (tool.Result, error) {
	m.runs.Add(1)
	return tool.Result{}, tool.ErrTransient("disk busy")
}

// alwaysFailTool fails with a fixed code for loop detection.
type alwaysFailTool struct {
	runs *atomic.Int32
	name string
	code tool.ErrorCode
}

func (a *alwaysFailTool) Name() string {
	if a.name != "" {
		return a.name
	}
	return "failish"
}
func (a *alwaysFailTool) Description() string { return "always fail" }
func (a *alwaysFailTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"n":{"type":"string"}}}`)
}
func (a *alwaysFailTool) Contract() tool.Contract {
	return tool.Contract{
		Version:     tool.ContractVersion,
		SideEffect:  tool.SideEffectRead,
		Idempotency: tool.IdempotencySafeRetry,
	}
}
func (a *alwaysFailTool) Execute(_ context.Context, _ json.RawMessage, _ *tool.Context) (tool.Result, error) {
	a.runs.Add(1)
	code := a.code
	if code == "" {
		code = tool.CodeInternal
	}
	return tool.Result{}, &tool.CodedError{Code: code, Message: "nope", Retryable: false}
}

func drainTurn(t *testing.T, eng *engine.Engine, timeout time.Duration) (protocol.TurnCompleted, []protocol.Event) {
	t.Helper()
	var completed protocol.TurnCompleted
	var all []protocol.Event
	deadline := time.After(timeout)
	for completed.StopReason == "" {
		select {
		case ev := <-eng.Events():
			all = append(all, ev)
			switch e := ev.(type) {
			case protocol.TurnCompleted:
				completed = e
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: e.RequestID, Decision: protocol.DecisionOnce}
			case protocol.EngineError:
				// collect; may precede TurnCompleted
			}
		case <-deadline:
			t.Fatalf("timeout; events=%d last stop=%q", len(all), completed.StopReason)
		}
	}
	return completed, all
}

func TestToolRetriesTransientSafeRetry(t *testing.T) {
	fails := &atomic.Int32{}
	fails.Store(2) // fail twice, succeed on 3rd
	runs := &atomic.Int32{}
	flaky := &flakySafeTool{failsLeft: fails, runs: runs}

	call := provider.ToolCall{ID: "c1", Name: "flaky_read", Args: []byte(`{}`)}
	prov := &scriptedProvider{steps: []streamStep{
		{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &call},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "done"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	}}
	eng := engine.New(engine.Options{
		SessionID:            "session-tool-retry",
		Select:               func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:      "scripted",
		Registry:             tool.NewRegistry(flaky),
		WorkDir:              t.TempDir(),
		Rules:                []permission.Ruleset{permission.Defaults()},
		ToolRetryBackoff:     func(int) time.Duration { return 0 },
		MaxToolRetryAttempts: 3,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "retry please"}

	completed, events := drainTurn(t, eng, 5*time.Second)
	if completed.StopReason != "end_turn" {
		t.Fatalf("stopReason = %q", completed.StopReason)
	}
	if runs.Load() != 3 {
		t.Fatalf("executions = %d, want 3", runs.Load())
	}
	var retries int
	var end protocol.ToolCallEnd
	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.ToolRetrying:
			retries++
			if e.CallID != "c1" || e.Name != "flaky_read" || e.ErrorCode != protocol.ErrorCodeTransient {
				t.Fatalf("ToolRetrying = %#v", e)
			}
		case protocol.ToolCallEnd:
			end = e
		}
	}
	if retries != 2 {
		t.Fatalf("ToolRetrying events = %d, want 2", retries)
	}
	if end.IsError || end.Output != "recovered" {
		t.Fatalf("ToolCallEnd = %#v", end)
	}
}

func TestMutativeToolDoesNotAutoRetryTransient(t *testing.T) {
	runs := &atomic.Int32{}
	mut := &mutativeOnceTool{runs: runs}
	call := provider.ToolCall{ID: "c1", Name: "apply_patch", Args: []byte(`{"patch":"x"}`)}
	prov := &scriptedProvider{steps: []streamStep{
		{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &call},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	}}
	eng := engine.New(engine.Options{
		SessionID:            "session-no-double-apply",
		Select:               func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:      "scripted",
		Registry:             tool.NewRegistry(mut),
		WorkDir:              t.TempDir(),
		Rules:                []permission.Ruleset{permission.Defaults()},
		ToolRetryBackoff:     func(int) time.Duration { return 0 },
		MaxToolRetryAttempts: 5,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "patch"}

	completed, events := drainTurn(t, eng, 5*time.Second)
	if completed.StopReason != "end_turn" {
		t.Fatalf("stopReason = %q", completed.StopReason)
	}
	if runs.Load() != 1 {
		t.Fatalf("apply_patch executions = %d, want 1 (no double-apply)", runs.Load())
	}
	for _, ev := range events {
		if _, ok := ev.(protocol.ToolRetrying); ok {
			t.Fatal("mutative tool must not emit ToolRetrying")
		}
	}
	var end protocol.ToolCallEnd
	for _, ev := range events {
		if e, ok := ev.(protocol.ToolCallEnd); ok {
			end = e
		}
	}
	if !end.IsError || end.ErrorCode != protocol.ErrorCodeTransient {
		t.Fatalf("ToolCallEnd = %#v", end)
	}
	// Retryable must be false so the model is not told to blind-retry.
	// (settled on provider.ToolResult via history — check end only has ErrorCode)
}

func TestToolLoopDetectorStopsTurn(t *testing.T) {
	runs := &atomic.Int32{}
	fail := &alwaysFailTool{runs: runs, name: "failish", code: tool.CodeInternal}
	// Model issues the same failing tool 3 times across tool rounds.
	args := []byte(`{"n":"same"}`)
	call := func(id string) provider.ToolCall {
		return provider.ToolCall{ID: id, Name: "failish", Args: args}
	}
	prov := &scriptedProvider{steps: []streamStep{
		{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: ptrCall(call("c1"))},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: ptrCall(call("c2"))},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: ptrCall(call("c3"))},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		// Should not be reached if loop stops the turn.
		{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "should not run"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	}}
	eng := engine.New(engine.Options{
		SessionID:         "session-loop",
		Select:            func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:   "scripted",
		Registry:          tool.NewRegistry(fail),
		WorkDir:           t.TempDir(),
		Rules:             []permission.Ruleset{permission.Defaults()},
		ToolLoopThreshold: 3,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "loop"}

	completed, events := drainTurn(t, eng, 5*time.Second)
	if completed.StopReason != "loop_detected" {
		t.Fatalf("stopReason = %q, want loop_detected", completed.StopReason)
	}
	var sawLoop bool
	for _, ev := range events {
		if _, ok := ev.(protocol.ToolLoopDetected); ok {
			sawLoop = true
		}
	}
	if !sawLoop {
		t.Fatal("expected ToolLoopDetected event")
	}
	// Third identical failure trips; Execute ran 3 times (or 2 + short-circuit).
	if runs.Load() < 2 || runs.Load() > 3 {
		t.Fatalf("executions = %d, want 2 or 3", runs.Load())
	}
}

func ptrCall(c provider.ToolCall) *provider.ToolCall { return &c }

func TestPreconditionRecoverHintOnConditional(t *testing.T) {
	runs := &atomic.Int32{}
	edit := &preconditionEdit{runs: runs}
	call := provider.ToolCall{ID: "c1", Name: "edit", Args: []byte(`{"path":"a.go"}`)}
	prov := &scriptedProvider{steps: []streamStep{
		{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &call},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "replan"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	}}
	eng := engine.New(engine.Options{
		SessionID:            "session-recover",
		Select:               func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:      "scripted",
		Registry:             tool.NewRegistry(edit),
		WorkDir:              t.TempDir(),
		Rules:                []permission.Ruleset{permission.Defaults()},
		MaxToolRetryAttempts: 3,
		ToolRetryBackoff:     func(int) time.Duration { return 0 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "edit"}

	_, events := drainTurn(t, eng, 5*time.Second)
	if runs.Load() != 1 {
		t.Fatalf("executions = %d, want 1 (recover, not retry)", runs.Load())
	}
	var end protocol.ToolCallEnd
	for _, ev := range events {
		if e, ok := ev.(protocol.ToolCallEnd); ok {
			end = e
		}
		if _, ok := ev.(protocol.ToolRetrying); ok {
			t.Fatal("precondition_failed must not auto-retry")
		}
	}
	if !end.IsError || end.ErrorCode != protocol.ErrorCodePreconditionFailed {
		t.Fatalf("end = %#v", end)
	}
	if !contains(end.Output, "[recovery:") {
		t.Fatalf("missing recovery hint: %q", end.Output)
	}
}

type preconditionEdit struct{ runs *atomic.Int32 }

func (p *preconditionEdit) Name() string        { return "edit" }
func (p *preconditionEdit) Description() string { return "edit" }
func (p *preconditionEdit) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (p *preconditionEdit) Contract() tool.Contract {
	return tool.LookupContract(tool.NewEdit())
}
func (p *preconditionEdit) Execute(context.Context, json.RawMessage, *tool.Context) (tool.Result, error) {
	p.runs.Add(1)
	return tool.Result{}, tool.ErrPrecondition("oldString not found")
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
