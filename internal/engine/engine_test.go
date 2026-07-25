package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/provider/echo"
	"github.com/jonathanung/strike-cli/internal/tool"
)

type streamStep struct {
	events []provider.StreamEvent
	err    error
	stream func(context.Context) <-chan provider.StreamEvent
}

type scriptedProvider struct {
	mu       sync.Mutex
	steps    []streamStep
	calls    int
	requests chan provider.Request
}

func (p *scriptedProvider) Name() string { return "scripted" }

func newScriptedProvider(steps ...streamStep) *scriptedProvider {
	return &scriptedProvider{steps: steps, requests: make(chan provider.Request, len(steps)+8)}
}

func (p *scriptedProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	if p.calls >= len(p.steps) {
		p.mu.Unlock()
		return nil, errors.New("unexpected Stream call")
	}
	step := p.steps[p.calls]
	p.calls++
	requests := p.requests
	p.mu.Unlock()
	if requests != nil {
		requests <- cloneProviderRequest(req)
	}
	if step.err != nil {
		return nil, step.err
	}
	if step.stream != nil {
		return step.stream(ctx), nil
	}
	ch := make(chan provider.StreamEvent, len(step.events))
	for _, ev := range step.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func (p *scriptedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func cloneProviderRequest(req provider.Request) provider.Request {
	out := req
	out.Messages = make([]provider.Message, len(req.Messages))
	for i, message := range req.Messages {
		out.Messages[i] = message
		out.Messages[i].ToolCalls = append([]provider.ToolCall(nil), message.ToolCalls...)
		for j := range out.Messages[i].ToolCalls {
			out.Messages[i].ToolCalls[j].Args = append(json.RawMessage(nil), message.ToolCalls[j].Args...)
		}
		if message.ToolResult != nil {
			result := *message.ToolResult
			out.Messages[i].ToolResult = &result
		}
	}
	out.Tools = append([]provider.ToolSchema(nil), req.Tools...)
	for i := range out.Tools {
		out.Tools[i].InputSchema = append(json.RawMessage(nil), req.Tools[i].InputSchema...)
	}
	return out
}

func selectEcho(string) (provider.Provider, string, error) {
	return echo.New(), "echo", nil
}

// TestFullLoop drives user input -> tool call -> permission ask -> approval
// -> tool result -> final message through the real engine with the echo
// provider, asserting the event sequence a frontend would see.
func TestFullLoop(t *testing.T) {
	eng := engine.New(engine.Options{
		Select:          selectEcho,
		InitialProvider: "echo",
		Registry:        tool.NewRegistry(tool.NewBash()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "run echo hello-strike"}

	var sawAsk, sawToolEnd, sawCompleted bool
	var toolOutput string
	deadline := time.After(10 * time.Second)
	for !sawCompleted {
		select {
		case <-deadline:
			t.Fatalf("timed out; ask=%v toolEnd=%v", sawAsk, sawToolEnd)
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				sawAsk = true
				if ev.Permission != "bash" {
					t.Errorf("permission = %q, want bash", ev.Permission)
				}
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.ToolCallEnd:
				sawToolEnd = true
				toolOutput = ev.Output
				if ev.IsError {
					t.Errorf("tool call failed: %s", ev.Output)
				}
			case protocol.TurnCompleted:
				sawCompleted = true
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
	if !sawAsk {
		t.Error("no PermissionAsked event")
	}
	if !sawToolEnd {
		t.Error("no ToolCallEnd event")
	}
	if !strings.Contains(toolOutput, "hello-strike") {
		t.Errorf("tool output %q does not contain command output", toolOutput)
	}
}

// TestNoModelSelected verifies input without a selected provider produces
// an EngineError, then works after a SelectModel op.
func TestNoModelSelected(t *testing.T) {
	eng := engine.New(engine.Options{
		Select:   selectEcho,
		Registry: tool.NewRegistry(),
		WorkDir:  t.TempDir(),
		Rules:    []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hello"}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for EngineError")
		case ev := <-eng.Events():
			if err, ok := ev.(protocol.EngineError); ok {
				if !strings.Contains(err.Message, "no model selected") {
					t.Errorf("error = %q, want no-model-selected", err.Message)
				}
				goto selected
			}
		}
	}
selected:
	eng.Ops() <- protocol.SelectModel{Provider: "echo"}
	eng.Ops() <- protocol.UserInput{Text: "hello again"}
	sawSelected := false
	for {
		select {
		case <-deadline:
			t.Fatal("timed out after SelectModel")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.ModelSelected:
				sawSelected = true
				if ev.Provider != "echo" || ev.Model != "echo" {
					t.Errorf("selected = %s/%s, want echo/echo", ev.Provider, ev.Model)
				}
			case protocol.EngineError:
				t.Fatalf("engine error after select: %s", ev.Message)
			case protocol.TurnCompleted:
				if !sawSelected {
					t.Error("no ModelSelected event")
				}
				return
			}
		}
	}
}

// TestRejectionFeedsBackToModel verifies a rejected permission becomes a
// correctable tool-result error, not a turn abort.
func TestRejectionFeedsBackToModel(t *testing.T) {
	eng := engine.New(engine.Options{
		Select:          selectEcho,
		InitialProvider: "echo",
		Registry:        tool.NewRegistry(tool.NewBash()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "run rm -rf /"}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{
					RequestID: ev.RequestID,
					Decision:  protocol.DecisionReject,
					Message:   "do not delete anything",
				}
			case protocol.ToolCallEnd:
				if !ev.IsError {
					t.Error("rejected call should be an error result")
				}
				if !strings.Contains(ev.Output, "do not delete anything") {
					t.Errorf("rejection feedback missing from output: %q", ev.Output)
				}
			case protocol.TurnCompleted:
				return
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
}

func TestRuntimeCorrelationAcrossTwoStreamToolLoop(t *testing.T) {
	const sessionID = "session-fixed"
	call := provider.ToolCall{ID: "call-1", Name: "bash", Args: json.RawMessage(`{"command":"printf correlated"}`)}
	prov := &scriptedProvider{steps: []streamStep{
		{events: []provider.StreamEvent{{Type: provider.EventToolCall, ToolCall: &call}, {Type: provider.EventDone, StopReason: "tool_use"}}},
		{events: []provider.StreamEvent{{Type: provider.EventTextDelta, Text: "done"}, {Type: provider.EventDone, StopReason: "end_turn"}}},
	}}
	eng := engine.New(engine.Options{
		SessionID:       sessionID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "scripted-model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewBash()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "use a tool"}

	var events []protocol.Event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out after events: %#v", events)
		case ev := <-eng.Events():
			events = append(events, ev)
			if asked, ok := ev.(protocol.PermissionAsked); ok {
				eng.Ops() <- protocol.PermissionReply{RequestID: asked.RequestID, Decision: protocol.DecisionOnce}
			}
			if _, ok := ev.(protocol.TurnCompleted); ok {
				goto complete
			}
		}
	}

complete:
	if calls := prov.callCount(); calls != 2 {
		t.Fatalf("Provider.Stream calls = %d, want 2", calls)
	}
	var turnID, firstProviderID, secondProviderID, permissionID string
	counts := map[string]int{}
	for _, ev := range events {
		corr := eventCorrelation(t, ev)
		if corr.SessionID != sessionID {
			t.Errorf("%T sessionId = %q, want %q", ev, corr.SessionID, sessionID)
		}
		switch ev := ev.(type) {
		case protocol.ModelSelected:
			counts["model"]++
			assertCorrelationFields(t, ev, corr, false, false)
		case protocol.AgentSelected:
			counts["agent"]++
			assertCorrelationFields(t, ev, corr, false, false)
		case protocol.UserMessage:
			counts["user"]++
			turnID = corr.TurnID
			assertCorrelationFields(t, ev, corr, true, false)
		case protocol.TurnStarted:
			counts["started"]++
			assertCorrelationFields(t, ev, corr, true, false)
		case protocol.ToolCallBegin:
			counts["begin"]++
			firstProviderID = corr.ProviderRequestID
			assertCorrelationFields(t, ev, corr, true, true)
		case protocol.PermissionAsked:
			counts["asked"]++
			permissionID = ev.RequestID
			assertCorrelationFields(t, ev, corr, true, true)
		case protocol.PermissionResolved:
			counts["resolved"]++
			if ev.RequestID != permissionID {
				t.Errorf("PermissionResolved requestId = %q, want %q", ev.RequestID, permissionID)
			}
			assertCorrelationFields(t, ev, corr, true, true)
		case protocol.ToolCallEnd:
			counts["end"]++
			assertCorrelationFields(t, ev, corr, true, true)
		case protocol.TextDelta:
			counts["text"]++
			secondProviderID = corr.ProviderRequestID
			assertCorrelationFields(t, ev, corr, true, true)
		case protocol.TurnCompleted:
			counts["completed"]++
			assertCorrelationFields(t, ev, corr, true, true)
		case protocol.EngineError:
			t.Fatalf("unexpected EngineError: %s", ev.Message)
		default:
			t.Fatalf("unexpected event %T", ev)
		}
		if corr.TurnID != "" && turnID != "" && corr.TurnID != turnID {
			t.Errorf("%T turnId = %q, want stable %q", ev, corr.TurnID, turnID)
		}
	}
	for _, name := range []string{"model", "agent", "user", "started", "begin", "asked", "resolved", "end", "text", "completed"} {
		if counts[name] != 1 {
			t.Errorf("%s event count = %d, want 1", name, counts[name])
		}
	}
	if turnID == "" {
		t.Error("turnId is empty")
	}
	if firstProviderID == "" || secondProviderID == "" || firstProviderID == secondProviderID {
		t.Errorf("provider request IDs = %q, %q; want two distinct nonempty IDs", firstProviderID, secondProviderID)
	}
	if permissionID == "" || permissionID == firstProviderID || permissionID == secondProviderID {
		t.Errorf("permission requestId = %q, provider request IDs = %q, %q; want distinct", permissionID, firstProviderID, secondProviderID)
	}
	for _, ev := range events {
		corr := eventCorrelation(t, ev)
		switch ev.(type) {
		case protocol.ToolCallBegin, protocol.ToolCallEnd, protocol.PermissionAsked, protocol.PermissionResolved:
			if corr.ProviderRequestID != firstProviderID {
				t.Errorf("%T providerRequestId = %q, want first call %q", ev, corr.ProviderRequestID, firstProviderID)
			}
		case protocol.TextDelta, protocol.TurnCompleted:
			if corr.ProviderRequestID != secondProviderID {
				t.Errorf("%T providerRequestId = %q, want second call %q", ev, corr.ProviderRequestID, secondProviderID)
			}
		}
	}
}

func TestProviderFailuresCarryRuntimeCorrelation(t *testing.T) {
	tests := []struct {
		name string
		step streamStep
	}{
		{name: "synchronous Stream error", step: streamStep{err: errors.New("sync stream failed")}},
		{name: "streamed EventError", step: streamStep{events: []provider.StreamEvent{{Type: provider.EventError, Err: errors.New("stream event failed")}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const sessionID = "session-failure"
			prov := &scriptedProvider{steps: []streamStep{tt.step}}
			eng := engine.New(engine.Options{
				SessionID:       sessionID,
				Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
				InitialProvider: "scripted",
				Registry:        tool.NewRegistry(),
				WorkDir:         t.TempDir(),
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go eng.Run(ctx)
			eng.Ops() <- protocol.UserInput{Text: "fail"}

			var failure protocol.EngineError
			var completed protocol.TurnCompleted
			deadline := time.After(2 * time.Second)
			for completed.StopReason == "" {
				select {
				case ev := <-eng.Events():
					switch ev := ev.(type) {
					case protocol.EngineError:
						failure = ev
					case protocol.TurnCompleted:
						completed = ev
					}
				case <-deadline:
					t.Fatal("timed out waiting for failed turn")
				}
			}
			if failure.Message == "" {
				t.Fatal("no EngineError emitted")
			}
			if failure.SessionID != sessionID || failure.TurnID == "" || failure.ProviderRequestID == "" {
				t.Errorf("EngineError correlation = %#v", failure.Correlation)
			}
			if completed.Correlation != failure.Correlation {
				t.Errorf("TurnCompleted correlation = %#v, want failure %#v", completed.Correlation, failure.Correlation)
			}
			if completed.StopReason != "error" {
				t.Errorf("stop reason = %q, want error", completed.StopReason)
			}
		})
	}
}

func TestRejectedOperationHasSessionOnlyCorrelation(t *testing.T) {
	const sessionID = "session-rejected"
	eng := engine.New(engine.Options{
		SessionID: sessionID,
		Select:    selectEcho,
		Registry:  tool.NewRegistry(),
		WorkDir:   t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "no provider yet"}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.AgentSelected:
				assertCorrelationFields(t, ev, ev.Correlation, false, false)
			case protocol.EngineError:
				if !strings.Contains(ev.Message, "no model selected") {
					t.Fatalf("EngineError = %q", ev.Message)
				}
				if ev.SessionID != sessionID || ev.TurnID != "" || ev.ProviderRequestID != "" {
					t.Errorf("rejection correlation = %#v, want session only", ev.Correlation)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for rejected operation")
		}
	}
}

const (
	canceledStartedOutput = "Tool call canceled because the turn was interrupted."
	unstartedOutput       = "Tool call not executed because the turn was interrupted before it started."
)

type channelTool struct {
	executed chan string
	blocks   map[string]<-chan struct{}
}

func (t *channelTool) Name() string            { return "channel" }
func (t *channelTool) Description() string     { return "test channel tool" }
func (t *channelTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *channelTool) Execute(ctx context.Context, args json.RawMessage, _ *tool.Context) (tool.Result, error) {
	var input struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return tool.Result{}, err
	}
	t.executed <- input.ID
	if release := t.blocks[input.ID]; release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}
	return tool.Result{Output: "completed " + input.ID}, nil
}

type permissionTool struct {
	executed chan string
}

func (t *permissionTool) Name() string            { return "permission" }
func (t *permissionTool) Description() string     { return "test permission tool" }
func (t *permissionTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *permissionTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	var input struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return tool.Result{}, err
	}
	t.executed <- input.ID
	if err := tc.Ask(ctx, tool.AskRequest{Permission: "permission", Patterns: []string{input.ID}}); err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Output: "completed " + input.ID}, nil
}

func toolCall(id, name string) provider.ToolCall {
	return provider.ToolCall{ID: id, Name: name, Args: json.RawMessage(fmt.Sprintf(`{"id":%q}`, id))}
}

func toolCallStep(calls ...provider.ToolCall) streamStep {
	events := make([]provider.StreamEvent, 0, len(calls)+1)
	for i := range calls {
		call := calls[i]
		events = append(events, provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &call})
	}
	events = append(events, provider.StreamEvent{Type: provider.EventDone, StopReason: "tool_use"})
	return streamStep{events: events}
}

func completedStep(text string) streamStep {
	return streamStep{events: []provider.StreamEvent{
		{Type: provider.EventTextDelta, Text: text},
		{Type: provider.EventDone, StopReason: "end_turn"},
	}}
}

func newTestEngine(t *testing.T, prov provider.Provider, tools ...tool.Tool) *engine.Engine {
	t.Helper()
	return engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return prov, "test-model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tools...),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
}

func receiveEvent(t *testing.T, events <-chan protocol.Event, match func(protocol.Event) bool) protocol.Event {
	t.Helper()
	guard := time.NewTimer(2 * time.Second)
	defer guard.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("Events closed before expected event")
			}
			if match(ev) {
				return ev
			}
		case <-guard.C:
			t.Fatal("timed out waiting for engine event")
		}
	}
}

func receiveRequest(t *testing.T, requests <-chan provider.Request) provider.Request {
	t.Helper()
	select {
	case req := <-requests:
		return req
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for provider request")
		return provider.Request{}
	}
}

func waitForTurnCompleted(t *testing.T, events <-chan protocol.Event) protocol.TurnCompleted {
	t.Helper()
	completed, _ := collectThroughTurnCompleted(t, events)
	return completed
}

func collectThroughTurnCompleted(t *testing.T, events <-chan protocol.Event) (protocol.TurnCompleted, []protocol.Event) {
	t.Helper()
	var collected []protocol.Event
	guard := time.NewTimer(2 * time.Second)
	defer guard.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("Events closed before TurnCompleted")
			}
			collected = append(collected, ev)
			if completed, ok := ev.(protocol.TurnCompleted); ok {
				return completed, collected
			}
		case <-guard.C:
			t.Fatal("timed out waiting for TurnCompleted")
		}
	}
}

