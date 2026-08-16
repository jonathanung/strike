package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// leakTool returns a fixed secret-bearing payload so we can assert engine
// scrubbing without depending on shell env dumps.
type leakTool struct {
	output string
}

func (leakTool) Name() string { return "leak" }
func (leakTool) Description() string {
	return "test tool that returns a fixed string"
}
func (leakTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t leakTool) Execute(context.Context, json.RawMessage, *tool.Context) (tool.Result, error) {
	return tool.Result{Title: "leak", Output: t.output}, nil
}

// TestToolResultSecretsScrubbed is Tier C: nested/echoed credential shapes in
// tool output must not reach ToolCallEnd or model-facing history.
func TestToolResultSecretsScrubbed(t *testing.T) {
	key := "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	bearer := "tok_nested_bypass_abc1234567890"
	payload := "outer wrapper\nAuthorization: Bearer " + bearer + "\nkey=" + key + "\nend"

	call := provider.ToolCall{ID: "leak-1", Name: "leak", Args: json.RawMessage(`{}`)}
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &call},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		streamStep{
			match: matchToolResult("leak-1"),
			events: []provider.StreamEvent{
				{Type: provider.EventTextDelta, Text: "done"},
				{Type: provider.EventDone, StopReason: "end_turn"},
			},
		},
	)

	eng := engine.New(engine.Options{
		SessionID: "s-secret",
		Select: func(string) (provider.Provider, string, error) {
			return prov, "echo", nil
		},
		InitialProvider: "echo",
		InitialModel:    "echo",
		Registry:        tool.NewRegistry(leakTool{output: payload}),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "leak please"}

	var end protocol.ToolCallEnd
	var sawEnd, sawDone bool
	deadline := time.After(10 * time.Second)
	for !sawDone {
		select {
		case <-deadline:
			t.Fatal("timed out")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.ToolCallEnd:
				end = ev
				sawEnd = true
			case protocol.TurnCompleted:
				sawDone = true
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
	if !sawEnd {
		t.Fatal("missing ToolCallEnd")
	}
	for _, banned := range []string{key, bearer} {
		if strings.Contains(end.Output, banned) {
			t.Fatalf("ToolCallEnd leaked %q: %q", banned, end.Output)
		}
	}
	// Model-facing history must also be scrubbed (second stream sees tool result).
	for _, m := range eng.Messages() {
		if m.ToolResult == nil {
			continue
		}
		for _, banned := range []string{key, bearer} {
			if strings.Contains(m.ToolResult.Output, banned) {
				t.Fatalf("history tool result leaked %q: %q", banned, m.ToolResult.Output)
			}
		}
	}
}
