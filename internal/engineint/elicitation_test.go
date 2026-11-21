package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/internal/enginebind"
	"github.com/jonathanung/strike/pkg/protocol"
)

// elicitingTool stands in for an MCP bridge tool whose server pauses the
// call with elicitation/create (#1353). It reports what came back.
type elicitingTool struct{}

func (elicitingTool) Name() string        { return "fixture_ask" }
func (elicitingTool) Description() string { return "asks for a name mid call" }
func (elicitingTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (elicitingTool) Execute(ctx context.Context, _ json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if tc.Elicit == nil {
		return tool.Result{Output: "no-elicit"}, nil
	}
	res := tc.Elicit(ctx, tool.ElicitRequest{
		Server:          "fixture",
		Message:         "Which name?",
		Mode:            protocol.ElicitationModeForm,
		RequestedSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`),
	})
	return tool.Result{Output: res.Action + ":" + string(res.Content)}, nil
}

func runElicitTurn(t *testing.T, reply func(protocol.ElicitationRequested) protocol.ElicitationReply) (requested protocol.ElicitationRequested, resolved protocol.ElicitationResolved, output string) {
	t.Helper()
	call := provider.ToolCall{ID: "ecall-1", Name: "fixture_ask", Args: json.RawMessage(`{}`)}
	prov := newScriptedProvider(toolCallStep(call), completedStep("done"))
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "sess-e",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(elicitingTool{}),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "go"}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the elicitation turn")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.ElicitationRequested:
				requested = ev
				eng.Ops() <- reply(ev)
			case protocol.ElicitationResolved:
				resolved = ev
			case protocol.ToolCallEnd:
				if ev.CallID == "ecall-1" {
					output = ev.Output
				}
			case protocol.TurnCompleted:
				return requested, resolved, output
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
}

func TestElicitationRoundTripThroughEngine(t *testing.T) {
	cases := []struct {
		name       string
		reply      func(protocol.ElicitationRequested) protocol.ElicitationReply
		wantAction string
		wantOutput string
	}{
		{
			name: "accept carries content back to the same call",
			reply: func(ev protocol.ElicitationRequested) protocol.ElicitationReply {
				return protocol.ElicitationReply{RequestID: ev.RequestID, Action: "accept", Content: json.RawMessage(`{"name":"ada"}`)}
			},
			wantAction: "accept",
			wantOutput: `accept:{"name":"ada"}`,
		},
		{
			name: "decline settles without content",
			reply: func(ev protocol.ElicitationRequested) protocol.ElicitationReply {
				return protocol.ElicitationReply{RequestID: ev.RequestID, Action: "decline", Content: json.RawMessage(`{"name":"x"}`)}
			},
			wantAction: "decline",
			wantOutput: "decline:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, res, out := runElicitTurn(t, tc.reply)
			if req.Server != "fixture" || req.Mode != "form" || req.RequestID == "" || len(req.RequestedSchema) == 0 {
				t.Fatalf("requested = %#v", req)
			}
			if req.SessionID != "sess-e" {
				t.Fatalf("requested correlation = %#v", req.Correlation)
			}
			if res.RequestID != req.RequestID || res.Action != tc.wantAction {
				t.Fatalf("resolved = %#v", res)
			}
			if !strings.Contains(out, tc.wantOutput) {
				t.Fatalf("tool output = %q, want %q", out, tc.wantOutput)
			}
		})
	}
}