func assertToolHistory(t *testing.T, req provider.Request, calls []provider.ToolCall, outputs []string) {
	t.Helper()
	if len(req.Messages) != 3+len(calls) {
		t.Fatalf("normalized history has %d messages, want %d: %#v", len(req.Messages), 3+len(calls), req.Messages)
	}
	if req.Messages[0].Role != provider.RoleUser || req.Messages[1].Role != provider.RoleAssistant || req.Messages[len(req.Messages)-1].Role != provider.RoleUser {
		t.Fatalf("normalized history roles are structurally invalid: %#v", req.Messages)
	}
	if !reflect.DeepEqual(req.Messages[1].ToolCalls, calls) {
		t.Errorf("assistant tool calls = %#v, want %#v", req.Messages[1].ToolCalls, calls)
	}
	for i, call := range calls {
		message := req.Messages[i+2]
		if message.Role != provider.RoleTool || message.ToolResult == nil {
			t.Fatalf("message %d = %#v, want tool result", i+2, message)
		}
		wantError := i > 0 || outputs[i] == canceledStartedOutput || outputs[i] == unstartedOutput
		if message.ToolResult.CallID != call.ID || message.ToolResult.Output != outputs[i] || message.ToolResult.IsError != wantError {
			t.Errorf("tool result %d = %#v, want call %q output %q IsError=%v", i, message.ToolResult, call.ID, outputs[i], wantError)
		}
	}
}

