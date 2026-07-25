package engine_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

type fastRecordingProvider struct {
	mu       sync.Mutex
	requests []provider.Request
}

func (p *fastRecordingProvider) Name() string { return "fast-recording" }

func (p *fastRecordingProvider) Stream(_ context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	stream := make(chan provider.StreamEvent, 1)
	stream <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
	close(stream)
	return stream, nil
}

func (p *fastRecordingProvider) Requests() []provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.Request(nil), p.requests...)
}

type blockingFastProvider struct {
	requests chan provider.Request
}

func (p *blockingFastProvider) Name() string { return "blocking-fast" }

func (p *blockingFastProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	p.requests <- req
	stream := make(chan provider.StreamEvent)
	go func() {
		defer close(stream)
		<-ctx.Done()
	}()
	return stream, nil
}

func TestSetFastConfirmsWithSessionOnlyCorrelation(t *testing.T) {
	const sessionID = "fast-session"
	eng := engine.New(engine.Options{
		SessionID:       sessionID,
		InitialProvider: "recording",
		Select: func(string) (provider.Provider, string, error) {
			return &fastRecordingProvider{}, "model", nil
		},
		Registry: tool.NewRegistry(),
		WorkDir:  t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.SetFast{Enabled: true}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		selected, ok := ev.(protocol.FastSelected)
		return ok && selected.Enabled
	})
	selected := event.(protocol.FastSelected)
	if selected.Correlation != (protocol.Correlation{SessionID: sessionID}) {
		t.Errorf("FastSelected correlation = %#v, want session only", selected.Correlation)
	}
}

func TestSetFastDuringActiveTurnEmitsSessionOnlyErrorWithoutConfirmation(t *testing.T) {
	const sessionID = "fast-active-session"
	prov := &blockingFastProvider{requests: make(chan provider.Request, 1)}
	eng := engine.New(engine.Options{
		SessionID:       sessionID,
		InitialProvider: "blocking",
		Select: func(string) (provider.Provider, string, error) {
			return prov, "model", nil
		},
		Registry: tool.NewRegistry(),
		WorkDir:  t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "keep the turn active"}
	select {
	case <-prov.requests:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for active provider request")
	}

	eng.Ops() <- protocol.SetFast{Enabled: true}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		err, ok := ev.(protocol.EngineError)
		return ok && strings.Contains(err.Message, "cannot change fast")
	})
	engineErr := event.(protocol.EngineError)
	if engineErr.Correlation != (protocol.Correlation{SessionID: sessionID}) {
		t.Errorf("EngineError correlation = %#v, want session only", engineErr.Correlation)
	}
	select {
	case ev := <-eng.Events():
		if _, ok := ev.(protocol.FastSelected); ok {
			t.Fatalf("active-turn SetFast emitted FastSelected: %#v", ev)
		}
	case <-time.After(100 * time.Millisecond):
	}
}

func TestFastPriorityReachesProviderAndSticksAcrossSelectionsAndTurns(t *testing.T) {
	first := &fastRecordingProvider{}
	second := &fastRecordingProvider{}
	eng := engine.New(engine.Options{
		InitialProvider: "first",
		Select: func(name string) (provider.Provider, string, error) {
			switch name {
			case "first":
				return first, "first-default", nil
			case "second":
				return second, "second-default", nil
			default:
				return nil, "", nil
			}
		},
		Registry: tool.NewRegistry(),
		WorkDir:  t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.SetEffort{Level: protocol.EffortLow}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		selected, ok := ev.(protocol.EffortSelected)
		return ok && selected.Level == protocol.EffortLow
	})
	eng.Ops() <- protocol.SetFast{Enabled: true}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		selected, ok := ev.(protocol.FastSelected)
		return ok && selected.Enabled
	})
	for _, input := range []string{"first turn", "second turn"} {
		eng.Ops() <- protocol.UserInput{Text: input}
		waitForEvent(t, eng, func(ev protocol.Event) bool {
			_, ok := ev.(protocol.TurnCompleted)
			return ok
		})
	}
	eng.Ops() <- protocol.SelectModel{Provider: "second", Model: "second-model"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		selected, ok := ev.(protocol.ModelSelected)
		return ok && selected.Provider == "second" && selected.Model == "second-model"
	})
	eng.Ops() <- protocol.UserInput{Text: "third turn"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})

	for name, requests := range map[string][]provider.Request{
		"first":  first.Requests(),
		"second": second.Requests(),
	} {
		if len(requests) == 0 {
			t.Errorf("%s provider received no requests", name)
		}
		for i, request := range requests {
			if !request.Priority {
				t.Errorf("%s request %d Priority = false, want true", name, i)
			}
			if request.Effort != provider.EffortLow {
				t.Errorf("%s request %d Effort = %q, want low", name, i, request.Effort)
			}
		}
	}
}
