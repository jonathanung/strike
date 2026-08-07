package engine_test
// Steer op race coverage (CI).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// steerStubTool is a no-permission tool for steer boundary tests.
type steerStubTool struct{}

func (steerStubTool) Name() string            { return "noop" }
func (steerStubTool) Description() string     { return "noop" }
func (steerStubTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (steerStubTool) Execute(context.Context, json.RawMessage, *tool.Context) (tool.Result, error) {
	return tool.Result{Output: "ok"}, nil
}

// TestSteerAtPreToolBoundary injects steer before the next Stream after tools.
func TestSteerAtPreToolBoundary(t *testing.T) {
	gate := make(chan struct{})
	prov := newScriptedProvider(
		// First stream: request a tool, hold until gate so we can steer mid-turn.
		streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
			ch := make(chan provider.StreamEvent, 4)
			go func() {
				defer close(ch)
				ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID: "c1", Name: "noop", Args: []byte(`{}`),
				}}
				ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "tool_use"}
				select {
				case <-ctx.Done():
				case <-gate:
				}
			}()
			return ch
		}},
		// Second stream after tool + steer boundary.
		streamStep{match: func(req provider.Request) bool {
			// Expect steer text as last user message.
			for i := len(req.Messages) - 1; i >= 0; i-- {
				if req.Messages[i].Role == provider.RoleUser && strings.Contains(req.Messages[i].Text, "steer-me") {
					return true
				}
			}
			return false
		}, events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "steered-ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)

	eng := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(steerStubTool{}),
		WorkDir:         t.TempDir(),
		Rules: []permission.Ruleset{{
			{Permission: "*", Pattern: "*", Action: permission.Allow},
		}},
		SessionID: "sess-steer-1",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "start"}
	_ = receiveRequest(t, prov.requests)

	// Steer while first stream/tool path is active.
	eng.Ops() <- protocol.Steer{Text: "steer-me", SessionID: "sess-steer-1"}

	// Unblock first stream consumer (already emitted tool_use).
	close(gate)

	var steered protocol.TurnSteered
	var completed protocol.TurnCompleted
	guard := time.NewTimer(3 * time.Second)
	defer guard.Stop()
	for completed.StopReason == "" {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("events closed")
			}
			switch e := ev.(type) {
			case protocol.TurnSteered:
				steered = e
			case protocol.TurnCompleted:
				completed = e
			case protocol.EngineError:
				t.Fatalf("engine error: %s", e.Message)
			}
		case <-guard.C:
			t.Fatal("timeout")
		}
	}
	if steered.Mode != protocol.SteerModeBoundary {
		t.Fatalf("mode = %q, want boundary", steered.Mode)
	}
	if !strings.Contains(steered.Text, "steer-me") {
		t.Fatalf("steered text = %q", steered.Text)
	}
}

// TestSteerCancelRestartOnFinalStream continues the turn when steer arrives
// during a no-tool final stream.
func TestSteerCancelRestartOnFinalStream(t *testing.T) {
	release := make(chan struct{})
	prov := newScriptedProvider(
		streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
			ch := make(chan provider.StreamEvent, 2)
			go func() {
				defer close(ch)
				select {
				case <-ctx.Done():
					ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "interrupted"}
					return
				case <-release:
				}
				ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: "partial"}
				ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
			}()
			return ch
		}},
		streamStep{match: func(req provider.Request) bool {
			for _, m := range req.Messages {
				if m.Role == provider.RoleUser && strings.Contains(m.Text, "redirect") {
					return true
				}
			}
			return false
		}, events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "after-steer"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	eng := newTestEngine(t, prov)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}
	_ = receiveRequest(t, prov.requests)
	eng.Ops() <- protocol.Steer{Text: "redirect"}
	close(release)

	var modes []string
	guard := time.NewTimer(3 * time.Second)
	defer guard.Stop()
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("closed")
			}
			switch e := ev.(type) {
			case protocol.TurnSteered:
				modes = append(modes, e.Mode)
			case protocol.TurnCompleted:
				if len(modes) == 0 {
					t.Fatal("expected TurnSteered before complete")
				}
				if modes[0] != protocol.SteerModeCancelRestart && modes[0] != protocol.SteerModeBoundary {
					t.Fatalf("modes = %v", modes)
				}
				return
			case protocol.EngineError:
				t.Fatalf("%s", e.Message)
			}
		case <-guard.C:
			t.Fatalf("timeout modes=%v", modes)
		}
	}
}

// TestSteerQueuedFallbackOnInterrupt queues steer when the turn is canceled
// before a boundary applies it.
func TestSteerQueuedFallbackOnInterrupt(t *testing.T) {
	hold := make(chan struct{})
	prov := newScriptedProvider(
		streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
			ch := make(chan provider.StreamEvent, 1)
			go func() {
				defer close(ch)
				select {
				case <-ctx.Done():
					ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "interrupted"}
				case <-hold:
					ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
				}
			}()
			return ch
		}},
		completedStep("queued-followup"),
	)
	eng := newTestEngine(t, prov)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "running"}
	_ = receiveRequest(t, prov.requests)
	eng.Ops() <- protocol.Steer{Text: "please-queue"}
	eng.Ops() <- protocol.Interrupt{}

	var sawFallback bool
	var sawSecondTurn bool
	guard := time.NewTimer(3 * time.Second)
	defer guard.Stop()
	turns := 0
	for turns < 2 {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("closed")
			}
			switch e := ev.(type) {
			case protocol.TurnSteered:
				if e.Mode == protocol.SteerModeQueuedFallback {
					sawFallback = true
				}
			case protocol.UserMessage:
				if strings.Contains(e.Text, "please-queue") {
					sawSecondTurn = true
				}
			case protocol.TurnCompleted:
				turns++
			}
		case <-guard.C:
			t.Fatalf("timeout fallback=%v second=%v turns=%d", sawFallback, sawSecondTurn, turns)
		}
	}
	if !sawFallback {
		t.Fatal("expected queued_fallback TurnSteered")
	}
	if !sawSecondTurn {
		t.Fatal("expected queued steer as next user message")
	}
	close(hold)
}

