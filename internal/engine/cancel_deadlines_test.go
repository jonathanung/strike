package engine_test

import (
	"context"
	"encoding/json"
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

// partialCancelTool emits partial output then blocks until ctx cancel, returning
// the partial bytes with ErrorCodeCanceled (mirrors bash cancel path).
type partialCancelTool struct {
	started chan struct{}
}

func (t *partialCancelTool) Name() string            { return "partial" }
func (t *partialCancelTool) Description() string     { return "partial cancel tool" }
func (t *partialCancelTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *partialCancelTool) Execute(ctx context.Context, _ json.RawMessage, tc *tool.Context) (tool.Result, error) {
	const chunk = "partial-bytes-before-cancel"
	if tc != nil && tc.ReportOutput != nil {
		tc.ReportOutput(chunk)
	}
	if t.started != nil {
		select {
		case <-t.started:
		default:
			close(t.started)
		}
	}
	<-ctx.Done()
	return tool.Result{
		Title:     "partial",
		Output:    chunk,
		ErrorCode: tool.ErrorCodeCanceled,
		Metadata:  json.RawMessage(`{"incomplete":true}`),
	}, nil
}

// timeoutTool returns a timeout-coded result without needing a real deadline.
type timeoutTool struct{}

func (timeoutTool) Name() string            { return "timeoutish" }
func (timeoutTool) Description() string     { return "timeout tool" }
func (timeoutTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (timeoutTool) Execute(context.Context, json.RawMessage, *tool.Context) (tool.Result, error) {
	return tool.Result{
		Title:     "timeoutish",
		Output:    "got some\n(command timed out after 50ms)\n(exit code -1)",
		ErrorCode: tool.ErrorCodeTimeout,
		Metadata:  json.RawMessage(`{"incomplete":true,"errorCode":"timeout"}`),
	}, nil
}

func TestInterruptPreservesPartialToolOutputAndCanceledCode(t *testing.T) {
	call := toolCall("p1", "partial")
	pt := &partialCancelTool{started: make(chan struct{})}
	prov := newScriptedProvider(toolCallStep(call), completedStep("after"))
	eng := newTestEngine(t, prov, pt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "partial cancel"}
	_ = receiveRequest(t, prov.requests)

	select {
	case <-pt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool never started")
	}
	// Let ReportOutput land.
	time.Sleep(20 * time.Millisecond)
	eng.Ops() <- protocol.Interrupt{}

	completed, events := collectThroughTurnCompleted(t, eng.Events())
	if completed.StopReason != "interrupted" {
		t.Fatalf("stopReason = %q, want interrupted", completed.StopReason)
	}
	var end protocol.ToolCallEnd
	found := false
	for _, ev := range events {
		if e, ok := ev.(protocol.ToolCallEnd); ok && e.CallID == "p1" {
			end = e
			found = true
		}
	}
	if !found {
		t.Fatal("missing ToolCallEnd")
	}
	if !end.IsError {
		t.Fatal("want IsError")
	}
	if end.ErrorCode != protocol.ErrorCodeCanceled {
		t.Fatalf("ErrorCode = %q, want %q", end.ErrorCode, protocol.ErrorCodeCanceled)
	}
	if !strings.Contains(end.Output, "partial-bytes-before-cancel") {
		t.Fatalf("output missing partial bytes: %q", end.Output)
	}
	if !strings.Contains(end.Output, "incomplete") {
		t.Fatalf("output missing incomplete marker: %q", end.Output)
	}

	// Model history must also keep partial + cancel marker.
	eng.Ops() <- protocol.UserInput{Text: "next"}
	req := receiveRequest(t, prov.requests)
	var toolOut string
	for _, m := range req.Messages {
		if m.Role == provider.RoleTool && m.ToolResult != nil && m.ToolResult.CallID == "p1" {
			toolOut = m.ToolResult.Output
		}
	}
	if !strings.Contains(toolOut, "partial-bytes-before-cancel") {
		t.Fatalf("model tool result missing partial: %q", toolOut)
	}
}

func TestToolTimeoutProducesTimeoutCode(t *testing.T) {
	call := toolCall("t1", "timeoutish")
	prov := newScriptedProvider(toolCallStep(call), completedStep("done"))
	eng := newTestEngine(t, prov, timeoutTool{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "timeout tool"}
	_ = receiveRequest(t, prov.requests)
	completed, events := collectThroughTurnCompleted(t, eng.Events())
	if completed.StopReason != "end_turn" {
		// Tool timeout is not a turn interrupt; model continues.
		t.Fatalf("stopReason = %q, want end_turn", completed.StopReason)
	}
	var end protocol.ToolCallEnd
	for _, ev := range events {
		if e, ok := ev.(protocol.ToolCallEnd); ok && e.CallID == "t1" {
			end = e
		}
	}
	if end.CallID == "" {
		t.Fatal("missing ToolCallEnd")
	}
	if !end.IsError || end.ErrorCode != protocol.ErrorCodeTimeout {
		t.Fatalf("ToolCallEnd = %#v, want error timeout", end)
	}
	if !strings.Contains(end.Output, "timed out") {
		t.Fatalf("output = %q", end.Output)
	}
}

func TestTurnTimeoutStopReasonAndEngineError(t *testing.T) {
	// Slow stream that only ends on ctx cancel/deadline.
	prov := newScriptedProvider(streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
		ch := make(chan provider.StreamEvent)
		go func() {
			defer close(ch)
			ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: "working"}
			<-ctx.Done()
		}()
		return ch
	}})
	eng := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return prov, "test-model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		TurnTimeout:     40 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "turn deadline"}
	_ = receiveRequest(t, prov.requests)

	var (
		completed protocol.TurnCompleted
		sawErr    bool
	)
	guard := time.After(3 * time.Second)
	for completed.StopReason == "" {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("events closed")
			}
			switch e := ev.(type) {
			case protocol.EngineError:
				if e.Code == protocol.ErrorCodeTimeout {
					sawErr = true
				}
			case protocol.TurnCompleted:
				completed = e
			}
		case <-guard:
			t.Fatal("timeout waiting for turn completed")
		}
	}
	if completed.StopReason != "timeout" {
		t.Fatalf("stopReason = %q, want timeout", completed.StopReason)
	}
	if !sawErr {
		t.Fatal("expected EngineError with code timeout")
	}
}

