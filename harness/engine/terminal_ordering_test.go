package engine

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

type terminalOrderingProvider struct {
	mu                    sync.Mutex
	steps                 [][]provider.StreamEvent
	requests              chan provider.Request
	delayFirstTurnCleanup int
	heldCancels           []context.CancelFunc
}

func (p *terminalOrderingProvider) Name() string { return "terminal-ordering" }

func (p *terminalOrderingProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	first := len(p.steps) == 2 && p.delayFirstTurnCleanup > 0
	step := p.steps[0]
	p.steps = p.steps[1:]
	p.mu.Unlock()
	if first {
		p.heldCancels = make([]context.CancelFunc, 0, p.delayFirstTurnCleanup)
		for range p.delayFirstTurnCleanup {
			_, cancel := context.WithCancel(ctx)
			p.heldCancels = append(p.heldCancels, cancel)
		}
	}

	cloned := req
	cloned.Messages = append([]provider.Message(nil), req.Messages...)
	p.requests <- cloned
	stream := make(chan provider.StreamEvent, len(step))
	for _, event := range step {
		stream <- event
	}
	close(stream)
	return stream, nil
}

func TestUserInputImmediatelyAfterTurnCompletedStartsNextTurn(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(2)
	defer runtime.GOMAXPROCS(previousProcs)

	prov := &terminalOrderingProvider{
		steps: [][]provider.StreamEvent{
			{{Type: provider.EventTextDelta, Text: "first answer"}, {Type: provider.EventDone, StopReason: "end_turn"}},
			{{Type: provider.EventTextDelta, Text: "second answer"}, {Type: provider.EventDone, StopReason: "end_turn"}},
		},
		requests:              make(chan provider.Request, 2),
		delayFirstTurnCleanup: 500000,
	}
	eng := New(Options{
		Select:          func(string) (provider.Provider, string, error) { return prov, "test-model", nil },
		InitialProvider: "terminal-ordering",
		Registry:        tool.NewRegistry(),
	})
	eng.ops = make(chan protocol.Op)
	eng.events = make(chan protocol.Event)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	receiveEngineEvent(t, eng.events, func(event protocol.Event) bool {
		_, ok := event.(protocol.AgentSelected)
		return ok
	})
	eng.ops <- protocol.UserInput{Text: "first question"}
	firstRequest := receiveProviderRequestWhileDrainingEvents(t, prov.requests, eng.events)
	if len(firstRequest.Messages) != 1 || firstRequest.Messages[0].Role != provider.RoleUser || firstRequest.Messages[0].Text != "first question" {
		t.Fatalf("first provider history = %#v, want first user message", firstRequest.Messages)
	}

	receiveEngineEvent(t, eng.events, func(event protocol.Event) bool {
		_, ok := event.(protocol.TurnCompleted)
		return ok
	})
	eng.ops <- protocol.UserInput{Text: "second question"}

	secondRequest := receiveProviderRequestWhileDrainingEvents(t, prov.requests, eng.events)

	want := []provider.Message{
		{Role: provider.RoleUser, Text: "first question"},
		{Role: provider.RoleAssistant, Text: "first answer"},
		{Role: provider.RoleUser, Text: "second question"},
	}
	if len(secondRequest.Messages) != len(want) {
		t.Fatalf("second provider history = %#v, want %#v", secondRequest.Messages, want)
	}
	for i := range want {
		if secondRequest.Messages[i].Role != want[i].Role || secondRequest.Messages[i].Text != want[i].Text {
			t.Errorf("second provider history message %d = %#v, want %#v", i, secondRequest.Messages[i], want[i])
		}
	}
}

func TestFinishingTurnJoinsBeforeProcessingNextOp(t *testing.T) {
	prov := &terminalOrderingProvider{
		steps: [][]provider.StreamEvent{
			{{Type: provider.EventDone, StopReason: "end_turn"}},
		},
		requests: make(chan provider.Request, 1),
	}
	eng := New(Options{Registry: tool.NewRegistry()})
	eng.prov = prov
	eng.provName = prov.Name()
	eng.model = "test-model"
	eng.events = make(chan protocol.Event)
	turnDone := make(chan struct{})
	eng.turnDone = turnDone
	finishing := make(chan struct{})
	eng.turnFinishing = finishing
	turnCtx, turnCancel := context.WithCancel(context.Background())
	defer turnCancel()
	eng.turnCancel = turnCancel

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	receiveEngineEvent(t, eng.events, func(event protocol.Event) bool {
		_, ok := event.(protocol.AgentSelected)
		return ok
	})

	releaseTurnDone := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseTurnDone) }) }
	defer release()
	go func() {
		eng.emit(protocol.TurnCompleted{StopReason: "end_turn"})
		close(finishing)
		<-releaseTurnDone
		close(turnDone)
	}()
	receiveEngineEvent(t, eng.events, func(event protocol.Event) bool {
		_, ok := event.(protocol.TurnCompleted)
		return ok
	})
	select {
	case <-finishing:
	case <-time.After(2 * time.Second):
		t.Fatal("finishing signal did not close after TurnCompleted emission")
	}
	if turnCtx.Err() != nil {
		t.Fatalf("turn context ended before turnDone closed: %v", turnCtx.Err())
	}

	eng.ops <- protocol.UserInput{Text: "queued next turn"}
	select {
	case req := <-prov.requests:
		t.Fatalf("queued op reached provider before turnDone closed: %#v", req)
	case event := <-eng.events:
		t.Fatalf("queued op emitted %T before turnDone closed: %#v", event, event)
	case <-time.After(100 * time.Millisecond):
	}

	release()
	request := receiveProviderRequestWhileDrainingEvents(t, prov.requests, eng.events)
	if len(request.Messages) != 1 || request.Messages[0].Role != provider.RoleUser || request.Messages[0].Text != "queued next turn" {
		t.Fatalf("provider history after turn join = %#v, want queued user message", request.Messages)
	}
}

func receiveEngineEvent(t *testing.T, events <-chan protocol.Event, match func(protocol.Event) bool) protocol.Event {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("Events closed before expected event")
			}
			if match(event) {
				return event
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for engine event")
		}
	}
}

func receiveProviderRequest(t *testing.T, requests <-chan provider.Request) provider.Request {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for provider request")
		return provider.Request{}
	}
}

func receiveProviderRequestWhileDrainingEvents(t *testing.T, requests <-chan provider.Request, events <-chan protocol.Event) provider.Request {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case request := <-requests:
			return request
		case event, ok := <-events:
			if !ok {
				t.Fatal("Events closed before provider request")
			}
			if engineErr, ok := event.(protocol.EngineError); ok {
				t.Fatalf("UserInput emitted EngineError instead of reaching provider: %s", engineErr.Message)
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for provider request")
		}
	}
}
