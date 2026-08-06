package engine_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/ledger"
	"github.com/jonathanung/strike-cli/internal/memory"
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

func TestSummarizeCompactReplacesOlderHistory(t *testing.T) {
	matchSummarize := func(req provider.Request) bool {
		if len(req.Tools) != 0 {
			return false
		}
		for _, m := range req.Messages {
			if m.Role == provider.RoleUser && strings.Contains(m.Text, "Summarize this conversation history") {
				return true
			}
		}
		return false
	}
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "r1"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "r2"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
		streamStep{
			match: matchSummarize,
			events: []provider.StreamEvent{
				{Type: provider.EventTextDelta, Text: "SUMMARY: first turn about foo"},
				{Type: provider.EventDone, StopReason: "end_turn"},
			},
		},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "r3"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	eng := engine.New(engine.Options{
		SessionID:          "session-summarize-compact",
		Select:             func(string) (provider.Provider, string, error) { return prov, "cheap-model", nil },
		InitialProvider:    "scripted",
		Registry:           tool.NewRegistry(),
		WorkDir:            t.TempDir(),
		Rules:              []permission.Ruleset{permission.Defaults()},
		KeepUserTurns:      1,
		CompactionStrategy: protocol.CompactionStrategySummarize,
		CompactionModel:    "summary-model",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "first about foo"}
	waitCompactTurn(t, eng)
	eng.Ops() <- protocol.UserInput{Text: "second"}
	waitCompactTurn(t, eng)

	eng.Ops() <- protocol.Compact{}
	var started protocol.CompactionStarted
	var completed protocol.CompactionCompleted
	var sumReq provider.Request
	gotSumReq := false
	deadline := time.After(5 * time.Second)
	for completed.Reason == "" {
		select {
		case r := <-prov.requests:
			if matchSummarize(r) {
				sumReq = r
				gotSumReq = true
			}
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.CompactionStarted:
				started = ev
			case protocol.CompactionCompleted:
				completed = ev
			case protocol.EngineError:
				t.Fatalf("unexpected EngineError: %s", ev.Message)
			case protocol.TextDelta:
				t.Fatalf("summarize must not emit TextDelta into transcript: %q", ev.Text)
			}
		case <-deadline:
			t.Fatal("timed out waiting for summarize compaction")
		}
	}
	if started.Strategy != protocol.CompactionStrategySummarize {
		t.Fatalf("started strategy = %q", started.Strategy)
	}
	if completed.Strategy != protocol.CompactionStrategySummarize {
		t.Fatalf("completed strategy = %q", completed.Strategy)
	}
	if completed.Summary != "SUMMARY: first turn about foo" {
		t.Fatalf("summary = %q", completed.Summary)
	}
	if completed.Removed < 1 {
		t.Fatalf("removed = %d", completed.Removed)
	}
	// Drain any request that raced past CompactionCompleted in the select.
	for !gotSumReq {
		select {
		case r := <-prov.requests:
			if matchSummarize(r) {
				sumReq = r
				gotSumReq = true
			}
		default:
			goto afterSumReq
		}
	}
afterSumReq:
	if !gotSumReq {
		t.Fatal("summarize Stream request not observed")
	}
	if sumReq.Model != "summary-model" {
		t.Fatalf("summarize model = %q, want summary-model", sumReq.Model)
	}
	if len(sumReq.Tools) != 0 {
		t.Fatalf("summarize must not send tools: %#v", sumReq.Tools)
	}

	eng.Ops() <- protocol.UserInput{Text: "third"}
	req := waitCompactTurnRequest(t, eng, prov)
	foundSummary := false
	foundFirst := false
	foundThird := false
	for _, m := range req.Messages {
		if m.Role == provider.RoleUser && strings.Contains(m.Text, "SUMMARY: first turn about foo") {
			foundSummary = true
		}
		if m.Role == provider.RoleUser && m.Text == "first about foo" {
			foundFirst = true
		}
		if m.Role == provider.RoleUser && m.Text == "third" {
			foundThird = true
		}
	}
	if !foundSummary {
		t.Fatalf("summary missing in next Stream history: %#v", req.Messages)
	}
	if foundFirst {
		t.Fatalf("old first turn should not appear: %#v", req.Messages)
	}
	if !foundThird {
		t.Fatalf("third intent missing: %#v", req.Messages)
	}
	if !historyToolPairsOK(req.Messages) {
		t.Fatalf("invalid tool pairs after summarize: %#v", req.Messages)
	}
}

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