func TestCancelDuringToolDispatchRace(t *testing.T) {
	// Repeated cancel-while-Execute must not corrupt history or hang.
	// Under go test -race this stresses engine emit/settle paths.
	const n = 6
	for i := 0; i < n; i++ {
		call := toolCall("race", "partial")
		pt := &partialCancelTool{started: make(chan struct{})}
		prov := newScriptedProvider(toolCallStep(call), completedStep("x"))
		eng := newTestEngine(t, prov, pt)
		ctx, cancel := context.WithCancel(context.Background())
		go eng.Run(ctx)
		eng.Ops() <- protocol.UserInput{Text: "race"}
		_ = receiveRequest(t, prov.requests)
		select {
		case <-pt.started:
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatal("tool never started")
		}
		// Interrupt concurrently with a second goroutine still draining events
		// via collectThroughTurnCompleted.
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(i) * time.Millisecond)
			eng.Ops() <- protocol.Interrupt{}
		}()
		completed, events := collectThroughTurnCompleted(t, eng.Events())
		wg.Wait()
		if completed.StopReason != "interrupted" {
			t.Fatalf("iter %d stopReason = %q", i, completed.StopReason)
		}
		var ends int
		for _, ev := range events {
			if end, ok := ev.(protocol.ToolCallEnd); ok {
				ends++
				if end.ErrorCode != protocol.ErrorCodeCanceled {
					t.Fatalf("iter %d ErrorCode = %q", i, end.ErrorCode)
				}
				if !strings.Contains(end.Output, "partial-bytes") {
					t.Fatalf("iter %d missing partial: %q", i, end.Output)
				}
			}
		}
		if ends != 1 {
			t.Fatalf("iter %d ToolCallEnd count = %d, want 1", i, ends)
		}
		cancel()
	}
}

func TestProviderStreamCancelEndsInterrupted(t *testing.T) {
	// Acceptance: provider stream cancel ends the turn (interrupted); no
	// dangling tool codes when no tool started.
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
	eng.Ops() <- protocol.UserInput{Text: "stream"}
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
