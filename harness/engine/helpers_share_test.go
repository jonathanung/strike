package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func historyToolPairsOK(msgs []provider.Message) bool {
	pending := map[string]struct{}{}
	for _, m := range msgs {
		switch m.Role {
		case provider.RoleAssistant:
			for _, c := range m.ToolCalls {
				if c.ID == "" {
					return false
				}
				pending[c.ID] = struct{}{}
			}
		case provider.RoleTool:
			if m.ToolResult == nil || m.ToolResult.CallID == "" {
				return false
			}
			if _, ok := pending[m.ToolResult.CallID]; !ok {
				return false
			}
			delete(pending, m.ToolResult.CallID)
		}
	}
	return len(pending) == 0
}

func captureSystemPrompt(t *testing.T, opts engine.Options, providerName, model string) string {
	t.Helper()
	prov := newScriptedProvider(streamStep{
		events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		},
	})
	opts.SessionID = "s-prompt"
	opts.Select = func(string) (provider.Provider, string, error) {
		return prov, model, nil
	}
	if opts.Registry == nil {
		opts.Registry = tool.NewRegistry()
	}
	opts.InitialProvider = providerName
	opts.InitialModel = model

	eng := engine.New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hi"}
	return waitStreamRequest(t, eng, prov).System
}

func waitStreamRequest(t *testing.T, eng *engine.Engine, prov *scriptedProvider) provider.Request {
	t.Helper()
	var req provider.Request
	deadline := time.After(5 * time.Second)
	for req.System == "" {
		select {
		case req = <-prov.requests:
		case ev := <-eng.Events():
			if err, ok := ev.(protocol.EngineError); ok {
				t.Fatalf("engine error: %s", err.Message)
			}
		case <-deadline:
			t.Fatal("timeout waiting for Stream request")
		}
	}
	return req
}
