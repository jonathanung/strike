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

func TestTurnTimeoutDuringToolPropagatesTimeoutCode(t *testing.T) {
	// Tool blocks until ctx deadline; turn timeout must settle tool as timeout
	// (not canceled) and complete the turn with stopReason timeout.
	call := toolCall("slow1", "partial")
	pt := &partialCancelTool{started: make(chan struct{})}
	prov := newScriptedProvider(toolCallStep(call), completedStep("after-timeout"))
	eng := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return prov, "test-model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(pt),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		TurnTimeout:     500 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "slow tool under turn deadline"}
	_ = receiveRequest(t, prov.requests)

	startGuard := time.NewTimer(3 * time.Second)
	defer startGuard.Stop()
waitForTool:
	for {
		select {
		case <-pt.started:
			break waitForTool
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("events closed before tool started")
			}
			switch e := ev.(type) {
			case protocol.EngineError:
				t.Fatalf("engine error before tool started: code=%q message=%q", e.Code, e.Message)
			case protocol.TurnCompleted:
				t.Fatalf("turn completed before tool started: stopReason=%q", e.StopReason)
			}
		case <-startGuard.C:
			t.Fatal("timeout waiting for tool to start")
		}
	}

	var (
		completed protocol.TurnCompleted
		sawErr    bool
		end       protocol.ToolCallEnd
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
			case protocol.ToolCallEnd:
				if e.CallID == "slow1" {
					end = e
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
		t.Fatal("expected EngineError timeout")
	}
	if end.CallID == "" {
		t.Fatal("missing ToolCallEnd")
	}
	if end.ErrorCode != protocol.ErrorCodeTimeout {
		t.Fatalf("tool ErrorCode = %q, want timeout (not canceled)", end.ErrorCode)
	}
	if !strings.Contains(end.Output, "partial-bytes-before-cancel") {
		t.Fatalf("partial output lost: %q", end.Output)
	}
}

func TestTurnTimeoutResumeAppliesConfiguredPostureNotExpiredDeadline(t *testing.T) {
	// After a timed-out turn, a resumed engine with the same TurnTimeout must
	// still apply a fresh bound on the next turn (not "already expired").
	// First engine: short timeout fires.
	prov1 := newScriptedProvider(streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
		ch := make(chan provider.StreamEvent)
		go func() {
			defer close(ch)
			ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: "hang"}
			<-ctx.Done()
		}()
		return ch
	}})
	eng1 := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return prov1, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		TurnTimeout:     40 * time.Millisecond,
	})
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	go eng1.Run(ctx1)
	eng1.Ops() <- protocol.UserInput{Text: "first"}
	_ = receiveRequest(t, prov1.requests)
	completed, events := collectThroughTurnCompleted(t, eng1.Events())
	if completed.StopReason != "timeout" {
		t.Fatalf("first stopReason = %q", completed.StopReason)
	}
	// Capture events for Restore (include user message from live path).
	_ = events

	// Second engine: same configured timeout, seeded history, QuietStartup.
	// A fast-completing stream must finish normally — proving the deadline is
	// fresh, not inherited as already-expired from the prior process.
	prov2 := newScriptedProvider(completedStep("ok after resume"))
	restored := engine.Restore([]protocol.Event{
		protocol.UserMessage{Correlation: protocol.Correlation{SessionID: "s"}, Text: "first"},
		protocol.TextDelta{Correlation: protocol.Correlation{SessionID: "s"}, Text: "hang"},
		protocol.TurnCompleted{Correlation: protocol.Correlation{SessionID: "s"}, StopReason: "timeout"},
		protocol.EngineError{Correlation: protocol.Correlation{SessionID: "s"}, Code: protocol.ErrorCodeTimeout, Message: "turn deadline exceeded"},
	})
	eng2 := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return prov2, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		TurnTimeout:     5 * time.Second, // configured posture (generous)
		InitialMessages: restored.Messages,
		QuietStartup:    true,
	})
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go eng2.Run(ctx2)
	// Allow quiet startup to settle.
	time.Sleep(20 * time.Millisecond)
	eng2.Ops() <- protocol.UserInput{Text: "second after resume"}
	_ = receiveRequest(t, prov2.requests)
	completed2, _ := collectThroughTurnCompleted(t, eng2.Events())
	if completed2.StopReason != "end_turn" {
		t.Fatalf("resume turn stopReason = %q, want end_turn (fresh deadline, not expired)", completed2.StopReason)
	}
}

func TestTurnTimeoutDistinguishedFromInterrupt(t *testing.T) {
	// Interrupt → interrupted; deadline → timeout. Same slow stream shape.
	newSlow := func() (*scriptedProvider, *engine.Engine) {
		prov := newScriptedProvider(streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
			ch := make(chan provider.StreamEvent)
			go func() {
				defer close(ch)
				ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: "x"}
				<-ctx.Done()
			}()
			return ch
		}})
		eng := engine.New(engine.Options{
			Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
			InitialProvider: "scripted",
			Registry:        tool.NewRegistry(),
			WorkDir:         t.TempDir(),
			Rules:           []permission.Ruleset{permission.Defaults()},
			// Long timeout so interrupt wins on the interrupt path.
			TurnTimeout: 30 * time.Second,
		})
		return prov, eng
	}

	// Interrupt path.
	provI, engI := newSlow()
	ctxI, cancelI := context.WithCancel(context.Background())
	defer cancelI()
	go engI.Run(ctxI)
	engI.Ops() <- protocol.UserInput{Text: "interrupt me"}
	_ = receiveRequest(t, provI.requests)
	engI.Ops() <- protocol.Interrupt{}
	cI, _ := collectThroughTurnCompleted(t, engI.Events())
	if cI.StopReason != "interrupted" {
		t.Fatalf("interrupt stopReason = %q", cI.StopReason)
	}

	// Timeout path (short deadline).
	provT := newScriptedProvider(streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
		ch := make(chan provider.StreamEvent)
		go func() {
			defer close(ch)
			ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: "x"}
			<-ctx.Done()
		}()
		return ch
	}})
	engT := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return provT, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		TurnTimeout:     40 * time.Millisecond,
	})
	ctxT, cancelT := context.WithCancel(context.Background())
	defer cancelT()
	go engT.Run(ctxT)
	engT.Ops() <- protocol.UserInput{Text: "timeout me"}
	_ = receiveRequest(t, provT.requests)
	cT, events := collectThroughTurnCompleted(t, engT.Events())
	if cT.StopReason != "timeout" {
		t.Fatalf("timeout stopReason = %q", cT.StopReason)
	}
	var sawTimeoutErr bool
	for _, ev := range events {
		if e, ok := ev.(protocol.EngineError); ok && e.Code == protocol.ErrorCodeTimeout {
			sawTimeoutErr = true
		}
	}
	if !sawTimeoutErr {
		t.Fatal("timeout path missing EngineError timeout")
	}
}
