package engine_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestManualCompactReplacesOlderHistory(t *testing.T) {
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "r1"},
			{Type: provider.EventDone, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 100, OutputTokens: 10}},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "r2"},
			{Type: provider.EventDone, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 200, OutputTokens: 10}},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "r3"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	eng := engine.New(engine.Options{
		SessionID:       "session-manual-compact",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		KeepUserTurns:   1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "first"}
	waitCompactTurn(t, eng)
	eng.Ops() <- protocol.UserInput{Text: "second"}
	waitCompactTurn(t, eng)

	eng.Ops() <- protocol.Compact{}
	var started protocol.CompactionStarted
	var completed protocol.CompactionCompleted
	deadline := time.After(3 * time.Second)
	for completed.Reason == "" {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.CompactionStarted:
				started = ev
			case protocol.CompactionCompleted:
				completed = ev
			case protocol.EngineError:
				t.Fatalf("unexpected EngineError: %s", ev.Message)
			}
		case <-deadline:
			t.Fatal("timed out waiting for compaction")
		}
	}
	if started.Reason != protocol.CompactionReasonManual {
		t.Fatalf("started reason = %q", started.Reason)
	}
	if completed.Reason != protocol.CompactionReasonManual || completed.Removed < 1 {
		t.Fatalf("completed = %#v", completed)
	}

	eng.Ops() <- protocol.UserInput{Text: "third"}
	req := waitCompactTurnRequest(t, eng, prov)
	if len(req.Messages) == 0 {
		t.Fatal("empty history after compact")
	}
	foundMarker := false
	foundThird := false
	foundFirst := false
	for _, m := range req.Messages {
		if m.Role == provider.RoleUser && strings.HasPrefix(m.Text, "[Prior conversation compacted") {
			foundMarker = true
		}
		if m.Role == provider.RoleUser && m.Text == "third" {
			foundThird = true
		}
		if m.Role == provider.RoleUser && m.Text == "first" {
			foundFirst = true
		}
	}
	if !foundMarker {
		t.Fatalf("compact marker missing in %#v", req.Messages)
	}
	if !foundThird {
		t.Fatalf("current user intent missing in %#v", req.Messages)
	}
	if foundFirst {
		t.Fatalf("old first user turn should have been compacted away: %#v", req.Messages)
	}
}

func TestThresholdCompactionBeforeStream(t *testing.T) {
	// Three scripted responses: seed-a, seed-b (reports high usage), then
	// third turn auto-compacts before Stream.
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "a"},
			{Type: provider.EventDone, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 100, OutputTokens: 10}},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "b"},
			{Type: provider.EventDone, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 900, OutputTokens: 50}},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "c"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	eng := engine.New(engine.Options{
		SessionID:           "session-threshold",
		Select:              func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:     "scripted",
		Registry:            tool.NewRegistry(),
		WorkDir:             t.TempDir(),
		Rules:               []permission.Ruleset{permission.Defaults()},
		ContextWindow:       1000,
		CompactionThreshold: 0.80,
		CompactionBuffer:    1,
		MaxTokens:           10,
		KeepUserTurns:       1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "seed-a"}
	waitCompactTurn(t, eng)
	eng.Ops() <- protocol.UserInput{Text: "seed-b"}
	waitCompactTurn(t, eng)

	eng.Ops() <- protocol.UserInput{Text: "seed-c"}
	var sawThreshold bool
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.CompactionStarted:
				if ev.Reason != protocol.CompactionReasonThreshold {
					t.Fatalf("reason = %q, want threshold", ev.Reason)
				}
				sawThreshold = true
			case protocol.TurnCompleted:
				if !sawThreshold {
					t.Fatal("expected threshold compaction before third turn stream")
				}
				return
			case protocol.EngineError:
				t.Fatalf("EngineError: %s", ev.Message)
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}
}