func TestSummarizeCompactFallsBackToTrim(t *testing.T) {
	matchSummarize := func(req provider.Request) bool {
		for _, m := range req.Messages {
			if m.Role == provider.RoleUser && strings.Contains(m.Text, "Summarize this conversation history") {
				return true
			}
		}
		return false
	}
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "r1"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "r2"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
		streamStep{match: matchSummarize, err: errors.New("summarizer unavailable")},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "r3"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	eng := engine.New(engine.Options{
		SessionID:          "session-summarize-fallback",
		Select:             func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:    "scripted",
		Registry:           tool.NewRegistry(),
		WorkDir:            t.TempDir(),
		Rules:              []permission.Ruleset{permission.Defaults()},
		KeepUserTurns:      1,
		CompactionStrategy: protocol.CompactionStrategySummarize,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "first"}
	waitCompactTurn(t, eng)
	eng.Ops() <- protocol.UserInput{Text: "second"}
	waitCompactTurn(t, eng)

	eng.Ops() <- protocol.Compact{}
	var completed protocol.CompactionCompleted
	var sawFallback bool
	deadline := time.After(5 * time.Second)
	for completed.Reason == "" {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.CompactionCompleted:
				completed = ev
			case protocol.EngineError:
				if strings.Contains(ev.Message, "fell back to trim") {
					sawFallback = true
				}
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}
	if !sawFallback {
		t.Fatal("expected fallback EngineError notice")
	}
	if completed.Strategy != protocol.CompactionStrategyTrim {
		t.Fatalf("applied strategy = %q, want trim", completed.Strategy)
	}
	if completed.Summary != "" {
		t.Fatalf("summary should be empty on trim fallback: %q", completed.Summary)
	}

	eng.Ops() <- protocol.UserInput{Text: "third"}
	req := waitCompactTurnRequest(t, eng, prov)
	foundMarker := false
	foundSummaryWord := false
	for _, m := range req.Messages {
		if m.Role == provider.RoleUser && strings.HasPrefix(m.Text, "[Prior conversation compacted") {
			foundMarker = true
			if strings.Contains(m.Text, "summary of") {
				foundSummaryWord = true
			}
		}
	}
	if !foundMarker {
		t.Fatalf("trim marker missing: %#v", req.Messages)
	}
	if foundSummaryWord {
		t.Fatalf("should not use summary marker after fallback: %#v", req.Messages)
	}
}

func TestSummarizeOpOverride(t *testing.T) {
	matchSummarize := func(req provider.Request) bool {
		for _, m := range req.Messages {
			if m.Role == provider.RoleUser && strings.Contains(m.Text, "Summarize this conversation history") {
				return true
			}
		}
		return false
	}
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "r1"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "r2"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
		streamStep{
			match: matchSummarize,
			events: []provider.StreamEvent{
				{Type: provider.EventTextDelta, Text: "op-override-summary"},
				{Type: provider.EventDone, StopReason: "end_turn"},
			},
		},
	)
	// Default strategy is trim; op requests summarize.
	eng := engine.New(engine.Options{
		SessionID:       "session-op-override",
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

	eng.Ops() <- protocol.Compact{Strategy: protocol.CompactionStrategySummarize}
	var completed protocol.CompactionCompleted
	deadline := time.After(5 * time.Second)
	for completed.Reason == "" {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.CompactionCompleted:
				completed = ev
			case protocol.EngineError:
				t.Fatalf("EngineError: %s", ev.Message)
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}
	if completed.Strategy != protocol.CompactionStrategySummarize || completed.Summary != "op-override-summary" {
		t.Fatalf("completed = %#v", completed)
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
				if ev.Strategy != protocol.CompactionStrategyTrim && ev.Strategy != "" {
					t.Fatalf("default strategy = %q, want trim", ev.Strategy)
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

func TestThresholdCompactionSummarize(t *testing.T) {
	matchSummarize := func(req provider.Request) bool {
		for _, m := range req.Messages {
			if m.Role == provider.RoleUser && strings.Contains(m.Text, "Summarize this conversation history") {
				return true
			}
		}
		return false
	}
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "a"},
			{Type: provider.EventDone, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 100, OutputTokens: 10}},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "b"},
			{Type: provider.EventDone, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 900, OutputTokens: 50}},
		}},
		streamStep{
			match: matchSummarize,
			events: []provider.StreamEvent{
				{Type: provider.EventTextDelta, Text: "threshold-summary"},
				{Type: provider.EventDone, StopReason: "end_turn"},
			},
		},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "c"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	eng := engine.New(engine.Options{
		SessionID:           "session-threshold-summarize",
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
		CompactionStrategy:  protocol.CompactionStrategySummarize,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "seed-a"}
	waitCompactTurn(t, eng)
	eng.Ops() <- protocol.UserInput{Text: "seed-b"}
	waitCompactTurn(t, eng)

	eng.Ops() <- protocol.UserInput{Text: "seed-c"}
	var completed protocol.CompactionCompleted
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.CompactionCompleted:
				completed = ev
			case protocol.TurnCompleted:
				if completed.Reason != protocol.CompactionReasonThreshold {
					t.Fatalf("expected threshold compaction, got %#v", completed)
				}
				if completed.Strategy != protocol.CompactionStrategySummarize {
					t.Fatalf("strategy = %q", completed.Strategy)
				}
				if completed.Summary != "threshold-summary" {
					t.Fatalf("summary = %q", completed.Summary)
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

func TestCompactEmitsResidueWithMarkedDecisionAndPins(t *testing.T) {
	dir := t.TempDir()
	store, err := ledger.Open(dir, "proj-residue")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Append(ledger.AppendInput{
		Kind:          ledger.KindDecision,
		Statement:     "prefer structured residue over free-text-only compact markers",
		Confidence:    ledger.ConfidenceHigh,
		AuthorSession: "session-residue",
		AuthorRoot:    "session-residue",
	}); err != nil {
		t.Fatal(err)
	}

	mem := openTestMemory(t)
	if err := mem.Put("pref.pin", "PINNED_MEMORY_MARKER", []string{memory.TagPreference}); err != nil {
		t.Fatal(err)
	}

	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "DECISION: keep source ids on every residual item"},
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
		SessionID:       "session-residue",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		KeepUserTurns:   1,
		Ledger:          store,
		Memory:          mem,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.SetContextControls{
		PinKinds: []string{protocol.PromptLayerMemory},
		SetPin:   true,
	}
	// Drain controls event.
	deadline := time.After(2 * time.Second)
drainControls:
	for {
		select {
		case ev := <-eng.Events():
			if _, ok := ev.(protocol.ContextControlsSelected); ok {
				break drainControls
			}
		case <-deadline:
			t.Fatal("timeout waiting for context controls")
		}
	}

	eng.Ops() <- protocol.UserInput{Text: "first\nFACT: residue schema is versioned"}
	waitCompactTurn(t, eng)
	eng.Ops() <- protocol.UserInput{Text: "second"}
	waitCompactTurn(t, eng)

	eng.Ops() <- protocol.Compact{}
	var completed protocol.CompactionCompleted
	deadline = time.After(3 * time.Second)
	for completed.Reason == "" {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.CompactionCompleted:
				completed = ev
			case protocol.EngineError:
				t.Fatalf("unexpected EngineError: %s", ev.Message)
			}
		case <-deadline:
			t.Fatal("timed out waiting for compaction")
		}
	}
	if completed.Residue == nil {
		t.Fatal("expected residue on CompactionCompleted")
	}
	res := completed.Residue
	if res.SchemaVersion != protocol.CompactionResidueSchemaVersion {
		t.Fatalf("schema = %q", res.SchemaVersion)
	}
	// Ledger decision must not be silently discarded.
	foundLedger := false
	for _, d := range res.Decisions {
		if strings.Contains(d.Text, "structured residue") {
			foundLedger = true
			if d.LedgerID == "" || !stringSliceContains(d.SourceIDs, "ledger:"+d.LedgerID) {
				t.Fatalf("ledger decision missing provenance: %#v", d)
			}
		}
		if strings.Contains(d.Text, "source ids") {
			if !stringSliceContains(d.SourceIDs, "hist:1") && len(d.SourceIDs) == 0 {
				t.Fatalf("marked decision missing source ids: %#v", d)
			}
		}
	}
	if !foundLedger {
		t.Fatalf("ledger decision missing from residue: %#v", res.Decisions)
	}
	foundFact := false
	for _, f := range res.Facts {
		if strings.Contains(f.Text, "versioned") {
			foundFact = true
			if !stringSliceContains(f.SourceIDs, "hist:0") {
				t.Fatalf("fact sources = %v", f.SourceIDs)
			}
		}
	}
	if !foundFact {
		t.Fatalf("fact missing: %#v", res.Facts)
	}
	if !stringSliceContains(res.PinnedKinds, protocol.PromptLayerMemory) {
		t.Fatalf("pinned kinds = %v", res.PinnedKinds)
	}

	// Rebuild skeleton is usable for continue.
	skel := engine.RebuildPromptSkeleton(res)
	if !strings.Contains(skel, "structured residue") {
		t.Fatalf("rebuild missing decision: %q", skel)
	}

	// Next turn: marker carries residue; pin layer still in system prompt.
	eng.Ops() <- protocol.UserInput{Text: "third"}
	req := waitCompactTurnRequest(t, eng, prov)
	foundMarker := false
	for _, m := range req.Messages {
		if m.Role == provider.RoleUser && strings.HasPrefix(m.Text, "[Prior conversation compacted") {
			foundMarker = true
			if !strings.Contains(m.Text, "structured residue") {
				t.Fatalf("compact marker missing ledger decision: %q", m.Text)
			}
		}
	}
	if !foundMarker {
		t.Fatalf("compact marker missing in %#v", req.Messages)
	}
	if !strings.Contains(req.System, "PINNED_MEMORY_MARKER") {
		t.Fatal("pinned memory layer must survive compaction")
	}
}

