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

// recordingProvider captures the Requests the engine builds so tests can
// assert on what would actually be sent, then completes the turn immediately.
type recordingProvider struct {
	mu       sync.Mutex
	requests []provider.Request
}

func (p *recordingProvider) Name() string { return "recording" }

func (p *recordingProvider) Stream(_ context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	ch := make(chan provider.StreamEvent, 2)
	ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: "ok"}
	ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
	close(ch)
	return ch, nil
}

func (p *recordingProvider) lastEffort(t *testing.T) provider.Effort {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		t.Fatal("provider received no requests")
	}
	return p.requests[len(p.requests)-1].Effort
}

func newRecordingEngine(t *testing.T, opts engine.Options) (*engine.Engine, *recordingProvider, context.CancelFunc) {
	t.Helper()
	rec := &recordingProvider{}
	opts.Select = func(string) (provider.Provider, string, error) { return rec, "rec-model", nil }
	opts.InitialProvider = "recording"
	if opts.Registry == nil {
		opts.Registry = tool.NewRegistry()
	}
	if opts.WorkDir == "" {
		opts.WorkDir = t.TempDir()
	}
	// Permit sandbox degrade so CI without bwrap is not fail-closed by #1030.
	// Prefer AllowDegrade over forcing SandboxMode off so yolo+sandbox checks
	// still exercise the real dial.
	if !opts.SandboxAllowDegrade && strings.TrimSpace(opts.SandboxMode) == "" {
		opts.SandboxAllowDegrade = true
	}
	eng := engine.New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	go eng.Run(ctx)
	return eng, rec, cancel
}

// waitForEvent drains events until pred matches or the deadline passes.
func waitForEvent(t *testing.T, eng *engine.Engine, pred func(protocol.Event) bool) protocol.Event {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for event")
			return nil
		case ev := <-eng.Events():
			if pred(ev) {
				return ev
			}
		}
	}
}

func TestSetEffortConfirmsAndReachesTheProvider(t *testing.T) {
	const sessionID = "session-effort"
	eng, rec, cancel := newRecordingEngine(t, engine.Options{SessionID: sessionID})
	defer cancel()

	eng.Ops() <- protocol.SetEffort{Level: protocol.EffortXHigh}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.EffortSelected)
		return ok && sel.Level == protocol.EffortXHigh
	})
	selected := event.(protocol.EffortSelected)
	if selected.Correlation != (protocol.Correlation{SessionID: sessionID}) {
		t.Errorf("EffortSelected correlation = %#v, want session only", selected.Correlation)
	}

	eng.Ops() <- protocol.UserInput{Text: "hello"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})
	if got := rec.lastEffort(t); got != provider.EffortXHigh {
		t.Errorf("request effort = %q, want xhigh", got)
	}
}

func TestInitialEffortAppliesAtStartup(t *testing.T) {
	eng, rec, cancel := newRecordingEngine(t, engine.Options{InitialEffort: protocol.EffortLow})
	defer cancel()

	eng.Ops() <- protocol.UserInput{Text: "hello"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})
	if got := rec.lastEffort(t); got != provider.EffortLow {
		t.Errorf("request effort = %q, want low", got)
	}
}

// TestUnsetEffortSendsNothing is the degradation guarantee: a user who never
// touches /effort must not have a level invented for them.
func TestUnsetEffortSendsNothing(t *testing.T) {
	eng, rec, cancel := newRecordingEngine(t, engine.Options{})
	defer cancel()

	eng.Ops() <- protocol.UserInput{Text: "hello"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})
	if got := rec.lastEffort(t); got != provider.EffortDefault {
		t.Errorf("request effort = %q, want unset", got)
	}
}

func TestUnknownEffortIsRejectedWithoutChangingState(t *testing.T) {
	eng, rec, cancel := newRecordingEngine(t, engine.Options{InitialEffort: protocol.EffortHigh})
	defer cancel()

	eng.Ops() <- protocol.SetEffort{Level: protocol.Effort("turbo")}
	ev := waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.EngineError)
		return ok
	})
	if msg := ev.(protocol.EngineError).Message; !strings.Contains(msg, "turbo") {
		t.Errorf("error message = %q, want it to name the bad level", msg)
	}

	eng.Ops() <- protocol.UserInput{Text: "hello"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})
	if got := rec.lastEffort(t); got != provider.EffortHigh {
		t.Errorf("request effort = %q, want the previous level (high) preserved", got)
	}
}

func TestSetEffortDuringActiveTurnEmitsSessionOnlyCorrelatedError(t *testing.T) {
	const sessionID = "session-active-effort"
	prov := newScriptedProvider(streamStep{stream: func(ctx context.Context) <-chan provider.StreamEvent {
		ch := make(chan provider.StreamEvent)
		go func() {
			defer close(ch)
			<-ctx.Done()
		}()
		return ch
	}})
	eng := engine.New(engine.Options{
		SessionID:       sessionID,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "keep the turn active"}
	select {
	case <-prov.requests:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the active provider request")
	}

	eng.Ops() <- protocol.SetEffort{Level: protocol.EffortMax}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		err, ok := ev.(protocol.EngineError)
		return ok && strings.Contains(err.Message, "cannot change effort")
	})
	engineErr := event.(protocol.EngineError)
	if engineErr.Correlation != (protocol.Correlation{SessionID: sessionID}) {
		t.Errorf("EngineError correlation = %#v, want session only", engineErr.Correlation)
	}
}