func assertToolEventIDs(t *testing.T, events []protocol.Event, wantBegin, wantEnd []string) {
	t.Helper()
	var begins, ends []string
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ToolCallBegin:
			begins = append(begins, ev.CallID)
		case protocol.ToolCallEnd:
			ends = append(ends, ev.CallID)
		}
	}
	if !reflect.DeepEqual(begins, wantBegin) {
		t.Errorf("ToolCallBegin IDs = %v, want %v", begins, wantBegin)
	}
	if !reflect.DeepEqual(ends, wantEnd) {
		t.Errorf("ToolCallEnd IDs = %v, want %v", ends, wantEnd)
	}
}

func toolBoundaryEvents(events []protocol.Event) ([]protocol.ToolCallBegin, []protocol.ToolCallEnd) {
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

func collectUntilEventsClose(t *testing.T, events <-chan protocol.Event) []protocol.Event {
	t.Helper()
	var collected []protocol.Event
	guard := time.NewTimer(2 * time.Second)
	defer guard.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return collected
			}
			collected = append(collected, ev)
		case <-guard.C:
			t.Fatal("timed out waiting for Events to close")
		}
	}
}

func TestInterruptDuringProviderStreamDoesNotRetainToolCalls(t *testing.T) {
	call := toolCall("streamed", "channel")
	delivered := make(chan struct{})
	prov := newScriptedProvider(
		streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
			ch := make(chan provider.StreamEvent)
			go func() {
				ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &call}
				close(delivered)
				<-ctx.Done()
				close(ch)
			}()
			return ch
		}},
		completedStep("next"),
	)
	eng := newTestEngine(t, prov, &channelTool{executed: make(chan string, 1)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "cancel streaming"}
	_ = receiveRequest(t, prov.requests)
	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("provider tool call was not consumed")
	}
	eng.Ops() <- protocol.Interrupt{}
	if completed := waitForTurnCompleted(t, eng.Events()); completed.StopReason != "interrupted" {
		t.Fatalf("stop reason = %q, want interrupted", completed.StopReason)
	}
	eng.Ops() <- protocol.UserInput{Text: "next"}
	req := receiveRequest(t, prov.requests)
	if len(req.Messages) != 2 || req.Messages[0].Role != provider.RoleUser || req.Messages[1].Role != provider.RoleUser {
		t.Fatalf("history after stream cancellation = %#v, want two user messages only", req.Messages)
	}
}

