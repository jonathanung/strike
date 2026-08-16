package engine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

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

type countingBash struct {
	runs *atomic.Int32
}

func (c *countingBash) Name() string        { return "bash" }
func (c *countingBash) Description() string { return "test bash" }
func (c *countingBash) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)
}
func (c *countingBash) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	c.runs.Add(1)
	if err := tc.Ask(ctx, tool.AskRequest{Permission: "bash", Patterns: []string{"echo once"}}); err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Title: "echo once", Output: "once"}, nil
}

func taskToolCall(id, prompt string) provider.ToolCall {
	b, _ := json.Marshal(prompt)
	return provider.ToolCall{
		ID:   id,
		Name: "task",
		Args: json.RawMessage(`{"prompt":` + string(b) + `}`),
	}
}

func multiProviderSelect(providers map[string]*scriptedProvider, defaults map[string]string) engine.SelectFunc {
	return func(name string) (provider.Provider, string, error) {
		p, ok := providers[name]
		if !ok {
			return nil, "", fmt.Errorf("unknown provider %q", name)
		}
		def := defaults[name]
		if def == "" {
			def = name + "-default"
		}
		return p, def, nil
	}
}

func countEvents[T protocol.Event](events []protocol.Event) int {
	n := 0
	for _, ev := range events {
		if _, ok := ev.(T); ok {
			n++
		}
	}
	return n
}

func taskToolCallWithAgent(id, prompt, agent string) provider.ToolCall {
	args, _ := json.Marshal(map[string]any{
		"prompt": prompt,
		"agent":  agent,
	})
	return provider.ToolCall{ID: id, Name: "task", Args: args}
}

func waitForEvent(t *testing.T, eng *engine.Engine, pred func(protocol.Event) bool) protocol.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed before match")
			}
			if pred(ev) {
				return ev
			}
		case <-deadline:
			t.Fatal("timeout waiting for event")
		}
	}
}

func taskToolCallWith(id string, fields map[string]any) provider.ToolCall {
	if fields == nil {
		fields = map[string]any{}
	}
	args, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return provider.ToolCall{ID: id, Name: "task", Args: args}
}

func writeToolCall(id, path, content string) provider.ToolCall {
	args, err := json.Marshal(map[string]any{"filePath": path, "content": content})
	if err != nil {
		panic(err)
	}
	return provider.ToolCall{ID: id, Name: "write", Args: args}
}

func childCompletedNudgeStep(reply string) streamStep {
	s := completedStep(reply)
	s.match = func(req provider.Request) bool {
		for _, m := range req.Messages {
			if m.Role == provider.RoleUser && strings.Contains(m.Text, "[child.completed") {
				return true
			}
		}
		return false
	}
	return s
}

func drainAndReply(t *testing.T, eng *engine.Engine, timeout time.Duration) []protocol.Event {
	t.Helper()
	var collected []protocol.Event
	var parentDone bool
	var started, completed int
	var noticeSeen bool
	var turnsAfterNotice int
	guard := time.NewTimer(timeout)
	defer guard.Stop()
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed before drain finished")
			}
			collected = append(collected, ev)
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.QuestionAsked:
			case protocol.ChildStarted:
				started++
			case protocol.ChildCompleted:
				completed++
			case protocol.UserMessage:
				if strings.Contains(ev.Text, "[child.completed") {
					noticeSeen = true
				}
			case protocol.TurnCompleted:
				parentDone = true
				if noticeSeen {
					turnsAfterNotice++
				}
			}
			if !parentDone || started != completed {
				continue
			}
			if started == 0 {
				return collected
			}
			if turnsAfterNotice >= 1 && childCompletionNotices(collected) >= completed {
				return collected
			}
		case <-guard.C:
			t.Fatalf("timed out; parentDone=%v started=%d completed=%d notice=%v turnsAfterNotice=%d notices=%d events=%v",
				parentDone, started, completed, noticeSeen, turnsAfterNotice, childCompletionNotices(collected), summarizeEvents(collected))
		}
	}
}

func childCompletionNotices(events []protocol.Event) int {
	n := 0
	for _, ev := range events {
		if um, ok := ev.(protocol.UserMessage); ok {
			n += strings.Count(um.Text, "[child.completed")
		}
	}
	return n
}

func summarizeEvents(events []protocol.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, eventSummary(ev))
	}
	return out
}

func eventSummary(ev protocol.Event) string {
	switch ev := ev.(type) {
	case protocol.ChildStarted:
		return "ChildStarted session=" + ev.SessionID
	case protocol.ChildCompleted:
		return "ChildCompleted status=" + string(ev.Status)
	case protocol.ToolCallBegin:
		return "ToolCallBegin " + ev.Name + " " + ev.CallID
	case protocol.ToolCallEnd:
		return "ToolCallEnd " + ev.CallID + " err=" + boolString(ev.IsError)
	case protocol.PermissionAsked:
		return "PermissionAsked " + ev.Permission + " " + ev.RequestID
	case protocol.QuestionAsked:
		return "QuestionAsked " + ev.RequestID
	case protocol.QuestionResolved:
		return "QuestionResolved " + ev.RequestID
	case protocol.AgentSelected:
		return "AgentSelected " + ev.Name
	case protocol.TurnCompleted:
		return "TurnCompleted " + ev.StopReason
	case protocol.EngineError:
		return "EngineError " + ev.Message
	case protocol.TextDelta:
		return "TextDelta"
	default:
		return "other"
	}
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