// TestAgentEffortPinOverridesTheConfiguredDefault mirrors how an agent's
// provider/model pins behave.
func TestAgentEffortPinOverridesTheConfiguredDefault(t *testing.T) {
	const sessionID = "session-agent-effort"
	eng, rec, cancel := newRecordingEngine(t, engine.Options{
		SessionID:     sessionID,
		InitialEffort: protocol.EffortLow,
		Agents: []engine.Agent{
			{Name: "build", Prompt: "b"},
			{Name: "deep", Prompt: "d", Effort: protocol.EffortMax},
		},
	})
	defer cancel()

	eng.Ops() <- protocol.SelectAgent{Name: "deep"}
	event := waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.EffortSelected)
		return ok && sel.Level == protocol.EffortMax
	})
	selected := event.(protocol.EffortSelected)
	if selected.Correlation != (protocol.Correlation{SessionID: sessionID}) {
		t.Errorf("EffortSelected correlation = %#v, want session only", selected.Correlation)
	}

	eng.Ops() <- protocol.UserInput{Text: "hello"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})
	if got := rec.lastEffort(t); got != provider.EffortMax {
		t.Errorf("request effort = %q, want the agent's max pin", got)
	}
}

// TestAgentWithoutEffortPinLeavesTheDialAlone: only agents that actually
// declare an effort should move it.
func TestAgentWithoutEffortPinLeavesTheDialAlone(t *testing.T) {
	eng, rec, cancel := newRecordingEngine(t, engine.Options{
		InitialEffort: protocol.EffortLow,
		Agents: []engine.Agent{
			{Name: "build", Prompt: "b"},
			{Name: "plain", Prompt: "p"},
		},
	})
	defer cancel()

	eng.Ops() <- protocol.SelectAgent{Name: "plain"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		sel, ok := ev.(protocol.AgentSelected)
		return ok && sel.Name == "plain"
	})

	eng.Ops() <- protocol.UserInput{Text: "hello"}
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.TurnCompleted)
		return ok
	})
	if got := rec.lastEffort(t); got != provider.EffortLow {
		t.Errorf("request effort = %q, want the configured low to survive", got)
	}
}

// TestReasoningDeltaEmittedWhenProviderSendsDisplayableCoT covers the
// frontend path: opaque replay bytes still land on the message, and readable
// prose streams as protocol.ReasoningDelta (not TextDelta).
func TestReasoningDeltaEmittedWhenProviderSendsDisplayableCoT(t *testing.T) {
	block := `{"type":"thinking","thinking":"consider the edge cases","signature":"sig=="}`
	rec := &reasoningProvider{block: block}
	eng := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return rec, "m", nil },
		InitialProvider: "reasoning",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "turn"}
	var deltas []protocol.ReasoningDelta
	var sawText bool
	waitForEvent(t, eng, func(ev protocol.Event) bool {
		switch e := ev.(type) {
		case protocol.ReasoningDelta:
			deltas = append(deltas, e)
		case protocol.TextDelta:
			if e.Text == "ok" {
				sawText = true
			}
		case protocol.TurnCompleted:
			return true
		}
		return false
	})
	if !sawText {
		t.Fatal("missing TextDelta for the final answer")
	}
	if len(deltas) != 1 || deltas[0].Text != "consider the edge cases" {
		t.Fatalf("ReasoningDelta = %#v, want one with thinking prose", deltas)
	}
}

// TestReasoningBlocksAreCarriedIntoTheNextRequest proves the replay path end
// to end through the engine: what a provider emits as EventReasoning must come
// back on the assistant message of the following request, byte-identical.
func TestReasoningBlocksAreCarriedIntoTheNextRequest(t *testing.T) {
	rec := &reasoningProvider{block: `{"type":"thinking","thinking":"","signature":"sig=="}`}
	eng := engine.New(engine.Options{
		Select:          func(string) (provider.Provider, string, error) { return rec, "m", nil },
		InitialProvider: "reasoning",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	for i := 0; i < 2; i++ {
		eng.Ops() <- protocol.UserInput{Text: "turn"}
		waitForEvent(t, eng, func(ev protocol.Event) bool {
			_, ok := ev.(protocol.TurnCompleted)
			return ok
		})
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	second := rec.requests[1]
	var assistant *provider.Message
	for i := range second.Messages {
		if second.Messages[i].Role == provider.RoleAssistant {
			assistant = &second.Messages[i]
			break
		}
	}
	if assistant == nil {
		t.Fatal("second request has no assistant message")
	}
	if len(assistant.Reasoning) != 1 {
		t.Fatalf("assistant reasoning blocks = %d, want 1", len(assistant.Reasoning))
	}
	if string(assistant.Reasoning[0]) != rec.block {
		t.Errorf("reasoning block = %s, want %s", assistant.Reasoning[0], rec.block)
	}
}

type reasoningProvider struct {
	mu       sync.Mutex
	block    string
	requests []provider.Request
}

func (p *reasoningProvider) Name() string { return "reasoning" }

func (p *reasoningProvider) Stream(_ context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	ch := make(chan provider.StreamEvent, 3)
	ch <- provider.StreamEvent{Type: provider.EventReasoning, Reasoning: []byte(p.block)}
	ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: "ok"}
	ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "end_turn"}
	close(ch)
	return ch, nil
}