func TestInterruptWhileWaitingForPermissionRepairsRetainedToolCalls(t *testing.T) {
	calls := []provider.ToolCall{toolCall("current", "permission"), toolCall("suffix", "permission")}
	prov := newScriptedProvider(toolCallStep(calls...), completedStep("next"))
	pt := &permissionTool{executed: make(chan string, len(calls))}
	eng := newTestEngine(t, prov, pt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "permission turn"}
	_ = receiveRequest(t, prov.requests)
	_ = receiveEvent(t, eng.Events(), func(ev protocol.Event) bool { _, ok := ev.(protocol.PermissionAsked); return ok })
	eng.Ops() <- protocol.Interrupt{}
	completed, afterAsk := collectThroughTurnCompleted(t, eng.Events())
	if completed.StopReason != "interrupted" {
		t.Errorf("stop reason = %q, want interrupted", completed.StopReason)
	}
	assertToolEventIDs(t, afterAsk, nil, []string{"current"})
	eng.Ops() <- protocol.UserInput{Text: "next"}
	req := receiveRequest(t, prov.requests)
	assertToolHistory(t, req, calls, []string{canceledStartedOutput, unstartedOutput})
	select {
	case id := <-pt.executed:
		if id != "current" {
			t.Errorf("executed tool = %q, want current", id)
		}
	default:
		t.Error("started permission tool did not Execute")
	}
	select {
	case id := <-pt.executed:
		t.Errorf("unstarted suffix unexpectedly executed: %q", id)
	default:
	}
}

