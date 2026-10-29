package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

func sendNowEngine(t *testing.T, prov provider.Provider, sessionID string) *engine.Engine {
	t.Helper()
	return engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(steerStubTool{}),
		WorkDir:         t.TempDir(),
		Rules: []permission.Ruleset{{
			{Permission: "*", Pattern: "*", Action: permission.Allow},
		}},
		SessionID: sessionID,
	})
}

func queueSnapshot(t *testing.T, eng *engine.Engine, want int) protocol.InputQueued {
	t.Helper()
	ev := receiveEvent(t, eng.Events(), func(ev protocol.Event) bool {
		q, ok := ev.(protocol.InputQueued)
		return ok && len(q.Items) == want
	})
	if ev == nil {
		t.Fatalf("expected a %d-item input.queued snapshot", want)
	}
	return ev.(protocol.InputQueued)
}

// TestInputQueueSendNowSteersQueuedItemIntoRunningTurn promotes the second
// queued item while a tool call holds the turn: the item leaves the FIFO,
// lands in the running turn as a steer at the next boundary (TurnSteered),
// and the item ahead of it stays queued and drains afterwards. The promoted
// item is delivered exactly once.
func TestInputQueueSendNowSteersQueuedItemIntoRunningTurn(t *testing.T) {
	gate := make(chan struct{})
	prov := newScriptedProvider(
		// First stream: request a tool, hold until gate so we can act mid-turn.
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
		// Second stream of the same turn, after the promoted steer lands.
		streamStep{match: func(req provider.Request) bool {
			for i := len(req.Messages) - 1; i >= 0; i-- {
				if req.Messages[i].Role == provider.RoleUser && strings.Contains(req.Messages[i].Text, "beta") {
					return true
				}
			}
			return false
		}, events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "steered-ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		}},
		// The item that stayed queued drains as the next turn.
		completedStep("alpha-turn"),
	)
	eng := sendNowEngine(t, prov, "sess-send-now-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "start"}
	_ = receiveRequest(t, prov.requests)

	eng.Ops() <- protocol.UserInput{Text: "alpha"}
	eng.Ops() <- protocol.UserInput{Text: "beta"}
	snap := queueSnapshot(t, eng, 2)
	if snap.Items[0].Text != "alpha" || snap.Items[1].Text != "beta" {
		t.Fatalf("queue = %#v", snap.Items)
	}
	betaID := snap.Items[1].ID

	eng.Ops() <- protocol.InputQueueSendNow{ID: betaID}
	left := queueSnapshot(t, eng, 1)
	if left.Items[0].Text != "alpha" {
		t.Fatalf("queue after send now = %#v", left.Items)
	}

	// A repeat send-now for the claimed id errors; it cannot double-send.
	eng.Ops() <- protocol.InputQueueSendNow{ID: betaID}
	ev := receiveEvent(t, eng.Events(), func(ev protocol.Event) bool {
		err, ok := ev.(protocol.EngineError)
		return ok && strings.Contains(err.Message, "not in the queue")
	})
	if ev == nil {
		t.Fatal("repeat send now did not error")
	}

	close(gate)

	steered := receiveEvent(t, eng.Events(), func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnSteered)
		return ok
	})
	if ts, ok := steered.(protocol.TurnSteered); !ok || ts.Mode != protocol.SteerModeBoundary || !strings.Contains(ts.Text, "beta") {
		t.Fatalf("steered = %#v, want boundary steer of beta", steered)
	}
	req2 := receiveRequest(t, prov.requests)
	foundBeta := false
	for _, msg := range req2.Messages {
		if msg.Role == provider.RoleUser && msg.Text == "beta" {
			foundBeta = true
		}
	}
	if !foundBeta {
		t.Fatalf("second stream messages = %#v, want beta as a user message", req2.Messages)
	}
	_ = waitForTurnCompleted(t, eng.Events())

	// Alpha drains as its own turn; beta never starts a turn of its own.
	req3 := receiveRequest(t, prov.requests)
	if len(req3.Messages) == 0 || req3.Messages[len(req3.Messages)-1].Text != "alpha" {
		t.Fatalf("third turn messages = %#v, want trailing user alpha", req3.Messages)
	}
	_ = waitForTurnCompleted(t, eng.Events())

	select {
	case req := <-prov.requests:
		t.Fatalf("unexpected extra provider request: %#v", req.Messages)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestInputQueueSendNowAfterDrainIsRejected covers the race: the turn ends
// and the item drains as a normal turn before Send now arrives. The op must
// be a no-op error — the item is delivered exactly once, as the drain.
func TestInputQueueSendNowAfterDrainIsRejected(t *testing.T) {
	release := make(chan struct{})
	prov := newScriptedProvider(
		streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
			ch := make(chan provider.StreamEvent, 1)
			go func() {
				defer close(ch)
				select {
				case <-ctx.Done():
				case <-release:
					ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
				}
			}()
			return ch
		}},
		completedStep("alpha-turn"),
	)
	eng := sendNowEngine(t, prov, "sess-send-now-2")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hold"}
	_ = receiveRequest(t, prov.requests)
	eng.Ops() <- protocol.UserInput{Text: "alpha"}
	snap := queueSnapshot(t, eng, 1)
	alphaID := snap.Items[0].ID

	close(release)
	_ = waitForTurnCompleted(t, eng.Events())
	req := receiveRequest(t, prov.requests)
	if len(req.Messages) == 0 || req.Messages[len(req.Messages)-1].Text != "alpha" {
		t.Fatalf("drained turn = %#v, want trailing user alpha", req.Messages)
	}
	_ = waitForTurnCompleted(t, eng.Events())

	eng.Ops() <- protocol.InputQueueSendNow{ID: alphaID}
	ev := receiveEvent(t, eng.Events(), func(ev protocol.Event) bool {
		err, ok := ev.(protocol.EngineError)
		return ok && strings.Contains(err.Message, "not in the queue")
	})
	if ev == nil {
		t.Fatal("send now after drain did not error")
	}
	select {
	case req := <-prov.requests:
		t.Fatalf("send now after drain delivered the item again: %#v", req.Messages)
	case <-time.After(200 * time.Millisecond):
	}
}
