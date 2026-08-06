package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// TestSoloVerifyCmdPass: Options.Verify cmd gate exit 0 attaches verified report
// on TurnCompleted and emits verification.started/completed timeline events.
func TestSoloVerifyCmdPass(t *testing.T) {
	prov := newScriptedProvider(completedStep("solo claimed done"))
	eng := engine.New(engine.Options{
		SessionID:       "solo-vg-pass",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		Verify: []tool.VerifyGate{
			{Kind: "cmd", Value: "true", Description: "always-pass"},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "do work"}
	events := collectEventsUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		for _, ev := range evs {
			if _, ok := ev.(protocol.TurnCompleted); ok {
				return true
			}
		}
		return false
	})

	var started []protocol.VerificationStarted
	var completed []protocol.VerificationCompleted
	var turns []protocol.TurnCompleted
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.VerificationStarted:
			started = append(started, ev)
		case protocol.VerificationCompleted:
			completed = append(completed, ev)
		case protocol.TurnCompleted:
			turns = append(turns, ev)
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if len(started) != 1 || started[0].Scope != protocol.VerificationScopeTurn || started[0].GateCount != 1 {
		t.Fatalf("started = %#v", started)
	}
	if len(completed) != 1 || !completed[0].Report.Passed || !completed[0].Report.Verified || !completed[0].Report.Claimed {
		t.Fatalf("completed = %#v", completed)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %d", len(turns))
	}
	tc := turns[0]
	if tc.StopReason != "end_turn" {
		t.Fatalf("stopReason = %q", tc.StopReason)
	}
	if tc.Verification == nil || !tc.Verification.Passed || !tc.Verification.Claimed || !tc.Verification.Verified {
		t.Fatalf("turn verification = %#v", tc.Verification)
	}
	if len(tc.Verification.Checks) != 1 || !tc.Verification.Checks[0].Passed {
		t.Fatalf("checks = %#v", tc.Verification.Checks)
	}
	if tc.Verification.Env.SessionID != "solo-vg-pass" || tc.Verification.Env.WorkDir == "" {
		t.Fatalf("env = %#v", tc.Verification.Env)
	}
}