func TestInterruptDuringLaterToolRepairsUnstartedSuffix(t *testing.T) {
	calls := []provider.ToolCall{toolCall("prefix", "channel"), toolCall("current", "channel"), toolCall("suffix", "channel")}
	neverRelease := make(chan struct{})
	ct := &channelTool{executed: make(chan string, len(calls)), blocks: map[string]<-chan struct{}{"current": neverRelease}}
	prov := newScriptedProvider(toolCallStep(calls...), completedStep("next"))
	eng := newTestEngine(t, prov, ct)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "three tools"}
	_ = receiveRequest(t, prov.requests)
	for _, want := range []string{"prefix", "current"} {
		select {
		case got := <-ct.executed:
			if got != want {
				t.Fatalf("execution order = %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s Execute", want)
		}
	}
	eng.Ops() <- protocol.Interrupt{}
	completed, turnEvents := collectThroughTurnCompleted(t, eng.Events())
	if completed.StopReason != "interrupted" {
		t.Errorf("stop reason = %q, want interrupted", completed.StopReason)
	}
	assertToolEventIDs(t, turnEvents, []string{"prefix", "current"}, []string{"prefix", "current"})
	eng.Ops() <- protocol.UserInput{Text: "next"}
	req := receiveRequest(t, prov.requests)
	assertToolHistory(t, req, calls, []string{"completed prefix", canceledStartedOutput, unstartedOutput})
	select {
	case id := <-ct.executed:
		t.Errorf("unstarted suffix unexpectedly executed: %q", id)
	default:
	}
}

type closeAwareTool struct {
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	cleaned  chan struct{}
}

func (t *closeAwareTool) Name() string            { return "close-aware" }
func (t *closeAwareTool) Description() string     { return "test close-aware tool" }
func (t *closeAwareTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *closeAwareTool) Execute(ctx context.Context, _ json.RawMessage, _ *tool.Context) (tool.Result, error) {
	close(t.started)
	<-ctx.Done()
	close(t.canceled)
	<-t.release
	close(t.cleaned)
	return tool.Result{}, ctx.Err()
}

