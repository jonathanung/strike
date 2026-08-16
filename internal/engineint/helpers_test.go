package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/provider/echo"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/internal/enginebind"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

type streamStep struct {
	events []provider.StreamEvent
	err    error
	stream func(context.Context) <-chan provider.StreamEvent
	match  func(provider.Request) bool
}

type scriptedProvider struct {
	mu       sync.Mutex
	steps    []streamStep
	used     []bool
	calls    int
	requests chan provider.Request
}

func (p *scriptedProvider) Name() string { return "scripted" }

func newScriptedProvider(steps ...streamStep) *scriptedProvider {
	return &scriptedProvider{
		steps:    steps,
		used:     make([]bool, len(steps)),
		requests: make(chan provider.Request, len(steps)+8),
	}
}

func (p *scriptedProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	if len(p.used) != len(p.steps) {
		p.used = make([]bool, len(p.steps))
	}
	idx := -1
	for i, step := range p.steps {
		if p.used[i] || step.match == nil || !step.match(req) {
			continue
		}
		idx = i
		break
	}
	if idx < 0 {
		for i, step := range p.steps {
			if p.used[i] || step.match != nil {
				continue
			}
			idx = i
			break
		}
	}
	if idx < 0 {
		p.mu.Unlock()
		return nil, errors.New("unexpected Stream call")
	}
	step := p.steps[idx]
	p.used[idx] = true
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

func matchUserText(text string) func(provider.Request) bool {
	return func(req provider.Request) bool {
		for _, m := range req.Messages {
			if m.Role == provider.RoleUser && m.Text == text {
				return true
			}
		}
		return false
	}
}

func matchToolResult(callID string) func(provider.Request) bool {
	return func(req provider.Request) bool {
		for _, m := range req.Messages {
			if m.Role == provider.RoleTool && m.ToolResult != nil && m.ToolResult.CallID == callID {
				return true
			}
		}
		return false
	}
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
		Select:              func(string) (provider.Provider, string, error) { return prov, "test-model", nil },
		InitialProvider:     "scripted",
		Registry:            tool.NewRegistry(tools...),
		WorkDir:             t.TempDir(),
		Rules:               []permission.Ruleset{permission.Defaults()},
		SandboxAllowDegrade: true,
		BuildDiagnostic:     enginebind.Diagnostic(),
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

const (
	canceledStartedOutput = "Tool call canceled because the turn was interrupted."
	unstartedOutput       = "Tool call not executed because the turn was interrupted before it started."
)

func streamStepText(text string) streamStep { return completedStep(text) }