func TestOverflowCompactsAndRetriesModelOnly(t *testing.T) {
	var bashRuns atomic.Int32
	bash := &countingBash{runs: &bashRuns}
	call := provider.ToolCall{ID: "c1", Name: "bash", Args: []byte(`{"command":"echo once"}`)}

	prov := &scriptedProvider{steps: []streamStep{
		{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &call},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "done1"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
		{err: errors.New("openai: invalid_request_error: context_length_exceeded: too many tokens")},
		{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "recovered"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	}, requests: make(chan provider.Request, 8)}

	eng := engine.New(engine.Options{
		SessionID:          "session-overflow",
		Select:             func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:    "scripted",
		Registry:           tool.NewRegistry(bash),
		WorkDir:            t.TempDir(),
		Rules:              []permission.Ruleset{permission.Defaults()},
		KeepUserTurns:      1,
		MaxStreamAttempts:  1,
		StreamRetryBackoff: func(int) time.Duration { return 0 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "use tool"}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.TurnCompleted:
				goto afterFirst
			case protocol.EngineError:
				t.Fatalf("first turn error: %s", ev.Message)
			}
		case <-deadline:
			t.Fatal("timed out first turn")
		}
	}
afterFirst:

	eng.Ops() <- protocol.UserInput{Text: "continue"}
	var sawStarted, sawCompleted bool
	var text string
	var stop string
	deadline = time.After(5 * time.Second)
	for stop == "" {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.CompactionStarted:
				if ev.Reason != protocol.CompactionReasonOverflow {
					t.Fatalf("compaction reason = %q", ev.Reason)
				}
				sawStarted = true
			case protocol.CompactionCompleted:
				sawCompleted = true
			case protocol.TextDelta:
				text += ev.Text
			case protocol.TurnCompleted:
				stop = ev.StopReason
			case protocol.EngineError:
				t.Fatalf("unexpected EngineError: %s", ev.Message)
			case protocol.PermissionAsked:
				t.Fatal("permission ask on overflow recovery — tools must not re-run")
			}
		case <-deadline:
			t.Fatal("timed out overflow recovery")
		}
	}
	if !sawStarted || !sawCompleted {
		t.Fatalf("compaction events started=%v completed=%v", sawStarted, sawCompleted)
	}
	if text != "recovered" {
		t.Fatalf("text = %q", text)
	}
	if stop != "end_turn" {
		t.Fatalf("stop = %q", stop)
	}
	if bashRuns.Load() != 1 {
		t.Fatalf("bash runs = %d, want 1 (no tool replay)", bashRuns.Load())
	}
	var lastReq provider.Request
	for {
		select {
		case lastReq = <-prov.requests:
		default:
			goto checkReq
		}
	}
checkReq:
	foundMarker := false
	for _, m := range lastReq.Messages {
		if m.Role == provider.RoleUser && strings.HasPrefix(m.Text, "[Prior conversation compacted") {
			foundMarker = true
		}
	}
	if !foundMarker {
		t.Fatalf("post-overflow history missing marker: %#v", lastReq.Messages)
	}
}

func TestOverflowWithoutCompactableHistoryFailsOnce(t *testing.T) {
	prov := newScriptedProvider(
		streamStep{err: errors.New("maximum context length exceeded")},
	)
	eng := engine.New(engine.Options{
		SessionID:         "session-overflow-fail",
		Select:            func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:   "scripted",
		Registry:          tool.NewRegistry(),
		WorkDir:           t.TempDir(),
		Rules:             []permission.Ruleset{permission.Defaults()},
		MaxStreamAttempts: 1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "only"}
	var errMsg string
	var stop string
	deadline := time.After(3 * time.Second)
	for stop == "" {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.EngineError:
				errMsg = ev.Message
			case protocol.TurnCompleted:
				stop = ev.StopReason
			case protocol.CompactionCompleted:
				t.Fatal("unexpected compaction with nothing to drop")
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}
	if stop != "error" {
		t.Fatalf("stop = %q, want error", stop)
	}
	if !strings.Contains(errMsg, "compaction could not reduce history") {
		t.Fatalf("error = %q", errMsg)
	}
	if prov.callCount() != 1 {
		t.Fatalf("Stream calls = %d, want 1 (no retry loop)", prov.callCount())
	}
}

func waitCompactTurn(t *testing.T, eng *engine.Engine) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.TurnCompleted:
				return
			case protocol.EngineError:
				t.Fatalf("EngineError: %s", ev.Message)
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn")
		}
	}
}

func waitCompactTurnRequest(t *testing.T, eng *engine.Engine, prov *scriptedProvider) provider.Request {
	t.Helper()
	deadline := time.After(5 * time.Second)
	var req provider.Request
	gotReq := false
	for {
		select {
		case r := <-prov.requests:
			req = r
			gotReq = true
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.TurnCompleted:
				if !gotReq {
					t.Fatal("turn completed without provider request")
				}
				return req
			case protocol.EngineError:
				t.Fatalf("EngineError: %s", ev.Message)
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn request")
		}
	}
}
