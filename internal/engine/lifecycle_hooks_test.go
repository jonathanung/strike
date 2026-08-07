package engine_test

import (
	"context"
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

func TestLifecycleSessionStartAndEnd(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "session.log")
	// Quote path for shell.
	eng := engine.New(engine.Options{
		SessionID:       "sess-life",
		WorkDir:         dir,
		InitialProvider: "scripted",
		Select: func(string) (provider.Provider, string, error) {
			return newScriptedProvider(streamStep{events: []provider.StreamEvent{
				{Type: provider.EventTextDelta, Text: "hi"},
				{Type: provider.EventDone, StopReason: "end_turn"},
			}}), "m", nil
		},
		Registry: tool.NewRegistry(),
		Rules: []permission.Ruleset{
			permission.Defaults(),
			{{Permission: "hook", Pattern: "*", Action: permission.Allow}},
		},
		HookRules: permission.HookRuleset{
			{Event: permission.HookEventSessionStart, Action: permission.HookActionLog},
			{Event: permission.HookEventSessionEnd, Action: permission.HookActionNotify, Message: "bye"},
		},
		Hooks: []tool.HookDef{{
			Event:   tool.HookEventSessionStart,
			Command: "printf start >> " + marker,
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		eng.Run(ctx)
		close(done)
	}()

	var sawStart bool
	deadline := time.After(5 * time.Second)
	for !sawStart {
		select {
		case <-deadline:
			t.Fatal("timeout waiting session_start")
		case ev := <-eng.Events():
			if hm, ok := ev.(protocol.HookMatched); ok && hm.Event == permission.HookEventSessionStart {
				sawStart = true
			}
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("Run did not exit")
	}

	// Drain closed/remaining events for session_end.
	var sawEnd bool
	drainDeadline := time.After(2 * time.Second)
	for !sawEnd {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				goto after
			}
			if hm, ok := ev.(protocol.HookMatched); ok && hm.Event == permission.HookEventSessionEnd {
				sawEnd = true
			}
		case <-drainDeadline:
			goto after
		}
	}
after:
	if !sawEnd {
		t.Error("missing session_end HookMatched")
	}
	if b, err := os.ReadFile(marker); err != nil || !strings.Contains(string(b), "start") {
		t.Errorf("shell session_start marker: %q err=%v", b, err)
	}
}

func TestLifecycleSessionResume(t *testing.T) {
	eng := engine.New(engine.Options{
		SessionID:       "sess-resume",
		QuietStartup:    true,
		InitialProvider: "scripted",
		InitialMessages: []provider.Message{
			{Role: provider.RoleUser, Text: "prior"},
		},
		Select: func(string) (provider.Provider, string, error) {
			return newScriptedProvider(), "m", nil
		},
		Registry: tool.NewRegistry(),
		HookRules: permission.HookRuleset{
			{Event: permission.HookEventSessionResume, Action: permission.HookActionLog},
			{Event: permission.HookEventSessionStart, Action: permission.HookActionLog},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout session_resume")
		case ev := <-eng.Events():
			if hm, ok := ev.(protocol.HookMatched); ok {
				if hm.Event == permission.HookEventSessionStart {
					t.Fatal("got session_start on resume path")
				}
				if hm.Event == permission.HookEventSessionResume {
					return
				}
			}
		}
	}
}

func TestLifecycleTurnAndProviderAttempt(t *testing.T) {
	eng := engine.New(engine.Options{
		SessionID:       "sess-turn-prov",
		InitialProvider: "scripted",
		Select: func(string) (provider.Provider, string, error) {
			return newScriptedProvider(streamStep{events: []provider.StreamEvent{
				{Type: provider.EventTextDelta, Text: "ok"},
				{Type: provider.EventDone, StopReason: "end_turn"},
			}}), "m", nil
		},
		Registry: tool.NewRegistry(),
		HookRules: permission.HookRuleset{
			{Event: permission.HookEventTurnStart, Action: permission.HookActionLog},
			{Event: permission.HookEventTurnEnd, Action: permission.HookActionNotify, Message: "done"},
			{Event: permission.HookEventProviderAttempt, Action: permission.HookActionLog},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hello"}

	var sawTurnStart, sawTurnEnd, sawAttempt bool
	deadline := time.After(10 * time.Second)
	for !(sawTurnStart && sawTurnEnd && sawAttempt) {
		select {
		case <-deadline:
			t.Fatalf("turnStart=%v turnEnd=%v attempt=%v", sawTurnStart, sawTurnEnd, sawAttempt)
		case ev := <-eng.Events():
			if hm, ok := ev.(protocol.HookMatched); ok {
				switch hm.Event {
				case permission.HookEventTurnStart:
					sawTurnStart = true
				case permission.HookEventTurnEnd:
					sawTurnEnd = true
				case permission.HookEventProviderAttempt:
					sawAttempt = true
					if hm.Correlation.TurnID == "" {
						t.Error("provider_attempt missing turn correlation")
					}
				}
			}
		}
	}
}

func TestLifecyclePermissionResolutionHook(t *testing.T) {
	// PermissionDecided (and thus permission_resolution hooks) fire on deny/ask
	// paths — synchronous allow is intentionally silent to avoid JSONL flood.
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
		SessionID:       "sess-perm-hook",
		WorkDir:         dir,
		InitialProvider: "scripted",
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		Registry:        tool.NewRegistry(tool.NewSleep()),
		Rules: []permission.Ruleset{
			{{Permission: "sleep", Pattern: "*", Action: permission.Deny}},
		},
		HookRules: permission.HookRuleset{
			{Event: permission.HookEventPermissionResolution, Action: permission.HookActionLog},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}

	var sawPermHook bool
	var end protocol.ToolCallEnd
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			if hm, ok := ev.(protocol.HookMatched); ok && hm.Event == permission.HookEventPermissionResolution {
				sawPermHook = true
				if hm.Tool != "sleep" {
					t.Errorf("subject tool=%q want sleep", hm.Tool)
				}
			}
			if e, ok := ev.(protocol.ToolCallEnd); ok {
				end = e
			}
			if _, ok := ev.(protocol.TurnCompleted); ok {
				if !sawPermHook {
					t.Fatal("turn completed without permission_resolution hook")
				}
				if !end.IsError {
					t.Fatal("deny must fail tool")
				}
				return
			}
		}
	}
}

func TestLifecycleCompactionHook(t *testing.T) {
	// Seed enough history so Compact can drop older turns.
	msgs := make([]provider.Message, 0, 8)
	for i := 0; i < 6; i++ {
		msgs = append(msgs,
			provider.Message{Role: provider.RoleUser, Text: "u" + string(rune('a'+i))},
			provider.Message{Role: provider.RoleAssistant, Text: "a" + string(rune('a'+i))},
		)
	}
	eng := engine.New(engine.Options{
		SessionID:       "sess-compact-hook",
		InitialProvider: "scripted",
		InitialMessages: msgs,
		KeepUserTurns:   1,
		Select: func(string) (provider.Provider, string, error) {
			return newScriptedProvider(), "m", nil
		},
		Registry: tool.NewRegistry(),
		HookRules: permission.HookRuleset{
			{Event: permission.HookEventCompaction, Action: permission.HookActionLog},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Wait for startup then compact.
	time.Sleep(50 * time.Millisecond)
	eng.Ops() <- protocol.Compact{}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout compaction hook")
		case ev := <-eng.Events():
			if hm, ok := ev.(protocol.HookMatched); ok && hm.Event == permission.HookEventCompaction {
				return
			}
			if err, ok := ev.(protocol.EngineError); ok && strings.Contains(err.Message, "nothing to compact") {
				t.Fatalf("compact failed: %s", err.Message)
			}
		}
	}
}

func TestLifecycleHooksCannotWidenHardDeny(t *testing.T) {
	// pre_tool_use allow hook must not run Execute when ruleset denies.
	dir := t.TempDir()
	call := provider.ToolCall{ID: "c1", Name: "bash", Args: []byte(`{"command":"echo pwned"}`)}
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
	marker := filepath.Join(dir, "should-not-run")
	eng := engine.New(engine.Options{
		SessionID:       "sess-no-widen",
		WorkDir:         dir,
		InitialProvider: "scripted",
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		Registry:        tool.NewRegistry(tool.NewBash()),
		Rules: []permission.Ruleset{
			{{Permission: "bash", Pattern: "*", Action: permission.Deny}},
			{{Permission: "hook", Pattern: "*", Action: permission.Allow}},
		},
		Hooks: []tool.HookDef{{
			Event:   tool.HookEventPreToolUse,
			Matcher: "bash",
			Command: "printf allowed-by-hook; touch " + marker,
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
				if !end.IsError {
					t.Fatalf("hard deny must error, got %q", end.Output)
				}
				if _, err := os.Stat(marker); err == nil {
					t.Fatal("pre_tool_use hook ran despite hard deny — widened permission")
				}
				return
			}
		}
	}
}

func TestLifecycleDeclarativeOrderingBeforeShell(t *testing.T) {
	dir := t.TempDir()
	orderFile := filepath.Join(dir, "order")
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
		SessionID:       "sess-order",
		WorkDir:         dir,
		InitialProvider: "scripted",
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		Registry:        tool.NewRegistry(tool.NewSleep()),
		Rules: []permission.Ruleset{
			permission.Defaults(),
			{{Permission: "hook", Pattern: "*", Action: permission.Allow}},
			{{Permission: "sleep", Pattern: "*", Action: permission.Allow}},
		},
		HookRules: permission.HookRuleset{
			{Event: permission.HookEventPreToolUse, Matcher: "sleep", Action: permission.HookActionLog},
		},
		Hooks: []tool.HookDef{{
			Event:   tool.HookEventPreToolUse,
			Matcher: "sleep",
			Command: "printf shell >> " + orderFile,
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "go"}

	var sawRule bool
	var ruleBeforeShell bool
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			if hm, ok := ev.(protocol.HookMatched); ok && hm.Event == permission.HookEventPreToolUse {
				sawRule = true
				if _, err := os.Stat(orderFile); err != nil {
					ruleBeforeShell = true
				}
			}
			if _, ok := ev.(protocol.TurnCompleted); ok {
				if !sawRule {
					t.Fatal("missing declarative HookMatched")
				}
				if !ruleBeforeShell {
					t.Fatal("declarative rule should fire before shell hook side effect")
				}
				if _, err := os.Stat(orderFile); err != nil {
					t.Fatal("shell hook did not run")
				}
				return
			}
		}
	}
}
