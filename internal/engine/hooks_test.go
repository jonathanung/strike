package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestPreToolUseHookBlocks(t *testing.T) {
	dir := t.TempDir()
	call := provider.ToolCall{ID: "c1", Name: "sleep", Args: []byte(`{"seconds":0.01}`)}
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &call},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	eng := engine.New(engine.Options{
		Select: func(string) (provider.Provider, string, error) {
			return prov, "m", nil
		},
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewSleep()),
		WorkDir:         dir,
		Rules: []permission.Ruleset{
			permission.Defaults(),
			{{Permission: "hook", Pattern: "*", Action: permission.Allow}},
		},
		Hooks: []tool.HookDef{{
			Event:   tool.HookEventPreToolUse,
			Command: `printf 'blocked by policy'; exit 2`,
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}

	var end protocol.ToolCallEnd
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.ToolCallEnd:
				end = ev
			case protocol.TurnCompleted:
				if end.CallID == "" {
					t.Fatal("no ToolCallEnd")
				}
				if !end.IsError {
					t.Fatalf("want error end, got %q", end.Output)
				}
				want := protocol.ToolFeedbackBlocked("blocked by policy")
				if end.Output != want {
					t.Fatalf("output=%q want %q", end.Output, want)
				}
				return
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			case protocol.PermissionAsked:
				t.Fatalf("unexpected permission ask: %#v", ev)
			}
		}
	}
}

func TestPreToolUseHookTrustAsk(t *testing.T) {
	dir := t.TempDir()
	call := provider.ToolCall{ID: "c1", Name: "sleep", Args: []byte(`{"seconds":0.01}`)}
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &call},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "done"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	eng := engine.New(engine.Options{
		Select: func(string) (provider.Provider, string, error) {
			return prov, "m", nil
		},
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewSleep()),
		WorkDir:         dir,
		Rules:           []permission.Ruleset{permission.Defaults()},
		Hooks: []tool.HookDef{{
			Event:   tool.HookEventPreToolUse,
			Command: `printf 'trusted-hook-note'`,
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}

	var sawHookAsk, sawEnd bool
	var end protocol.ToolCallEnd
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				if ev.Permission != "hook" {
					t.Fatalf("permission=%q want hook", ev.Permission)
				}
				sawHookAsk = true
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionAlways}
			case protocol.ToolCallEnd:
				sawEnd = true
				end = ev
			case protocol.TurnCompleted:
				if !sawHookAsk {
					t.Fatal("expected hook trust ask")
				}
				if !sawEnd {
					t.Fatal("no ToolCallEnd")
				}
				if end.IsError {
					t.Fatalf("tool error: %s", end.Output)
				}
				if !strings.Contains(end.Output, "trusted-hook-note") {
					t.Fatalf("output missing inject: %q", end.Output)
				}
				return
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
}

func TestPostToolUseHookInjects(t *testing.T) {
	dir := t.TempDir()
	call := provider.ToolCall{ID: "c1", Name: "sleep", Args: []byte(`{"seconds":0.01}`)}
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &call},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "done"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	eng := engine.New(engine.Options{
		Select: func(string) (provider.Provider, string, error) {
			return prov, "m", nil
		},
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewSleep()),
		WorkDir:         dir,
		Rules: []permission.Ruleset{
			permission.Defaults(),
			{{Permission: "hook", Pattern: "*", Action: permission.Allow}},
		},
		Hooks: []tool.HookDef{{
			Event:   tool.HookEventPostToolUse,
			Command: `printf 'post-note'`,
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.ToolCallEnd:
				if ev.IsError {
					t.Fatalf("tool error: %s", ev.Output)
				}
				if !strings.Contains(ev.Output, "post-note") {
					t.Fatalf("output=%q", ev.Output)
				}
			case protocol.TurnCompleted:
				return
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
}