func TestClosedOpsAfterStartedToolCancelsJoinsAndClosesEvents(t *testing.T) {
	call := toolCall("close-after-start", "close-aware")
	prov := newScriptedProvider(toolCallStep(call))
	ct := &closeAwareTool{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
		cleaned:  make(chan struct{}),
	}
	eng := newTestEngine(t, prov, ct)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan struct{})
	go func() {
		eng.Run(ctx)
		close(runDone)
	}()
	eng.Ops() <- protocol.UserInput{Text: "close after begin"}
	_ = receiveRequest(t, prov.requests)

	var events []protocol.Event
	for {
		ev := receiveEvent(t, eng.Events(), func(protocol.Event) bool { return true })
		events = append(events, ev)
		if _, ok := ev.(protocol.ToolCallBegin); ok {
			break
		}
	}
	select {
	case <-ct.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not start after ToolCallBegin")
	}
	close(eng.Ops())
	select {
	case <-ct.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("closing Ops did not cancel Execute context")
	}
	select {
	case <-ct.cleaned:
		t.Fatal("Execute cleanup completed before the test released it")
	default:
	}
	select {
	case <-runDone:
		t.Fatal("Run returned before Execute cleanup completed")
	default:
	}
	close(ct.release)
	select {
	case <-ct.cleaned:
	case <-time.After(2 * time.Second):
		t.Fatal("Execute cleanup did not complete")
	}
	events = append(events, collectUntilEventsClose(t, eng.Events())...)
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Execute cleanup completed")
	}

	begins, ends := toolBoundaryEvents(events)
	if len(begins) != 1 || len(ends) != 1 {
		t.Fatalf("tool boundary counts = begin %d, end %d; want exactly one each", len(begins), len(ends))
	}
	if begins[0].CallID != call.ID || ends[0].CallID != call.ID {
		t.Errorf("tool boundary call IDs = %q/%q, want %q/%q", begins[0].CallID, ends[0].CallID, call.ID, call.ID)
	}
	if begins[0].Correlation != ends[0].Correlation {
		t.Errorf("tool boundary correlations differ: begin %#v, end %#v", begins[0].Correlation, ends[0].Correlation)
	}
	if ends[0].Output != canceledStartedOutput || !ends[0].IsError {
		t.Errorf("ToolCallEnd = output %q IsError=%v, want %q IsError=true", ends[0].Output, ends[0].IsError, canceledStartedOutput)
	}
}

func TestShutdownDropsBlockedBeginWithoutUnmatchedEnd(t *testing.T) {
	call := toolCall("shutdown-blocked", "channel")
	prov := newScriptedProvider(toolCallStep(call))
	ct := &channelTool{executed: make(chan string, 1)}
	eng := newTestEngine(t, prov, ct)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	for range 2 {
		_ = receiveEvent(t, eng.Events(), func(protocol.Event) bool { return true })
	}
	for range 254 {
		eng.Ops() <- protocol.SelectModel{Provider: "scripted"}
	}
	eng.Ops() <- protocol.UserInput{Text: "shutdown with blocked begin"}
	_ = receiveRequest(t, prov.requests)
	close(eng.Ops())

	events := collectUntilEventsClose(t, eng.Events())
	assertToolEventIDs(t, events, nil, nil)
	select {
	case id := <-ct.executed:
		t.Errorf("tool with abandoned begin unexpectedly executed: %q", id)
	default:
	}
}

type shutdownTool struct {
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	cleaned  chan struct{}
}

func (t *shutdownTool) Name() string            { return "shutdown" }
func (t *shutdownTool) Description() string     { return "test shutdown tool" }
func (t *shutdownTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *shutdownTool) Execute(ctx context.Context, _ json.RawMessage, _ *tool.Context) (tool.Result, error) {
	close(t.started)
	<-ctx.Done()
	close(t.canceled)
	<-t.release
	close(t.cleaned)
	runtime.Goexit()
	return tool.Result{}, nil
}

func TestRunShutdownCancelsAndJoinsActiveTurn(t *testing.T) {
	for _, test := range []struct {
		name     string
		shutdown func(context.CancelFunc, chan<- protocol.Op)
	}{
		{name: "parent canceled", shutdown: func(cancel context.CancelFunc, _ chan<- protocol.Op) { cancel() }},
		{name: "Ops closed", shutdown: func(_ context.CancelFunc, ops chan<- protocol.Op) { close(ops) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := toolCall("shutdown", "shutdown")
			prov := newScriptedProvider(toolCallStep(call))
			st := &shutdownTool{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}), cleaned: make(chan struct{})}
			eng := newTestEngine(t, prov, st)
			ctx, cancel := context.WithCancel(context.Background())
			eng.Ops() <- protocol.UserInput{Text: "shutdown"}
			eventsClosed := make(chan struct{})
			go func() {
				for range eng.Events() {
				}
				close(eventsClosed)
			}()
			runDone := make(chan struct{})
			go func() {
				eng.Run(ctx)
				close(runDone)
			}()
			select {
			case <-st.started:
			case <-time.After(2 * time.Second):
				t.Fatal("tool did not start")
			}
			test.shutdown(cancel, eng.Ops())
			select {
			case <-st.canceled:
			case <-time.After(500 * time.Millisecond):
				t.Error("shutdown did not cancel active tool")
				cancel()
				<-st.canceled
			}
			select {
			case <-runDone:
				t.Error("Run returned before active tool cleanup completed")
			case <-time.After(50 * time.Millisecond):
			}
			select {
			case <-eventsClosed:
				t.Error("Events closed before active tool cleanup completed")
			default:
			}
			close(st.release)
			select {
			case <-st.cleaned:
			case <-time.After(2 * time.Second):
				t.Fatal("tool cleanup did not complete")
			}
			select {
			case <-eventsClosed:
			case <-time.After(2 * time.Second):
				t.Error("Events did not close after active tool cleanup completed")
			}
			select {
			case <-runDone:
			case <-time.After(2 * time.Second):
				t.Error("Run did not return after active tool cleanup completed")
			}
		})
	}
}

