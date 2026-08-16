package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestSessionCostEnvelopeHardStop(t *testing.T) {
	// $1/MTok input → 1_000_000 tokens = $1.00 exhausts a $1 ceiling.
	// First stream crosses 50% and 100% in one report when priced high enough.
	// Use two streams: first hits 50%, second exhausts.
	//
	// Stream 1: 500k input → $0.50 → level 50
	// Stream 2 would need a tool loop; simpler: one stream with 1M tokens → 100%.
	prov := newScriptedProvider(streamStep{events: []provider.StreamEvent{
		{Type: provider.EventTextDelta, Text: "burning budget"},
		{Type: provider.EventDone, StopReason: "end_turn", Usage: &provider.Usage{
			InputTokens:  1_000_000,
			OutputTokens: 0,
		}},
	}})

	estimate := func(_, _ string, u provider.Usage) float64 {
		return float64(u.InputTokens+u.OutputTokens) / 1e6 * 1.0 // $1 / MTok
	}

	eng := engine.New(engine.Options{
		SessionID:         "sess-budget",
		Select:            func(string) (provider.Provider, string, error) { return prov, "m", nil },
		InitialProvider:   "scripted",
		InitialModel:      "m",
		Registry:          tool.NewRegistry(),
		WorkDir:           t.TempDir(),
		Rules:             []permission.Ruleset{permission.Defaults()},
		MaxSessionCostUSD: 1.0,
		EstimateUsageCost: estimate,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	_ = waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.ModelSelected)
		return ok
	})

	eng.Ops() <- protocol.UserInput{Text: "spend it"}
	_, events := collectThroughTurnCompleted(t, eng.Events())

	var (
		saw50, saw100 bool
		exhaustedWarn bool
		engineErr     bool
		stopReason    string
	)
	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.SessionBudgetWarning:
			switch e.Level {
			case protocol.SessionBudgetLevel50:
				saw50 = true
			case protocol.SessionBudgetLevel80:
				// may skip if jump is large
			case protocol.SessionBudgetLevel100:
				saw100 = true
			}
			if e.Exhausted {
				exhaustedWarn = true
			}
			if e.Kind != protocol.SessionBudgetKindCostUSD {
				t.Fatalf("kind=%q", e.Kind)
			}
			if e.MaxCostUSD != 1.0 {
				t.Fatalf("max=%v", e.MaxCostUSD)
			}
		case protocol.EngineError:
			if e.Code == protocol.ErrorCodeBudgetExhausted {
				engineErr = true
			}
		case protocol.TurnCompleted:
			stopReason = e.StopReason
		}
	}
	if !saw100 || !exhaustedWarn {
		t.Fatalf("want 100%% exhausted warning; saw50=%v saw100=%v exhausted=%v events=%v",
			saw50, saw100, exhaustedWarn, summarizeEvents(events))
	}
	if !engineErr {
		t.Fatalf("want EngineError budget_exhausted; events=%v", summarizeEvents(events))
	}
	if stopReason != "budget_exhausted" {
		t.Fatalf("stopReason=%q want budget_exhausted", stopReason)
	}

	// Follow-up input is rejected without starting a turn.
	eng.Ops() <- protocol.UserInput{Text: "again"}
	deadline := time.After(2 * time.Second)
	var rejected bool
	for !rejected {
		select {
		case ev := <-eng.Events():
			if err, ok := ev.(protocol.EngineError); ok && err.Code == protocol.ErrorCodeBudgetExhausted {
				rejected = true
			}
			if _, ok := ev.(protocol.TurnStarted); ok {
				t.Fatal("should not start a new turn after envelope exhausted")
			}
		case <-deadline:
			t.Fatal("timed out waiting for reject")
		}
	}
}

func TestMaxTurnTokensHardStop(t *testing.T) {
	prov := newScriptedProvider(streamStep{events: []provider.StreamEvent{
		{Type: provider.EventTextDelta, Text: "tok"},
		{Type: provider.EventDone, StopReason: "end_turn", Usage: &provider.Usage{
			InputTokens:  80,
			OutputTokens: 30, // used=110 > max 100
		}},
	}})

	eng := engine.New(engine.Options{
		SessionID:       "sess-turn-tok",
		Select:          func(string) (provider.Provider, string, error) { return prov, "m", nil },
		InitialProvider: "scripted",
		InitialModel:    "m",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		MaxTurnTokens:   100,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	_ = waitForEvent(t, eng, func(ev protocol.Event) bool {
		_, ok := ev.(protocol.ModelSelected)
		return ok
	})

	eng.Ops() <- protocol.UserInput{Text: "hi"}
	_, events := collectThroughTurnCompleted(t, eng.Events())

	var sawTurnKind, engineErr bool
	var stopReason string
	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.SessionBudgetWarning:
			if e.Kind == protocol.SessionBudgetKindTurnTokens && e.Exhausted {
				sawTurnKind = true
			}
		case protocol.EngineError:
			if e.Code == protocol.ErrorCodeBudgetExhausted {
				engineErr = true
			}
		case protocol.TurnCompleted:
			stopReason = e.StopReason
		}
	}
	if !sawTurnKind || !engineErr || stopReason != "budget_exhausted" {
		t.Fatalf("turn token stop failed: warn=%v err=%v stop=%q events=%v",
			sawTurnKind, engineErr, stopReason, summarizeEvents(events))
	}
}

func TestProtocolSessionBudgetWarningRoundTrip(t *testing.T) {
	ev := protocol.SessionBudgetWarning{
		Correlation: protocol.Correlation{SessionID: "s1", TurnID: "t1"},
		Level:       protocol.SessionBudgetLevel80,
		Kind:        protocol.SessionBudgetKindCostUSD,
		CostUSD:     0.8,
		MaxCostUSD:  1.0,
		Ratio:       0.8,
		Message:     "session cost budget 80%: $0.8 / $1 USD",
	}
	env, err := protocol.Wrap(ev)
	if err != nil {
		t.Fatal(err)
	}
	if env.Type != "session.budget_warning" {
		t.Fatalf("type=%q", env.Type)
	}
	got, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	w, ok := got.(protocol.SessionBudgetWarning)
	if !ok {
		t.Fatalf("got %T", got)
	}
	if w.Level != protocol.SessionBudgetLevel80 || w.CostUSD != 0.8 || w.MaxCostUSD != 1.0 {
		t.Fatalf("decoded %#v", w)
	}
}
