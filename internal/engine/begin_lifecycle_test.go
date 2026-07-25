package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

type lifecycleTool struct {
	executed chan struct{}
}

func (t *lifecycleTool) Name() string            { return "lifecycle" }
func (t *lifecycleTool) Description() string     { return "test lifecycle tool" }
func (t *lifecycleTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *lifecycleTool) Execute(context.Context, json.RawMessage, *tool.Context) (tool.Result, error) {
	t.executed <- struct{}{}
	return tool.Result{Output: "executed"}, nil
}

func TestAcceptedBlockedBeginInterruptedEmitsMatchedBoundaryWithoutExecute(t *testing.T) {
	lt := &lifecycleTool{executed: make(chan struct{}, 1)}
	eng := New(Options{Registry: tool.NewRegistry(lt), WorkDir: t.TempDir()})
	for range cap(eng.events) {
		eng.events <- protocol.EngineError{Message: "backpressure"}
	}

	ctx, cancel := context.WithCancel(context.Background())
	eng.turnCancel = cancel
	call := provider.ToolCall{ID: "accepted", Name: lt.Name(), Args: json.RawMessage(`{}`)}
	corr := protocol.Correlation{SessionID: "session", TurnID: "turn", ProviderRequestID: "request"}
	result := make(chan provider.Message, 1)
	go func() {
		result <- eng.execToolCall(ctx, call, corr)
	}()

	var req beginReq
	select {
	case req = <-eng.beginReqs:
		// Receiving the unbuffered request proves the begin handoff was accepted.
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not accept the blocked begin request")
	}
	workerResult := req.result
	ackResult := make(chan beginAck, 1)
	req.result = ackResult
	serveDone := make(chan bool, 1)
	go func() {
		serveDone <- eng.serveBeginReq(req)
	}()

	eng.ops <- protocol.Interrupt{}
	events := []protocol.Event{<-eng.events}
	var ack beginAck
	select {
	case ack = <-ackResult:
	case <-time.After(2 * time.Second):
		t.Fatal("serveBeginReq did not acknowledge the accepted begin")
	}
	if !ack.emitted {
		t.Error("accepted normal Interrupt acknowledged emitted=false; want emitted=true")
	}
	workerResult <- ack

	var message provider.Message
	guard := time.NewTimer(2 * time.Second)
	defer guard.Stop()
	collecting := true
	for collecting {
		select {
		case ev := <-eng.events:
			events = append(events, ev)
		case message = <-result:
			collecting = false
		case <-guard.C:
			t.Fatal("timed out waiting for execToolCall to return")
		}
	}
	for {
		select {
		case ev := <-eng.events:
			events = append(events, ev)
		default:
			goto complete
		}
	}

complete:
	select {
	case open := <-serveDone:
		if !open {
			t.Error("normal Interrupt was treated as Ops shutdown")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serveBeginReq did not return")
	}

	begins, ends := lifecycleBoundaries(events)
	if len(begins) != 1 || len(ends) != 1 {
		t.Fatalf("tool boundary counts = begin %d, end %d; want exactly one each", len(begins), len(ends))
	}
	if begins[0].CallID != call.ID || ends[0].CallID != call.ID {
		t.Errorf("tool boundary call IDs = %q/%q, want %q/%q", begins[0].CallID, ends[0].CallID, call.ID, call.ID)
	}
	if begins[0].Correlation != corr || ends[0].Correlation != corr {
		t.Errorf("tool boundary correlations = %#v/%#v, want %#v", begins[0].Correlation, ends[0].Correlation, corr)
	}
	if ends[0].Output != canceledToolOutput || !ends[0].IsError {
		t.Errorf("ToolCallEnd = output %q IsError=%v, want %q IsError=true", ends[0].Output, ends[0].IsError, canceledToolOutput)
	}
	assertLifecycleToolResult(t, message, call.ID, canceledToolOutput)
	select {
	case <-lt.executed:
		t.Error("Execute started after Interrupt of an accepted blocked begin")
	default:
	}
}

func TestCanceledBeforeBeginHandoffUsesHistoryOnlyUnstartedResult(t *testing.T) {
	lt := &lifecycleTool{executed: make(chan struct{}, 1)}
	eng := New(Options{Registry: tool.NewRegistry(lt), WorkDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	call := provider.ToolCall{ID: "unstarted", Name: lt.Name(), Args: json.RawMessage(`{}`)}
	result := make(chan provider.Message, 1)
	go func() {
		result <- eng.execToolCall(ctx, call, protocol.Correlation{SessionID: "session"})
	}()
	cancel()

	var message provider.Message
	select {
	case message = <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("execToolCall did not return after pre-handoff cancellation")
	}
	assertLifecycleToolResult(t, message, call.ID, unstartedToolOutput)
	select {
	case ev := <-eng.events:
		t.Fatalf("pre-handoff cancellation emitted unexpected event %T", ev)
	default:
	}
	select {
	case <-lt.executed:
		t.Error("pre-handoff canceled tool executed")
	default:
	}
}

func assertLifecycleToolResult(t *testing.T, message provider.Message, callID, output string) {
	t.Helper()
	if message.Role != provider.RoleTool || message.ToolResult == nil {
		t.Fatalf("result = %#v, want tool result", message)
	}
	if message.ToolResult.CallID != callID || message.ToolResult.Output != output || !message.ToolResult.IsError {
		t.Errorf("tool result = %#v, want call %q output %q IsError=true", message.ToolResult, callID, output)
	}
}

func lifecycleBoundaries(events []protocol.Event) ([]protocol.ToolCallBegin, []protocol.ToolCallEnd) {
	var begins []protocol.ToolCallBegin
	var ends []protocol.ToolCallEnd
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ToolCallBegin:
			begins = append(begins, ev)
		case protocol.ToolCallEnd:
			ends = append(ends, ev)
		}
	}
	return begins, ends
}