func TestCompletedTurnStateIsReapedBeforeNextOp(t *testing.T) {
	prov := newScriptedProvider(completedStep("first"))
	eng := newTestEngine(t, prov)
	ctx, cancel := context.WithCancel(context.Background())
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "first"}
	_ = receiveRequest(t, prov.requests)
	_ = waitForTurnCompleted(t, eng.Events())
	eng.Ops() <- protocol.SelectModel{Provider: "scripted"}
	_ = receiveEvent(t, eng.Events(), func(ev protocol.Event) bool { _, ok := ev.(protocol.ModelSelected); return ok })
	cancel()
	for range eng.Events() {
	}
	value := reflect.ValueOf(eng).Elem()
	if !value.FieldByName("turnDone").IsNil() || !value.FieldByName("turnCancel").IsNil() {
		t.Error("completed turn lifecycle remains retained after a later op")
	}
}

func TestLateInterruptAfterReapDoesNotAffectNextTurn(t *testing.T) {
	releaseSecond := make(chan struct{})
	secondCanceled := make(chan struct{}, 1)
	prov := newScriptedProvider(completedStep("first"), streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
		ch := make(chan provider.StreamEvent, 1)
		go func() {
			select {
			case <-ctx.Done():
				secondCanceled <- struct{}{}
			case <-releaseSecond:
				ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
			}
			close(ch)
		}()
		return ch
	}})
	eng := newTestEngine(t, prov)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "first"}
	_ = receiveRequest(t, prov.requests)
	_ = waitForTurnCompleted(t, eng.Events())
	eng.Ops() <- protocol.Interrupt{}
	eng.Ops() <- protocol.UserInput{Text: "second"}
	_ = receiveRequest(t, prov.requests)
	select {
	case <-secondCanceled:
		t.Fatal("late interrupt canceled the next turn")
	default:
	}
	close(releaseSecond)
	if completed := waitForTurnCompleted(t, eng.Events()); completed.StopReason != "end_turn" {
		t.Errorf("second turn stop reason = %q, want end_turn", completed.StopReason)
	}
}

func assertCorrelationFields(t *testing.T, ev protocol.Event, corr protocol.Correlation, wantTurn, wantProvider bool) {
	t.Helper()
	if (corr.TurnID != "") != wantTurn {
		t.Errorf("%T turnId = %q, want populated=%v", ev, corr.TurnID, wantTurn)
	}
	if (corr.ProviderRequestID != "") != wantProvider {
		t.Errorf("%T providerRequestId = %q, want populated=%v", ev, corr.ProviderRequestID, wantProvider)
	}
}

func eventCorrelation(t *testing.T, ev protocol.Event) protocol.Correlation {
	t.Helper()
	switch ev := ev.(type) {
	case protocol.UserMessage:
		return ev.Correlation
	case protocol.TurnStarted:
		return ev.Correlation
	case protocol.TextDelta:
		return ev.Correlation
	case protocol.ToolCallBegin:
		return ev.Correlation
	case protocol.ToolCallEnd:
		return ev.Correlation
	case protocol.PermissionAsked:
		return ev.Correlation
	case protocol.PermissionResolved:
		return ev.Correlation
	case protocol.TurnCompleted:
		return ev.Correlation
	case protocol.ModelSelected:
		return ev.Correlation
	case protocol.AgentSelected:
		return ev.Correlation
	case protocol.EngineError:
		return ev.Correlation
	case protocol.UsageReported:
		return ev.Correlation
	case protocol.EffortSelected:
		return ev.Correlation
	case protocol.FastSelected:
		return ev.Correlation
	default:
		t.Fatalf("event %T has no correlation assertion", ev)
		return protocol.Correlation{}
	}
}

