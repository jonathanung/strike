package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/internal/enginebind"
	"github.com/jonathanung/strike-cli/internal/tools"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func questionToolCall(id string, questions ...map[string]any) provider.ToolCall {
	args, _ := json.Marshal(map[string]any{"questions": questions})
	return provider.ToolCall{ID: id, Name: "question", Args: args}
}

func enterPlanToolCall(id string) provider.ToolCall {
	return provider.ToolCall{ID: id, Name: "enter_plan_mode", Args: json.RawMessage(`{}`)}
}

// TestQuestionFlow mirrors permission tests: tool AskUser → QuestionAsked →
// QuestionReply → tool end → turn complete.
func TestQuestionFlow(t *testing.T) {
	call := questionToolCall("qcall-1", map[string]any{
		"id":       "pref",
		"question": "Ship it?",
		"options": []map[string]any{
			{"label": "Yes", "description": "go"},
			{"label": "No", "description": "stop"},
		},
	})
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("after answer"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "sess-q",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewQuestion()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "ask me"}

	var (
		sawAsked, sawResolved, sawToolEnd, sawCompleted bool
		requestID                                       string
		toolOutput                                      string
	)
	deadline := time.After(10 * time.Second)
	for !sawCompleted {
		select {
		case <-deadline:
			t.Fatalf("timed out; asked=%v resolved=%v end=%v", sawAsked, sawResolved, sawToolEnd)
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.QuestionAsked:
				sawAsked = true
				requestID = ev.RequestID
				if len(ev.Questions) != 1 || ev.Questions[0].Question != "Ship it?" {
					t.Errorf("questions = %#v", ev.Questions)
				}
				eng.Ops() <- protocol.QuestionReply{
					RequestID: ev.RequestID,
					Answers:   []string{"Yes"},
				}
			case protocol.QuestionResolved:
				sawResolved = true
				if ev.RequestID != requestID {
					t.Errorf("resolved id = %q, want %q", ev.RequestID, requestID)
				}
			case protocol.ToolCallEnd:
				if ev.CallID != "qcall-1" {
					continue
				}
				sawToolEnd = true
				toolOutput = ev.Output
				if ev.IsError {
					t.Errorf("tool error: %s", ev.Output)
				}
			case protocol.TurnCompleted:
				sawCompleted = true
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
	if !sawAsked {
		t.Error("no QuestionAsked")
	}
	if !sawResolved {
		t.Error("no QuestionResolved")
	}
	if !sawToolEnd {
		t.Error("no ToolCallEnd for question")
	}
	if !strings.Contains(toolOutput, "Yes") {
		t.Errorf("tool output missing answer: %q", toolOutput)
	}
}

// TestQuestionMultiFlow: one question tool call with 2+ prompts; all answers
// return in a single QuestionReply and appear in the tool output.
func TestQuestionMultiFlow(t *testing.T) {
	call := questionToolCall("qcall-multi",
		map[string]any{
			"id":       "ship",
			"question": "Ship it?",
			"options": []map[string]any{
				{"label": "Yes"},
				{"label": "No"},
			},
		},
		map[string]any{
			"id":       "channel",
			"question": "Which channel?",
			"options": []map[string]any{
				{"label": "stable"},
				{"label": "beta"},
			},
		},
		map[string]any{
			"question": "Any release notes?",
		},
	)
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("after multi answers"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "sess-q-multi",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewQuestion()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "ask several things"}

	var (
		sawAsked, sawResolved, sawToolEnd, sawCompleted bool
		toolOutput                                      string
	)
	deadline := time.After(10 * time.Second)
	for !sawCompleted {
		select {
		case <-deadline:
			t.Fatalf("timed out; asked=%v resolved=%v end=%v", sawAsked, sawResolved, sawToolEnd)
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.QuestionAsked:
				sawAsked = true
				if len(ev.Questions) != 3 {
					t.Fatalf("questions = %d, want 3: %#v", len(ev.Questions), ev.Questions)
				}
				if ev.Questions[0].Question != "Ship it?" ||
					ev.Questions[1].Question != "Which channel?" ||
					ev.Questions[2].Question != "Any release notes?" {
					t.Errorf("questions = %#v", ev.Questions)
				}
				eng.Ops() <- protocol.QuestionReply{
					RequestID: ev.RequestID,
					Answers:   []string{"Yes", "beta", "n/a"},
				}
			case protocol.QuestionResolved:
				sawResolved = true
			case protocol.ToolCallEnd:
				if ev.CallID != "qcall-multi" {
					continue
				}
				sawToolEnd = true
				toolOutput = ev.Output
				if ev.IsError {
					t.Errorf("tool error: %s", ev.Output)
				}
				if ev.Title != "Asked 3 questions" {
					t.Errorf("title = %q, want Asked 3 questions", ev.Title)
				}
			case protocol.TurnCompleted:
				sawCompleted = true
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
	if !sawAsked {
		t.Error("no QuestionAsked")
	}
	if !sawResolved {
		t.Error("no QuestionResolved")
	}
	if !sawToolEnd {
		t.Error("no ToolCallEnd for multi question")
	}
	for _, want := range []string{"Yes", "beta", "n/a", "Ship it?", "Which channel?", "Any release notes?"} {
		if !strings.Contains(toolOutput, want) {
			t.Errorf("tool output missing %q: %q", want, toolOutput)
		}
	}
}

func TestChildQuestionReplyRouting(t *testing.T) {
	const childPrompt = "ask in child"
	taskCall := taskToolCall("task-q", childPrompt)
	childQ := questionToolCall("child-q-1", map[string]any{
		"question": "Child question?",
	})
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := toolCallStep(childQ)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("child after question")
			s.match = matchToolResult("child-q-1")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent after task")
			s.match = matchToolResult("task-q")
			return s
		}(),
		childCompletedNudgeStep("parent ack question child"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "parent-q-route",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewQuestion()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "delegate question"}

	var events []protocol.Event
	var sawChildAsk bool
	var parentDone, childDone bool
	guard := time.NewTimer(10 * time.Second)
	defer guard.Stop()
	for !(parentDone && childDone) {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed early")
			}
			events = append(events, ev)
			switch ev := ev.(type) {
			case protocol.QuestionAsked:
				if ev.ParentSessionID == "parent-q-route" || ev.Depth > 0 {
					sawChildAsk = true
				}
				eng.Ops() <- protocol.QuestionReply{
					RequestID: ev.RequestID,
					Answers:   []string{"from-parent"},
				}
			case protocol.ChildCompleted:
				childDone = true
			case protocol.TurnCompleted:
				parentDone = true
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		case <-guard.C:
			t.Fatalf("timed out; events=%v", summarizeEvents(events))
		}
	}
	if !sawChildAsk {
		t.Fatalf("never saw child-correlated QuestionAsked; events=%v", summarizeEvents(events))
	}
	if n := countEvents[protocol.ChildStarted](events); n != 1 {
		t.Errorf("ChildStarted = %d, want 1", n)
	}
	if n := countEvents[protocol.ChildCompleted](events); n != 1 {
		t.Errorf("ChildCompleted = %d, want 1", n)
	}
	var taskEnd *protocol.ToolCallEnd
	for _, ev := range events {
		if end, ok := ev.(protocol.ToolCallEnd); ok && end.CallID == "task-q" {
			e := end
			taskEnd = &e
		}
	}
	if taskEnd == nil {
		t.Fatal("missing task ToolCallEnd")
	}
	if taskEnd.IsError {
		t.Errorf("task failed after question reply: %s", taskEnd.Output)
	}
	if !strings.Contains(taskEnd.Output, "Started child session") {
		t.Errorf("task output = %q, want started notice", taskEnd.Output)
	}
	var childSummary string
	for _, ev := range events {
		if c, ok := ev.(protocol.ChildCompleted); ok {
			childSummary = c.Summary
		}
	}
	if !strings.Contains(childSummary, "child after question") {
		t.Errorf("ChildCompleted summary = %q, want child after question", childSummary)
	}
}

// TestQuestionDismissInterruptsTurn: empty QuestionReply (esc dismiss) settles
// the tool as an error and ends the turn with stopReason interrupted.
func TestQuestionDismissInterruptsTurn(t *testing.T) {
	call := questionToolCall("qcall-dismiss", map[string]any{
		"question": "Continue?",
		"options": []map[string]any{
			{"label": "Yes"},
			{"label": "No"},
		},
	})
	// Only one stream step: a second stream would mean the turn continued.
	prov := newScriptedProvider(toolCallStep(call))
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "sess-q-dismiss",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewQuestion()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "ask me"}

	var (
		sawToolEnd bool
		toolOut    string
		stopReason string
	)
	deadline := time.After(10 * time.Second)
	for stopReason == "" {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for interrupted turn")
		case ev := <-eng.Events():
			switch ev := ev.(type) {
			case protocol.QuestionAsked:
				eng.Ops() <- protocol.QuestionReply{RequestID: ev.RequestID, Answers: nil}
			case protocol.ToolCallEnd:
				if ev.CallID != "qcall-dismiss" {
					continue
				}
				sawToolEnd = true
				toolOut = ev.Output
				if !ev.IsError {
					t.Error("dismissed question should be an error tool result")
				}
			case protocol.TurnCompleted:
				stopReason = ev.StopReason
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		}
	}
	if !sawToolEnd {
		t.Error("missing ToolCallEnd for dismissed question")
	}
	if !strings.Contains(strings.ToLower(toolOut), "dismiss") &&
		!strings.Contains(strings.ToLower(toolOut), "rejected") {
		t.Errorf("tool output = %q, want dismiss/reject feedback", toolOut)
	}
	if stopReason != "interrupted" {
		t.Errorf("stop reason = %q, want interrupted", stopReason)
	}
}

// TestDeferredSwitchAgentAfterEnterPlanMode: enter_plan_mode queues the switch;
// AgentSelected{plan} fires after the tool batch (before the next Stream), so
// it may appear mid-turn before TurnCompleted.
func TestDeferredSwitchAgentAfterEnterPlanMode(t *testing.T) {
	call := enterPlanToolCall("enter-1")
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("planning next"),
	)
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "sess-plan",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tools.NewEnterPlanMode()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		Agents: []engine.Agent{
			{Name: "build", Description: "build"},
			{Name: "plan", Description: "plan"},
		},
		InitialAgent: "build",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Drain startup AgentSelected (build).
	startupDeadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-eng.Events():
			if a, ok := ev.(protocol.AgentSelected); ok && a.Name == "build" {
				goto ready
			}
		case <-startupDeadline:
			t.Fatal("timed out waiting for startup AgentSelected build")
		}
	}
