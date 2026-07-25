package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

// TestDeclarativeHookBlockWrite is the H2 exit criterion: a rule that blocks
// write bounces with ToolFeedbackBlocked and never mutates the file.
func TestDeclarativeHookBlockWrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"filePath": "out.txt", "content": "pwned\n"})
	call := provider.ToolCall{ID: "w1", Name: "write", Args: args}
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
	allowWrite := permission.Ruleset{
		{Permission: "write", Pattern: "*", Action: permission.Allow},
	}
	eng := engine.New(engine.Options{
		SessionID: "decl-block-write",
		Select: func(string) (provider.Provider, string, error) {
			return prov, "m", nil
		},
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewWrite()),
		WorkDir:         dir,
		Rules:           []permission.Ruleset{permission.Defaults(), allowWrite},
		HookRules: permission.HookRuleset{{
			Event:   permission.HookEventPreToolUse,
			Matcher: "write",
			Action:  permission.HookActionBlock,
			Message: "writes not allowed",
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "write"}
	var end protocol.ToolCallEnd
	var sawEnd, sawBlock bool
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				t.Fatalf("unexpected ask under declarative block: %#v", ev)
			case protocol.HookMatched:
				if ev.Action == permission.HookActionBlock {
					sawBlock = true
				}
			case protocol.ToolCallEnd:
				if ev.CallID == "w1" {
					end = ev
					sawEnd = true
				}
			case protocol.TurnCompleted:
				goto done
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
done:
	if !sawBlock {
		t.Fatal("missing HookMatched block")
	}
	if !sawEnd || !end.IsError {
		t.Fatalf("ToolCallEnd = %#v", end)
	}
	want := protocol.ToolFeedbackBlocked("writes not allowed")
	if end.Output != want {
		t.Errorf("output = %q, want %q", end.Output, want)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original\n" {
		t.Errorf("file = %q, want original", data)
	}
}

func TestDeclarativeHookLogNotifyAndTurn(t *testing.T) {
	dir := t.TempDir()
	call := provider.ToolCall{ID: "s1", Name: "sleep", Args: []byte(`{"seconds":0.01}`)}
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
		Rules:           []permission.Ruleset{permission.Defaults()},
		HookRules: permission.HookRuleset{
			{Event: permission.HookEventTurnStart, Action: permission.HookActionLog},
			{Event: permission.HookEventPreToolUse, Matcher: "*", Action: permission.HookActionLog},
			{Event: permission.HookEventPostToolUse, Matcher: "sleep", Action: permission.HookActionNotify, Message: "slept"},
			{Event: permission.HookEventTurnEnd, Action: permission.HookActionNotify, Message: "turn done"},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}
	var hits []protocol.HookMatched
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.HookMatched:
				hits = append(hits, ev)
			case protocol.TurnCompleted:
				goto done
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
done:
	want := map[string]int{
		permission.HookEventTurnStart + "/" + permission.HookActionLog:      1,
		permission.HookEventPreToolUse + "/" + permission.HookActionLog:     1,
		permission.HookEventPostToolUse + "/" + permission.HookActionNotify: 1,
		permission.HookEventTurnEnd + "/" + permission.HookActionNotify:     1,
	}
	got := map[string]int{}
	for _, h := range hits {
		got[h.Event+"/"+h.Action]++
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("%s = %d, want %d; hits=%v", k, got[k], n, hits)
		}
	}
}

func TestDeclarativeBlockSkipsShellHooks(t *testing.T) {
	// A block rule must not run shell hooks (trust ask / process).
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	call := provider.ToolCall{ID: "s1", Name: "sleep", Args: []byte(`{"seconds":0.01}`)}
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
		HookRules: permission.HookRuleset{{
			Event:   permission.HookEventPreToolUse,
			Matcher: "sleep",
			Action:  permission.HookActionBlock,
			Message: "no sleep",
		}},
		Hooks: []tool.HookDef{{
			Event:   tool.HookEventPreToolUse,
			Command: "touch " + marker,
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
			case protocol.TurnCompleted:
				if _, err := os.Stat(marker); err == nil {
					t.Fatal("shell hook ran despite declarative block")
				}
				return
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
}
