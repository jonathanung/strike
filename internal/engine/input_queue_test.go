package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
)

// TestUserInputQueuedDuringTurnDrainsFIFOAfterCompletion verifies the engine
// accept path: UserInput while a turn is active is buffered (not rejected)
// and starts in order after the active turn ends. Queue survives Interrupt.
func TestUserInputQueuedDuringTurnDrainsFIFOAfterCompletion(t *testing.T) {
	releaseFirst := make(chan struct{})
	prov := newScriptedProvider(
		streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
			ch := make(chan provider.StreamEvent, 1)
			go func() {
				defer close(ch)
				select {
				case <-ctx.Done():
					ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "interrupted"}
				case <-releaseFirst:
					ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
				}
			}()
			return ch
		}},
		completedStep("second-reply"),
		completedStep("third-reply"),
	)
	eng := newTestEngine(t, prov)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "first"}
	_ = receiveRequest(t, prov.requests)

	// Mid-turn inputs must not error; they queue.
	eng.Ops() <- protocol.UserInput{Text: "second"}
	eng.Ops() <- protocol.UserInput{Text: "third"}

	// Ensure no EngineError for "already running" while first turn holds.
	deadline := time.After(150 * time.Millisecond)
drainEarly:
	for {
		select {
		case ev := <-eng.Events():
			if err, ok := ev.(protocol.EngineError); ok {
				if strings.Contains(err.Message, "already running") {
					t.Fatalf("mid-turn UserInput rejected: %s", err.Message)
				}
			}
		case <-deadline:
			break drainEarly
		}
	}

	close(releaseFirst)
	_ = waitForTurnCompleted(t, eng.Events())

	req2 := receiveRequest(t, prov.requests)
	if len(req2.Messages) == 0 || req2.Messages[len(req2.Messages)-1].Text != "second" {
		t.Fatalf("second turn messages = %#v, want trailing user second", req2.Messages)
	}
	_ = waitForTurnCompleted(t, eng.Events())

	req3 := receiveRequest(t, prov.requests)
	if len(req3.Messages) == 0 || req3.Messages[len(req3.Messages)-1].Text != "third" {
		t.Fatalf("third turn messages = %#v, want trailing user third", req3.Messages)
	}
	_ = waitForTurnCompleted(t, eng.Events())
}

func TestUserInputQueueSurvivesInterrupt(t *testing.T) {
	releaseFirst := make(chan struct{})
	firstCanceled := make(chan struct{}, 1)
	prov := newScriptedProvider(
		streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
			ch := make(chan provider.StreamEvent, 1)
			go func() {
				defer close(ch)
				select {
				case <-ctx.Done():
					firstCanceled <- struct{}{}
					ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "interrupted"}
				case <-releaseFirst:
					ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
				}
			}()
			return ch
		}},
		completedStep("after-interrupt"),
	)
	eng := newTestEngine(t, prov)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "running"}
	_ = receiveRequest(t, prov.requests)
	eng.Ops() <- protocol.UserInput{Text: "queued through interrupt"}
	eng.Ops() <- protocol.Interrupt{}

	select {
	case <-firstCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("first turn was not interrupted")
	}
	_ = waitForTurnCompleted(t, eng.Events())

	req := receiveRequest(t, prov.requests)
	found := false
	for _, msg := range req.Messages {
		if msg.Role == provider.RoleUser && msg.Text == "queued through interrupt" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("provider request after interrupt = %#v, want queued user text", req.Messages)
	}
	_ = waitForTurnCompleted(t, eng.Events())
}

func TestUserInputQueueFullEmitsError(t *testing.T) {
	release := make(chan struct{})
	prov := newScriptedProvider(streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
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
	}})
	eng := newTestEngine(t, prov)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hold"}
	_ = receiveRequest(t, prov.requests)

	// Fill to maxPendingUserInputs (32); the next should error.
	for i := 0; i < 32; i++ {
		eng.Ops() <- protocol.UserInput{Text: "q"}
	}
	eng.Ops() <- protocol.UserInput{Text: "overflow"}

	ev := receiveEvent(t, eng.Events(), func(ev protocol.Event) bool {
		err, ok := ev.(protocol.EngineError)
		return ok && strings.Contains(err.Message, "input queue full")
	})
	if ev == nil {
		t.Fatal("expected queue full EngineError")
	}
	close(release)
}