func TestUsageReportedBeforeTurnCompletedWithCorrelation(t *testing.T) {
	const sessionID = "session-usage"
	prov := newScriptedProvider(streamStep{events: []provider.StreamEvent{
		{Type: provider.EventTextDelta, Text: "ok"},
		{Type: provider.EventDone, StopReason: "end_turn", Usage: &provider.Usage{
			InputTokens:         10,
			OutputTokens:        5,
			CacheReadTokens:     2,
			CacheCreationTokens: 1,
		}},
	}})
	eng := engine.New(engine.Options{
		SessionID:       sessionID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "scripted-model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hi"}
	_, events := collectThroughTurnCompleted(t, eng.Events())

	var usageIdx, completedIdx = -1, -1
	var usage protocol.UsageReported
	var completed protocol.TurnCompleted
	for i, ev := range events {
		switch e := ev.(type) {
		case protocol.UsageReported:
			usageIdx = i
			usage = e
		case protocol.TurnCompleted:
			completedIdx = i
			completed = e
		}
	}
	if usageIdx < 0 {
		t.Fatal("missing UsageReported")
	}
	if completedIdx < 0 {
		t.Fatal("missing TurnCompleted")
	}
	if usageIdx >= completedIdx {
		t.Fatalf("UsageReported at %d must precede TurnCompleted at %d", usageIdx, completedIdx)
	}
	if usage.Correlation.SessionID != sessionID {
		t.Errorf("usage sessionId = %q, want %q", usage.Correlation.SessionID, sessionID)
	}
	if usage.Correlation.TurnID == "" || usage.Correlation.ProviderRequestID == "" {
		t.Errorf("usage correlation incomplete: %+v", usage.Correlation)
	}
	if usage.Correlation != completed.Correlation {
		t.Errorf("usage corr = %+v, completed corr = %+v", usage.Correlation, completed.Correlation)
	}
	if !usage.Input.Known || usage.Input.N != 10 {
		t.Errorf("input = %+v, want known 10", usage.Input)
	}
	if !usage.Output.Known || usage.Output.N != 5 {
		t.Errorf("output = %+v, want known 5", usage.Output)
	}
	// used = input + cacheRead + cacheCreation + output = 10+2+1+5 = 18
	if !usage.Used.Known || usage.Used.N != 18 {
		t.Errorf("used = %+v, want known 18", usage.Used)
	}
	if usage.Source != protocol.UsageSourceActual {
		t.Errorf("source = %q, want %q", usage.Source, protocol.UsageSourceActual)
	}
}

func TestNoUsageReportedWhenDoneHasNilUsage(t *testing.T) {
	prov := newScriptedProvider(streamStep{events: []provider.StreamEvent{
		{Type: provider.EventTextDelta, Text: "ok"},
		{Type: provider.EventDone, StopReason: "end_turn"},
	}})
	eng := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return prov, "scripted-model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hi"}
	_, events := collectThroughTurnCompleted(t, eng.Events())
	for _, ev := range events {
		if _, ok := ev.(protocol.UsageReported); ok {
			t.Fatalf("unexpected UsageReported when Done.Usage is nil: %#v", events)
		}
	}
}

func TestUsageReportedUsesTotalTokensWhenPartsZero(t *testing.T) {
	prov := newScriptedProvider(streamStep{events: []provider.StreamEvent{
		{Type: provider.EventDone, StopReason: "end_turn", Usage: &provider.Usage{TotalTokens: 77}},
	}})
	eng := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hi"}
	_, events := collectThroughTurnCompleted(t, eng.Events())
	var usage *protocol.UsageReported
	for _, ev := range events {
		if u, ok := ev.(protocol.UsageReported); ok {
			cp := u
			usage = &cp
		}
	}
	if usage == nil {
		t.Fatal("missing UsageReported")
	}
	// Parts were not broken out by the vendor — only TotalTokens. Do not
	// fabricate Known zero for input/output; Used carries the total.
	if usage.Input.Known {
		t.Errorf("input = %+v, want Known=false when only TotalTokens is set", usage.Input)
	}
	if usage.Output.Known {
		t.Errorf("output = %+v, want Known=false when only TotalTokens is set", usage.Output)
	}
	if !usage.Used.Known || usage.Used.N != 77 {
		t.Errorf("used = %+v, want known 77 from TotalTokens fallback", usage.Used)
	}
}

func TestUsageReportedEstimatedSource(t *testing.T) {
	prov := newScriptedProvider(streamStep{events: []provider.StreamEvent{
		{Type: provider.EventDone, StopReason: "end_turn", Usage: &provider.Usage{
			InputTokens: 3, OutputTokens: 4, Estimated: true,
		}},
	}})
	eng := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hi"}
	_, events := collectThroughTurnCompleted(t, eng.Events())
	var usage *protocol.UsageReported
	for _, ev := range events {
		if u, ok := ev.(protocol.UsageReported); ok {
			cp := u
			usage = &cp
		}
	}
	if usage == nil {
		t.Fatal("missing UsageReported")
	}
	if usage.Source != protocol.UsageSourceEstimated {
		t.Errorf("source = %q, want %q", usage.Source, protocol.UsageSourceEstimated)
	}
}