// TestSoloVerifyCannotSelfCertify: model claims success + cmd fails →
// TurnCompleted still claims end_turn but Verification.Passed/Verified false.
func TestSoloVerifyCannotSelfCertify(t *testing.T) {
	// Model prose that "looks verified" is irrelevant — only the gate matters.
	prov := newScriptedProvider(completedStep("all tests green, verification: make test passed"))
	eng := engine.New(engine.Options{
		SessionID:       "solo-vg-fail",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		Verify: []tool.VerifyGate{
			{Kind: "cmd", Value: "false", Description: "must-fail"},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "claim done"}
	events := collectEventsUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		for _, ev := range evs {
			if _, ok := ev.(protocol.TurnCompleted); ok {
				return true
			}
		}
		return false
	})

	var tc *protocol.TurnCompleted
	var vc *protocol.VerificationCompleted
	for i := range events {
		switch ev := events[i].(type) {
		case protocol.TurnCompleted:
			tc = &ev
		case protocol.VerificationCompleted:
			vc = &ev
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if tc == nil {
		t.Fatal("missing TurnCompleted")
	}
	if tc.StopReason != "end_turn" {
		// Claimed completion still uses end_turn; verified is on the report.
		t.Fatalf("stopReason = %q (want end_turn claimed)", tc.StopReason)
	}
	if tc.Verification == nil {
		t.Fatal("expected verification report on TurnCompleted")
	}
	if tc.Verification.Passed || tc.Verification.Verified {
		t.Fatalf("model must not self-certify: %+v", tc.Verification)
	}
	if !tc.Verification.Claimed {
		t.Fatal("Claimed should be true for end_turn")
	}
	if vc == nil || vc.Report.Passed {
		t.Fatalf("VerificationCompleted = %#v", vc)
	}
	if !strings.Contains(tc.Verification.Summary, "must-fail") && !strings.Contains(tc.Verification.Summary, "verification failed") {
		t.Fatalf("summary = %q", tc.Verification.Summary)
	}
}

// TestSoloVerifySchemaGate: schema:handoff without structured handoff fails
// offline (solo path has no child handoff payload).
func TestSoloVerifySchemaGate(t *testing.T) {
	prov := newScriptedProvider(completedStep(`{"summary":"looks structured"}`))
	eng := engine.New(engine.Options{
		SessionID:       "solo-vg-schema",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		Verify: []tool.VerifyGate{
			{Kind: "schema", Value: "handoff"},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "finish"}
	events := collectEventsUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		for _, ev := range evs {
			if _, ok := ev.(protocol.TurnCompleted); ok {
				return true
			}
		}
		return false
	})

	var tc *protocol.TurnCompleted
	for i := range events {
		if ev, ok := events[i].(protocol.TurnCompleted); ok {
			tc = &ev
		}
	}
	if tc == nil || tc.Verification == nil {
		t.Fatalf("turn = %#v", tc)
	}
	// Solo path does not supply HandoffView → schema gate fails.
	if tc.Verification.Passed || tc.Verification.Verified {
		t.Fatalf("schema without handoff must fail: %+v", tc.Verification)
	}
	if tc.Verification.Claimed != true {
		t.Fatal("still claimed")
	}
}

// TestSoloVerifySkippedWithoutGates: no Options.Verify → no report / events.
func TestSoloVerifySkippedWithoutGates(t *testing.T) {
	prov := newScriptedProvider(completedStep("plain done"))
	eng := engine.New(engine.Options{
		SessionID:       "solo-vg-none",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hi"}
	events := collectEventsUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		for _, ev := range evs {
			if _, ok := ev.(protocol.TurnCompleted); ok {
				return true
			}
		}
		return false
	})
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.VerificationStarted, protocol.VerificationCompleted:
			t.Fatalf("unexpected verification event: %#v", ev)
		case protocol.TurnCompleted:
			if ev.Verification != nil {
				t.Fatalf("unexpected report: %#v", ev.Verification)
			}
		}
	}
}

// TestVerificationGatesEmitTimelineEvents: child path also emits
// verification.started/completed (shared with solo).
func TestVerificationGatesEmitTimelineEvents(t *testing.T) {
	const childPrompt = "gated-child-timeline"
	handoffJSON := `{"summary":"implemented","files_changed":[],"verification":"i pinky-swear","findings":[],"blockers":[]}`
	taskCall := taskToolCallWith("task-vg-tl", map[string]any{
		"prompt": childPrompt,
		"verify": []map[string]any{
			{"kind": "cmd", "value": "true", "description": "pass"},
		},
	})
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep(handoffJSON)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent done")
			s.match = matchToolResult("task-vg-tl")
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "lead-vg-tl",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "delegate gated"}
	events := drainAndReply(t, eng, 20*time.Second)

	var started, completed int
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.VerificationStarted:
			started++
			if ev.Scope != protocol.VerificationScopeChild {
				t.Fatalf("scope = %q", ev.Scope)
			}
		case protocol.VerificationCompleted:
			completed++
			if !ev.Report.Passed {
				t.Fatalf("report = %#v", ev.Report)
			}
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if started != 1 || completed != 1 {
		t.Fatalf("started=%d completed=%d events=%v", started, completed, summarizeEvents(events))
	}
}

func collectEventsUntil(t *testing.T, eng *engine.Engine, timeout time.Duration, done func([]protocol.Event) bool) []protocol.Event {
	t.Helper()
	deadline := time.After(timeout)
	var out []protocol.Event
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout collecting events; got %v", summarizeEvents(out))
		case ev, ok := <-eng.Events():
			if !ok {
				return out
			}
			out = append(out, ev)
			if done(out) {
				// Drain a short window for trailing verification events that
				// may race after TurnCompleted (should not; gates run first).
				drainDeadline := time.After(50 * time.Millisecond)
				for {
					select {
					case <-drainDeadline:
						return out
					case ev, ok := <-eng.Events():
						if !ok {
							return out
						}
						out = append(out, ev)
					}
				}
			}
		}
	}
}