// TestSteerRejectsWhenIdle distinguishes steer from queue/user.input.
func TestSteerRejectsWhenIdle(t *testing.T) {
	prov := newScriptedProvider(completedStep("x"))
	eng := newTestEngine(t, prov)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.Steer{Text: "nope"}
	ev := receiveEvent(t, eng.Events(), func(ev protocol.Event) bool {
		_, ok := ev.(protocol.EngineError)
		return ok
	})
	err := ev.(protocol.EngineError)
	if !strings.Contains(err.Message, "no active turn") {
		t.Fatalf("message = %q", err.Message)
	}
	if err.Code != protocol.ErrorCodeInvalidArgs {
		t.Fatalf("code = %q", err.Code)
	}
}

// TestSteerRejectsWrongTurnID validates turn targeting.
func TestSteerRejectsWrongTurnID(t *testing.T) {
	hold := make(chan struct{})
	prov := newScriptedProvider(
		streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
			ch := make(chan provider.StreamEvent, 1)
			go func() {
				defer close(ch)
				select {
				case <-ctx.Done():
				case <-hold:
				}
				ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
			}()
			return ch
		}},
	)
	eng := newTestEngine(t, prov)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}
	_ = receiveRequest(t, prov.requests)
	eng.Ops() <- protocol.Steer{Text: "x", TurnID: "not-the-turn"}
	ev := receiveEvent(t, eng.Events(), func(ev protocol.Event) bool {
		e, ok := ev.(protocol.EngineError)
		return ok && strings.Contains(e.Message, "does not match active turn")
	})
	if ev == nil {
		t.Fatal("expected targeting error")
	}
	close(hold)
	_ = waitForTurnCompleted(t, eng.Events())
}

// TestSteerDoesNotDuplicateToolHistory ensures tool results stay paired.
func TestSteerDoesNotDuplicateToolHistory(t *testing.T) {
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
				ID: "t1", Name: "noop", Args: []byte(`{}`),
			}},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		streamStep{match: func(req provider.Request) bool {
			// Count tool results and ensure single result before steer user msg.
			toolResults := 0
			steerIdx := -1
			for i, m := range req.Messages {
				if m.Role == provider.RoleTool {
					toolResults++
				}
				if m.Role == provider.RoleUser && strings.Contains(m.Text, "after-tool") {
					steerIdx = i
				}
			}
			return toolResults == 1 && steerIdx > 0
		}, events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "done"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
	)
	eng := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(steerStubTool{}),
		WorkDir:         t.TempDir(),
		Rules: []permission.Ruleset{{
			{Permission: "*", Pattern: "*", Action: permission.Allow},
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "use tool"}
	// Wait until first stream claimed.
	_ = receiveRequest(t, prov.requests)
	eng.Ops() <- protocol.Steer{Text: "after-tool"}

	completed := waitForTurnCompleted(t, eng.Events())
	if completed.StopReason == "error" {
		t.Fatalf("stop = %s", completed.StopReason)
	}
	// Second stream must have been the steered one.
	if prov.callCount() < 2 {
		t.Fatalf("calls = %d", prov.callCount())
	}
}

// TestUserInputStillQueuesAlongsideSteer keeps queue vs steer distinct:
// steer applies inside the active turn; UserInput remains a later turn.
func TestUserInputStillQueuesAlongsideSteer(t *testing.T) {
	prov := newScriptedProvider(
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
				ID: "c1", Name: "noop", Args: []byte(`{}`),
			}},
			{Type: provider.EventDone, StopReason: "tool_use"},
		}},
		// Post-tool boundary applies steer then streams again.
		streamStep{match: func(req provider.Request) bool {
			for _, m := range req.Messages {
				if m.Role == provider.RoleUser && m.Text == "steer-text" {
					return true
				}
			}
			return false
		}, events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "steered"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
		// Queued user input is a separate turn after complete.
		completedStep("queued"),
	)
	eng := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(steerStubTool{}),
		WorkDir:         t.TempDir(),
		Rules: []permission.Ruleset{{
			{Permission: "*", Pattern: "*", Action: permission.Allow},
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "start"}
	_ = receiveRequest(t, prov.requests)
	eng.Ops() <- protocol.Steer{Text: "steer-text"}
	eng.Ops() <- protocol.UserInput{Text: "queued-later"}

	var sawSteer, sawQueued, sawSteerEvent bool
	turns := 0
	guard := time.NewTimer(4 * time.Second)
	defer guard.Stop()
	for turns < 2 {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("closed")
			}
			switch e := ev.(type) {
			case protocol.TurnSteered:
				if e.Mode == protocol.SteerModeBoundary {
					sawSteerEvent = true
				}
			case protocol.UserMessage:
				if e.Text == "steer-text" {
					sawSteer = true
				}
				if e.Text == "queued-later" {
					sawQueued = true
				}
			case protocol.TurnCompleted:
				turns++
			case protocol.EngineError:
				t.Fatalf("engine error: %s", e.Message)
			}
		case <-guard.C:
			t.Fatalf("timeout steer=%v queued=%v event=%v turns=%d", sawSteer, sawQueued, sawSteerEvent, turns)
		}
	}
	if !sawSteer || !sawSteerEvent {
		t.Fatal("expected in-turn steer application")
	}
	if !sawQueued {
		t.Fatal("expected queued UserInput as a later turn")
	}
}
