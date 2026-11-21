package engine_test

import (
	"context"
	"testing"

	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/pkg/protocol"
)

// userMessageThroughTurn returns the UserMessage seen before the next
// TurnCompleted.
func userMessageThroughTurn(t *testing.T, events <-chan protocol.Event) protocol.UserMessage {
	t.Helper()
	_, evs := collectThroughTurnCompleted(t, events)
	for _, ev := range evs {
		if um, ok := ev.(protocol.UserMessage); ok {
			return um
		}
	}
	t.Fatalf("no UserMessage before TurnCompleted in %#v", evs)
	return protocol.UserMessage{}
}

// TestUserInputLoopIDReachesUserMessage covers #1827: a /loop firing's
// LoopID rides onto the transcript's UserMessage, both when the session is
// idle and when the firing queues behind a busy turn; an ordinary send
// carries none.
func TestUserInputLoopIDReachesUserMessage(t *testing.T) {
	release := make(chan struct{})
	prov := newScriptedProvider(
		completedStep("idle-reply"),
		completedStep("plain-reply"),
		streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
			ch := make(chan provider.StreamEvent, 1)
			go func() {
				defer close(ch)
				select {
				case <-ctx.Done():
				case <-release:
				}
				ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
			}()
			return ch
		}},
		completedStep("queued-reply"),
	)
	eng := newTestEngine(t, prov)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	tests := []struct {
		name string
		in   protocol.UserInput
		want string
	}{
		{name: "idle firing", in: protocol.UserInput{Text: "check ci", LoopID: "l1"}, want: "l1"},
		{name: "ordinary send", in: protocol.UserInput{Text: "hello"}, want: ""},
	}
	for _, tc := range tests {
		eng.Ops() <- tc.in
		_ = receiveRequest(t, prov.requests)
		um := userMessageThroughTurn(t, eng.Events())
		if um.Text != tc.in.Text || um.LoopID != tc.want {
			t.Fatalf("%s: UserMessage = %+v, want text %q loopId %q", tc.name, um, tc.in.Text, tc.want)
		}
	}

	// Busy turn: the firing queues and keeps its loop id when it starts.
	eng.Ops() <- protocol.UserInput{Text: "long task"}
	_ = receiveRequest(t, prov.requests)
	eng.Ops() <- protocol.UserInput{Text: "check ci", LoopID: "l2"}
	close(release)
	if um := userMessageThroughTurn(t, eng.Events()); um.LoopID != "" {
		t.Fatalf("busy turn UserMessage = %+v, want no loopId", um)
	}
	_ = receiveRequest(t, prov.requests)
	if um := userMessageThroughTurn(t, eng.Events()); um.Text != "check ci" || um.LoopID != "l2" {
		t.Fatalf("queued firing UserMessage = %+v, want loopId l2", um)
	}
}