func TestSummarizeCompactIncludesResidueSummary(t *testing.T) {
	matchSummarize := func(req provider.Request) bool {
		if len(req.Tools) != 0 {
			return false
		}
		for _, m := range req.Messages {
			if m.Role == provider.RoleUser && strings.Contains(m.Text, "Summarize this conversation history") {
				return true
			}
		}
		return false
	}
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "DECISION: measure summarize vs trim"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "r2"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
		streamStep{
			match: matchSummarize,
			events: []provider.StreamEvent{
				{Type: provider.EventTextDelta, Text: "SUMMARY_BODY_xyz"},
				{Type: provider.EventDone, StopReason: "end_turn"},
			},
		},
	)
	eng := engine.New(engine.Options{
		SessionID:          "session-residue-sum",
		Select:             func(string) (provider.Provider, string, error) { return prov, "cheap-model", nil },
		InitialProvider:    "scripted",
		Registry:           tool.NewRegistry(),
		WorkDir:            t.TempDir(),
		Rules:              []permission.Ruleset{permission.Defaults()},
		KeepUserTurns:      1,
		CompactionStrategy: protocol.CompactionStrategySummarize,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "first"}
	waitCompactTurn(t, eng)
	eng.Ops() <- protocol.UserInput{Text: "second"}
	waitCompactTurn(t, eng)

	eng.Ops() <- protocol.Compact{}
	var completed protocol.CompactionCompleted
	deadline := time.After(5 * time.Second)
	for completed.Reason == "" {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.CompactionCompleted:
				completed = ev
			case protocol.EngineError:
				// summarize fallback noise is ok only if we still get completed
				if !strings.Contains(ev.Message, "summarize") {
					t.Fatalf("EngineError: %s", ev.Message)
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for summarize compaction")
		}
	}
	if completed.Strategy != protocol.CompactionStrategySummarize {
		t.Fatalf("strategy = %q", completed.Strategy)
	}
	if completed.Residue == nil {
		t.Fatal("expected residue")
	}
	if completed.Residue.Summary != "SUMMARY_BODY_xyz" && completed.Summary != "SUMMARY_BODY_xyz" {
		t.Fatalf("summary missing: completed=%#v residue=%#v", completed.Summary, completed.Residue)
	}
	found := false
	for _, d := range completed.Residue.Decisions {
		if strings.Contains(d.Text, "summarize vs trim") {
			found = true
		}
	}
	if !found {
		t.Fatalf("decision missing under summarize: %#v", completed.Residue.Decisions)
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