ready:

	eng.Ops() <- protocol.UserInput{Text: "enter plan"}

	var (
		events                            []protocol.Event
		sawTurnCompleted, sawPlanSelected bool
		toolEndedIdx, planSelectedIdx     int
		toolEnded                         bool
	)
	deadline := time.After(10 * time.Second)
	for !(sawTurnCompleted && sawPlanSelected) {
		select {
		case <-deadline:
			t.Fatalf("timed out; events=%v completed=%v plan=%v", summarizeEvents(events), sawTurnCompleted, sawPlanSelected)
		case ev := <-eng.Events():
			events = append(events, ev)
			switch ev := ev.(type) {
			case protocol.ToolCallEnd:
				if ev.CallID == "enter-1" {
					toolEnded = true
					toolEndedIdx = len(events) - 1
					if ev.IsError {
						t.Fatalf("enter_plan_mode failed: %s", ev.Output)
					}
				}
			case protocol.TurnCompleted:
				sawTurnCompleted = true
			case protocol.AgentSelected:
				if ev.Name == "plan" {
					sawPlanSelected = true
					planSelectedIdx = len(events) - 1
				}
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			case protocol.QuestionAsked:
				// enter_plan_mode should not ask the user
				t.Fatalf("unexpected QuestionAsked: %#v", ev)
			}
		}
	}
	if !toolEnded {
		t.Error("enter_plan_mode ToolCallEnd missing")
	}
	// Switch applies after the tool batch that queued it, before the next Stream.
	if planSelectedIdx <= toolEndedIdx {
		t.Errorf("AgentSelected(plan) index %d must be after enter_plan_mode ToolCallEnd index %d; events=%v",
			planSelectedIdx, toolEndedIdx, summarizeEvents(events))
	}
}
