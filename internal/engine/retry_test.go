package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestStreamRetriesTransientFailureWithNewAttemptIdentity(t *testing.T) {
	prov := &scriptedProvider{steps: []streamStep{
		{err: errors.New("unexpected status 429: rate limited")},
		{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "recovered"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	}}
	eng := engine.New(engine.Options{
		SessionID:          "session-retry",
		Select:             func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:    "scripted",
		Registry:           tool.NewRegistry(),
		WorkDir:            t.TempDir(),
		Rules:              []permission.Ruleset{permission.Defaults()},
		StreamRetryBackoff: func(int) time.Duration { return 0 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "hi"}

	var retry protocol.ProviderRetrying
	var text protocol.TextDelta
	var completed protocol.TurnCompleted
	deadline := time.After(3 * time.Second)
	for completed.StopReason == "" {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.ProviderRetrying:
				retry = ev
			case protocol.TextDelta:
				text = ev
			case protocol.TurnCompleted:
				completed = ev
			case protocol.EngineError:
				t.Fatalf("unexpected EngineError: %s", ev.Message)
			}
		case <-deadline:
			t.Fatal("timed out waiting for recovered turn")
		}
	}
	if prov.callCount() != 2 {
		t.Fatalf("Stream calls = %d, want 2", prov.callCount())
	}
	if retry.Attempt != 1 || retry.ProviderRequestID == "" || retry.TurnID == "" {
		t.Fatalf("ProviderRetrying correlation = %#v", retry.Correlation)
	}
	if retry.NextAttempt != 2 {
		t.Fatalf("NextAttempt = %d, want 2", retry.NextAttempt)
	}
	if text.Text != "recovered" {
		t.Fatalf("text = %q", text.Text)
	}
	if text.Attempt != 2 || text.ProviderRequestID == "" {
		t.Fatalf("success correlation = %#v", text.Correlation)
	}
	if text.ProviderRequestID == retry.ProviderRequestID {
		t.Fatalf("retry and success share providerRequestId %q", text.ProviderRequestID)
	}
	if text.TurnID != retry.TurnID {
		t.Fatalf("turnId changed across retry: %q vs %q", retry.TurnID, text.TurnID)
	}
	if completed.StopReason != "end_turn" {
		t.Fatalf("stop reason = %q", completed.StopReason)
	}
	if completed.ProviderRequestID != text.ProviderRequestID || completed.Attempt != 2 {
		t.Fatalf("TurnCompleted correlation = %#v, want success attempt", completed.Correlation)
	}
}

func TestStreamDoesNotRetryPermanentFailure(t *testing.T) {
	prov := &scriptedProvider{steps: []streamStep{
		{err: errors.New("invalid_request_error: bad schema")},
		{events: []provider.StreamEvent{{Type: provider.EventDone, StopReason: "end_turn"}}},
	}}
	eng := engine.New(engine.Options{
		SessionID:          "session-perm",
		Select:             func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:    "scripted",
		Registry:           tool.NewRegistry(),
		WorkDir:            t.TempDir(),
		StreamRetryBackoff: func(int) time.Duration { return 0 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "hi"}

	var failure protocol.EngineError
	var completed protocol.TurnCompleted
	var sawRetry bool
	deadline := time.After(2 * time.Second)
	for completed.StopReason == "" {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.ProviderRetrying:
				sawRetry = true
			case protocol.EngineError:
				failure = ev
			case protocol.TurnCompleted:
				completed = ev
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}
	if sawRetry {
		t.Fatal("permanent failure must not emit ProviderRetrying")
	}
	if prov.callCount() != 1 {
		t.Fatalf("Stream calls = %d, want 1", prov.callCount())
	}
	if failure.Message == "" || failure.Attempt != 1 {
		t.Fatalf("EngineError = %#v", failure)
	}
	if completed.StopReason != "error" {
		t.Fatalf("stop reason = %q", completed.StopReason)
	}
}

func TestIncompleteStreamIsRetried(t *testing.T) {
	prov := &scriptedProvider{steps: []streamStep{
		{events: []provider.StreamEvent{{Type: provider.EventTextDelta, Text: "partial"}}}, // no terminal
		{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	}}
	eng := engine.New(engine.Options{
		SessionID:          "session-incomplete",
		Select:             func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:    "scripted",
		Registry:           tool.NewRegistry(),
		WorkDir:            t.TempDir(),
		StreamRetryBackoff: func(int) time.Duration { return 0 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "hi"}

	var retryCount int
	var completed protocol.TurnCompleted
	deadline := time.After(3 * time.Second)
	for completed.StopReason == "" {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.ProviderRetrying:
				retryCount++
				if ev.Message != provider.ErrIncompleteStream.Error() {
					t.Fatalf("retry message = %q, want %q", ev.Message, provider.ErrIncompleteStream.Error())
				}
			case protocol.TurnCompleted:
				completed = ev
			case protocol.EngineError:
				t.Fatalf("unexpected EngineError: %s", ev.Message)
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}
	if retryCount != 1 {
		t.Fatalf("retry events = %d, want 1", retryCount)
	}
	if prov.callCount() != 2 {
		t.Fatalf("Stream calls = %d, want 2", prov.callCount())
	}
	if completed.Attempt != 2 {
		t.Fatalf("completed attempt = %d, want 2", completed.Attempt)
	}
}

func TestRetryExhaustionFailsTurn(t *testing.T) {
	prov := &scriptedProvider{steps: []streamStep{
		{err: errors.New("unexpected status 503: unavailable")},
		{err: errors.New("unexpected status 503: unavailable")},
		{err: errors.New("unexpected status 503: unavailable")},
	}}
	eng := engine.New(engine.Options{
		SessionID:          "session-exhaust",
		Select:             func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:    "scripted",
		Registry:           tool.NewRegistry(),
		WorkDir:            t.TempDir(),
		MaxStreamAttempts:  3,
		StreamRetryBackoff: func(int) time.Duration { return 0 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "hi"}

	var retries int
	var failure protocol.EngineError
	var completed protocol.TurnCompleted
	deadline := time.After(3 * time.Second)
	for completed.StopReason == "" {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.ProviderRetrying:
				retries++
			case protocol.EngineError:
				failure = ev
			case protocol.TurnCompleted:
				completed = ev
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}
	if retries != 2 {
		t.Fatalf("retries = %d, want 2", retries)
	}
	if prov.callCount() != 3 {
		t.Fatalf("Stream calls = %d, want 3", prov.callCount())
	}
	if failure.Attempt != 3 || failure.Message == "" {
		t.Fatalf("EngineError = %#v", failure)
	}
	if completed.Correlation != failure.Correlation || completed.StopReason != "error" {
		t.Fatalf("completed = %#v failure = %#v", completed, failure)
	}
}

func TestToolLoopStreamFailureDoesNotRerunCompletedTools(t *testing.T) {
	var bashRuns atomic.Int32
	bash := &countingBash{runs: &bashRuns}
	call := provider.ToolCall{ID: "c1", Name: "bash", Args: []byte(`{"command":"echo once"}`)}
	prov := &scriptedProvider{steps: []streamStep{
		{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &call},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		// First follow-up after tool fails transiently; must not re-exec bash.
		{err: errors.New("unexpected status 502: bad gateway")},
		{events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "done"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	}}
	eng := engine.New(engine.Options{
		SessionID:          "session-tool-boundary",
		Select:             func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider:    "scripted",
		Registry:           tool.NewRegistry(bash),
		WorkDir:            t.TempDir(),
		Rules:              []permission.Ruleset{permission.Defaults()},
		StreamRetryBackoff: func(int) time.Duration { return 0 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	eng.Ops() <- protocol.UserInput{Text: "run tool then retry"}

	var completed protocol.TurnCompleted
	deadline := time.After(5 * time.Second)
	for completed.StopReason == "" {
		select {
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.TurnCompleted:
				completed = ev
			case protocol.EngineError:
				t.Fatalf("unexpected EngineError: %s", ev.Message)
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}
	if bashRuns.Load() != 1 {
		t.Fatalf("bash executions = %d, want 1 (no duplicate side effects)", bashRuns.Load())
	}
	if prov.callCount() != 3 {
		t.Fatalf("Stream calls = %d, want 3", prov.callCount())
	}
	if completed.StopReason != "end_turn" {
		t.Fatalf("stop reason = %q", completed.StopReason)
	}
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
