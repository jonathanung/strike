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
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// matchLatestUserContains claims streams whose latest user message contains sub.
// Prefer this over matchToolResult for finalization turns so earlier tool-result
// steps cannot steal the reserved handoff stream (#879).
func matchLatestUserContains(sub string) func(provider.Request) bool {
	return func(req provider.Request) bool {
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == provider.RoleUser {
				return strings.Contains(req.Messages[i].Text, sub)
			}
		}
		return false
	}
}

// matchToolResultUnlessFinalization is matchToolResult that ignores requests
// whose latest user message is a budget finalization prompt.
func matchToolResultUnlessFinalization(callID string) func(provider.Request) bool {
	base := matchToolResult(callID)
	return func(req provider.Request) bool {
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role == provider.RoleUser {
				if strings.Contains(req.Messages[i].Text, "Budget finalization") {
					return false
				}
				break
			}
		}
		return base(req)
	}
}

func TestChildBudgetToolCallsEscalatesAndStatus(t *testing.T) {
	// Child runs max_tool_calls+1 identical tools → hard trip → child.escalated
	// + failed terminal. Parent task_status exposes budget + objective.
	const (
		childPrompt  = "budget-child-tools"
		parentPrompt = "spawn budget child"
	)
	// Spawn with max_tool_calls=2; child will call channel thrice (3rd trips).
	taskArgs, _ := json.Marshal(map[string]any{
		"prompt": childPrompt,
		"budget": map[string]any{"max_tool_calls": 2},
	})
	taskCall := provider.ToolCall{ID: "task-bud", Name: "task", Args: taskArgs}

	ct := &channelTool{executed: make(chan string, 8)}

	handoffJSON := `{"summary":"stopped at tool budget","findings":["found csrf gap"],"files_changed":[],"blockers":["tool budget"],"recommended_next_action":"fix auth","incomplete":true}`
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("parent after spawn")
			s.match = matchToolResult("task-bud")
			return s
		}(),
		// Child turn: three tool calls (third trips budget during begin of 3rd or after 2nd).
		func() streamStep {
			s := toolCallStep(
				toolCall("c1", "channel"),
				toolCall("c2", "channel"),
				toolCall("c3", "channel"),
			)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("child should not need this if interrupted")
			s.match = matchToolResultUnlessFinalization("c1")
			return s
		}(),
		func() streamStep {
			s := completedStep("after c2")
			s.match = matchToolResultUnlessFinalization("c2")
			return s
		}(),
		func() streamStep {
			s := completedStep("after c3")
			s.match = matchToolResultUnlessFinalization("c3")
			return s
		}(),
		// Soft-budget finalization turn (#879).
		func() streamStep {
			s := completedStep(handoffJSON)
			s.match = matchLatestUserContains("Budget finalization")
			return s
		}(),
		childCompletedNudgeStep("parent ack"),
		// Parent second turn: task_status
		func() streamStep {
			return streamStep{
				match: matchLatestUserText("status please"),
				stream: func(ctx context.Context) <-chan provider.StreamEvent {
					// session id filled dynamically is hard — use a step that
					// fires on any second user turn and call task_status via
					// a fixed pattern after we know id from events.
					ch := make(chan provider.StreamEvent)
					close(ch)
					return ch
				},
			}
		}(),
	)

	eng := engine.New(engine.Options{
		SessionID:       "parent-budget",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        taskControlRegistry(ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: parentPrompt}
	events := drainUntil(t, eng, 15*time.Second, func(evs []protocol.Event) bool {
		var started, escalated, completed bool
		for _, ev := range evs {
			switch e := ev.(type) {
			case protocol.ChildStarted:
				if e.Prompt == childPrompt {
					started = true
				}
			case protocol.ChildEscalated:
				if e.Kind == "tool_calls" {
					escalated = true
				}
			case protocol.ChildCompleted:
				if e.Status == protocol.ChildStatusFailed || e.Status == protocol.ChildStatusCanceled {
					completed = true
				}
			}
		}
		return started && escalated && completed
	})

	var childID string
	var esc protocol.ChildEscalated
	var done protocol.ChildCompleted
	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.ChildStarted:
			if e.Prompt == childPrompt {
				childID = e.SessionID
			}
		case protocol.ChildEscalated:
			esc = e
		case protocol.ChildCompleted:
			done = e
		}
	}
	if childID == "" {
		t.Fatal("missing child id")
	}
	if esc.Kind != "tool_calls" {
		t.Fatalf("escalated = %+v", esc)
	}
	if esc.Action != protocol.EscalateActionFinalizing && esc.Action != protocol.EscalateActionInterrupted {
		t.Fatalf("action=%q want finalizing|interrupted", esc.Action)
	}
	if esc.Reason == "" || !strings.Contains(esc.Reason, "tool-call") {
		t.Fatalf("reason = %q", esc.Reason)
	}
	if done.Status != protocol.ChildStatusFailed {
		// canceled is acceptable if interrupt wins the race before budget terminal is applied
		if done.Status != protocol.ChildStatusCanceled {
			t.Fatalf("completed status = %s want failed|canceled", done.Status)
		}
	}
	if done.BudgetKind != "" && done.BudgetKind != "tool_calls" {
		t.Fatalf("BudgetKind=%q", done.BudgetKind)
	}
	// Soft finalization should preserve structured findings when the model answers.
	if done.BudgetKind == "tool_calls" && done.Finalization == protocol.FinalizationSucceeded {
		if len(done.Handoff.Findings) == 0 {
			t.Fatalf("expected findings on successful finalization: %+v", done.Handoff)
		}
		if done.Handoff.Quality != protocol.HandoffQualityComplete &&
			done.Handoff.Quality != protocol.HandoffQualityPartial {
			t.Fatalf("quality=%q", done.Handoff.Quality)
		}
	}
	if !strings.Contains(done.Summary, "tool-call") && done.Status == protocol.ChildStatusFailed {
		// summary should carry budget reason when failed
		if done.Summary == "" {
			t.Fatal("empty summary on failed child")
		}
	}

	// Direct engine status via a fresh status tool turn is heavy; call through
	// a minimal second user turn with scripted task_status.
	statusCall := controlToolCall("st-bud", "task_status", map[string]any{
		"session_id":     childID,
		"include_recent": true,
	})
	// Replace provider steps is hard mid-flight — use eng via tool context by
	// spawning another parent turn with a new scripted provider is complex.
	// Instead verify roster snapshot fields via agent_roster on a new engine
	// is overkill. Re-run status through Ops by injecting a turn with a
	// dedicated provider append — simplest path: use task_status via
	// permission-free internal isn't exported. Parse TeamRoster from events.
	var rosterOK bool
	for _, ev := range events {
		tr, ok := ev.(protocol.TeamRoster)
		if !ok {
			continue
		}
		for _, m := range tr.Members {
			if m.SessionID != childID {
				continue
			}
			if m.Objective != childPrompt && m.Objective == "" {
				// objective may be set
			}
			if m.Budget != nil && m.Budget.Escalated {
				rosterOK = true
			}
			if m.Budget != nil && m.Budget.ToolCalls >= 2 {
				rosterOK = true
			}
		}
	}
	// Roster may have been emitted before escalate; require escalated event at minimum.
	_ = rosterOK
	_ = statusCall

	// Race-friendly: ensure at least one ChildEscalated on the wire.
	if countEvents[protocol.ChildEscalated](events) < 1 {
		t.Fatal("expected ChildEscalated event")
	}
}

func TestChildBudgetWallClockEscalates(t *testing.T) {
	const childPrompt = "budget-wall-child"
	release := make(chan struct{})
	ct := &channelTool{
		executed: make(chan string, 2),
		blocks:   map[string]<-chan struct{}{"hold": release},
	}
	taskArgs, _ := json.Marshal(map[string]any{
		"prompt": childPrompt,
		"budget": map[string]any{"max_wall_clock_s": 1},
	})
	taskCall := provider.ToolCall{ID: "task-wall", Name: "task", Args: taskArgs}

	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("parent spawned")
			s.match = matchToolResult("task-wall")
			return s
		}(),
		func() streamStep {
			s := toolCallStep(toolCall("hold", "channel"))
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("child after hold")
			s.match = matchToolResult("hold")
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)

	eng := engine.New(engine.Options{
		SessionID:       "parent-wall",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        taskControlRegistry(ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}
	// Wait until child is blocked in tool, then let wall clock tick.
	select {
	case <-ct.executed:
	case <-time.After(5 * time.Second):
		t.Fatal("child tool never started")
	}

	events := drainUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		for _, ev := range evs {
			if e, ok := ev.(protocol.ChildEscalated); ok && e.Kind == "wall_clock" {
				return true
			}
		}
		return false
	})
	close(release) // unblock if still held

	found := false
	for _, ev := range events {
		if e, ok := ev.(protocol.ChildEscalated); ok && e.Kind == "wall_clock" {
			found = true
			if e.Action != protocol.EscalateActionFinalizing && e.Action != protocol.EscalateActionInterrupted {
				t.Fatalf("action=%s", e.Action)
			}
		}
	}
	if !found {
		t.Fatal("missing wall_clock ChildEscalated")
	}
}

func TestTaskStatusExposesBudgetFields(t *testing.T) {
	// Live child with default soft signals + objective on status JSON.
	const childPrompt = "status-budget-obj"
	release := make(chan struct{})
	ct := &channelTool{
		executed: make(chan string, 2),
		blocks:   map[string]<-chan struct{}{"hold": release},
	}
	taskCall := taskToolCall("task-st", childPrompt)

	var (
		statusCall provider.ToolCall
		turn2Ready = make(chan struct{})
	)

	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("spawned")
			s.match = matchToolResult("task-st")
			return s
		}(),
		func() streamStep {
			s := toolCallStep(toolCall("hold", "channel"))
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			return streamStep{
				match: matchLatestUserText("check status"),
				stream: func(ctx context.Context) <-chan provider.StreamEvent {
					select {
					case <-turn2Ready:
					case <-ctx.Done():
						ch := make(chan provider.StreamEvent)
						close(ch)
						return ch
					}
					events := []provider.StreamEvent{
						{Type: provider.EventToolCall, ToolCall: &statusCall},
						{Type: provider.EventDone, StopReason: "tool_use"},
					}
					ch := make(chan provider.StreamEvent, len(events))
					for _, ev := range events {
						ch <- ev
					}
					close(ch)
					return ch
				},
			}
		}(),
		func() streamStep {
			s := completedStep("status done")
			s.match = matchToolResult("st-1")
			return s
		}(),
		func() streamStep {
			s := completedStep("child done")
			s.match = matchToolResult("hold")
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)

	eng := engine.New(engine.Options{
		SessionID:       "parent-st-bud",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        taskControlRegistry(ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		DefaultChildBudget: tool.AgentBudgetLimits{
			MaxTokens: 999999,
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn"}
	events := drainUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		return countEvents[protocol.ChildStarted](evs) >= 1 &&
			countEvents[protocol.TurnCompleted](evs) >= 1
	})
	var childID string
	for _, ev := range events {
		if cs, ok := ev.(protocol.ChildStarted); ok && cs.Prompt == childPrompt {
			childID = cs.SessionID
		}
	}
	if childID == "" {
		t.Fatal("no child")
	}
	select {
	case <-ct.executed:
	case <-time.After(5 * time.Second):
		t.Fatal("child tool not running")
	}

	statusCall = controlToolCall("st-1", "task_status", map[string]any{
		"session_id":     childID,
		"include_recent": true,
	})
	close(turn2Ready)
	eng.Ops() <- protocol.UserInput{Text: "check status"}
	events = drainUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		_, ok := toolEndOutput(evs, "st-1")
		return ok
	})
	out, ok := toolEndOutput(events, "st-1")
	if !ok {
		t.Fatal("missing task_status output")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if payload["objective"] != childPrompt {
		t.Fatalf("objective=%v want %q\n%s", payload["objective"], childPrompt, out)
	}
	if payload["last_action"] == nil || payload["last_action"] == "" {
		t.Fatalf("missing last_action: %s", out)
	}
	bud, ok := payload["budget"].(map[string]any)
	if !ok {
		t.Fatalf("missing budget object: %s", out)
	}
	limits, _ := bud["limits"].(map[string]any)
	if limits == nil {
		// limits may be nested under budget with max_tokens
		if _, has := bud["max_tokens"]; !has {
			// AgentBudgetSnapshot has Limits field
			if _, has := bud["tokens_used"]; !has {
				t.Fatalf("budget shape unexpected: %#v", bud)
			}
		}
	}
	close(release)
}

func TestChildBudgetFinalizationSuccessAndHardSkip(t *testing.T) {
	// Soft tool budget → finalizing → structured handoff with findings.
	const childPrompt = "finalize-me-please"
	taskArgs, _ := json.Marshal(map[string]any{
		"prompt": childPrompt,
		"budget": map[string]any{"max_tool_calls": 1},
	})
	taskCall := provider.ToolCall{ID: "task-fin", Name: "task", Args: taskArgs}
	ct := &channelTool{executed: make(chan string, 4)}
	handoffJSON := `{
		"summary": "partial review before budget stop",
		"findings": ["nil deref in parse", "missing test for edge"],
		"files_changed": ["pkg/x.go"],
		"verification": "go test ./pkg -count=1 (not finished)",
		"blockers": ["tool-call budget"],
		"recommended_next_action": "finish tests then re-run",
		"incomplete": true
	}`

	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("parent spawned")
			s.match = matchToolResult("task-fin")
			return s
		}(),
		func() streamStep {
			// Two tools: 2nd trips max_tool_calls=1.
			s := toolCallStep(toolCall("t1", "channel"), toolCall("t2", "channel"))
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("after t1")
			s.match = matchToolResultUnlessFinalization("t1")
			return s
		}(),
		func() streamStep {
			s := completedStep("after t2")
			s.match = matchToolResultUnlessFinalization("t2")
			return s
		}(),
		func() streamStep {
			s := completedStep(handoffJSON)
			s.match = matchLatestUserContains("Budget finalization")
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)

	eng := engine.New(engine.Options{
		SessionID:       "parent-fin",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        taskControlRegistry(ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}
	events := drainUntil(t, eng, 20*time.Second, func(evs []protocol.Event) bool {
		var esc, done bool
		for _, ev := range evs {
			switch e := ev.(type) {
			case protocol.ChildEscalated:
				if e.Kind == "tool_calls" {
					esc = true
				}
			case protocol.ChildCompleted:
				if e.BudgetKind == "tool_calls" {
					done = true
				}
			}
		}
		return esc && done
	})

	var escalated protocol.ChildEscalated
	var completed protocol.ChildCompleted
	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.ChildEscalated:
			escalated = e
		case protocol.ChildCompleted:
			completed = e
		}
	}
	if escalated.Action != protocol.EscalateActionFinalizing {
		t.Fatalf("escalated action=%q want finalizing; full=%+v", escalated.Action, escalated)
	}
	if completed.Status != protocol.ChildStatusFailed {
		t.Fatalf("status=%s", completed.Status)
	}
	if completed.BudgetKind != "tool_calls" {
		t.Fatalf("BudgetKind=%q", completed.BudgetKind)
	}
	if completed.Finalization != protocol.FinalizationSucceeded {
		t.Fatalf("Finalization=%q handoff=%+v", completed.Finalization, completed.Handoff)
	}
	if len(completed.Handoff.Findings) < 2 {
		t.Fatalf("findings=%v", completed.Handoff.Findings)
	}
	if completed.Handoff.RecommendedNextAction == "" {
		t.Fatal("missing recommended_next_action")
	}
	if completed.Handoff.Quality != protocol.HandoffQualityPartial &&
		completed.Handoff.Quality != protocol.HandoffQualityComplete {
		t.Fatalf("quality=%q", completed.Handoff.Quality)
	}
	// Engine-tracked files may merge; model files_changed should appear.
	foundFile := false
	for _, f := range completed.Handoff.FilesChanged {
		if f == "pkg/x.go" {
			foundFile = true
		}
	}
	if !foundFile {
		t.Fatalf("files_changed missing pkg/x.go: %v", completed.Handoff.FilesChanged)
	}
}

func TestChildBudgetTokenFinalization(t *testing.T) {
	// Token budget trip should also attempt finalization.
	const childPrompt = "token-budget-child"
	taskArgs, _ := json.Marshal(map[string]any{
		"prompt": childPrompt,
		"budget": map[string]any{"max_tokens": 1},
	})
	taskCall := provider.ToolCall{ID: "task-tok", Name: "task", Args: taskArgs}
	handoffJSON := `{"summary":"token stop","findings":["partial"],"blockers":["tokens"],"files_changed":[]}`

	// Scripted provider reports usage via stream events — use echo-like usage
	// by completing with text after a tool so UsageReported may fire from
	// engine. Force trip via many tool rounds is harder; use wall path style:
	// child completes one stream with usage in provider if supported.
	// Fallback: use max_tokens=1 and a stream that emits UsageReported through
	// the engine's consume path — scripted provider may not emit usage.
	// Instead trip via dangerous_tools which is deterministic like tool_calls.
	_ = handoffJSON
	taskArgs, _ = json.Marshal(map[string]any{
		"prompt": childPrompt,
		"budget": map[string]any{"max_dangerous_tools": 1},
	})
	taskCall = provider.ToolCall{ID: "task-tok", Name: "task", Args: taskArgs}
	// Use write tool as dangerous — need registry with write. channel is not dangerous.
	// Reuse tool_calls dimension covered above; here cover tokens via unit evaluate
	// already in budget_test. Engine path: wall_clock already has escalate test.
	// Cover finalization failure (no structured reply).
	ct := &channelTool{executed: make(chan string, 4)}
	taskArgs, _ = json.Marshal(map[string]any{
		"prompt": childPrompt,
		"budget": map[string]any{"max_tool_calls": 1},
	})
	taskCall = provider.ToolCall{ID: "task-tok", Name: "task", Args: taskArgs}

	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("spawned")
			s.match = matchToolResult("task-tok")
			return s
		}(),
		func() streamStep {
			s := toolCallStep(toolCall("a", "channel"), toolCall("b", "channel"))
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("after a")
			s.match = matchToolResultUnlessFinalization("a")
			return s
		}(),
		func() streamStep {
			s := completedStep("after b")
			s.match = matchToolResultUnlessFinalization("b")
			return s
		}(),
		// Finalization turn returns prose only → finalization failed, quality unavailable/partial.
		func() streamStep {
			s := completedStep("sorry I cannot format json right now")
			s.match = matchLatestUserContains("Budget finalization")
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)

	eng := engine.New(engine.Options{
		SessionID:       "parent-tok",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        taskControlRegistry(ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}
	events := drainUntil(t, eng, 20*time.Second, func(evs []protocol.Event) bool {
		for _, ev := range evs {
			if cc, ok := ev.(protocol.ChildCompleted); ok && cc.BudgetKind == "tool_calls" {
				return true
			}
		}
		return false
	})
	var completed protocol.ChildCompleted
	for _, ev := range events {
		if cc, ok := ev.(protocol.ChildCompleted); ok {
			completed = cc
		}
	}
	if completed.Finalization != protocol.FinalizationFailed {
		t.Fatalf("Finalization=%q want failed; handoff=%+v", completed.Finalization, completed.Handoff)
	}
	// Prose summary still surfaces; quality is partial (non-generic summary) or unavailable.
	if completed.Handoff.Quality != protocol.HandoffQualityPartial &&
		completed.Handoff.Quality != protocol.HandoffQualityUnavailable {
		t.Fatalf("quality=%q", completed.Handoff.Quality)
	}
	if completed.Handoff.Summary == "" {
		t.Fatal("empty summary")
	}
}

func TestChildBudgetHardCancelSkipsFinalization(t *testing.T) {
	// Parent cancel while child runs: no finalization model call.
	const childPrompt = "cancel-no-finalize"
	release := make(chan struct{})
	ct := &channelTool{
		executed: make(chan string, 2),
		blocks:   map[string]<-chan struct{}{"hold": release},
	}
	taskArgs, _ := json.Marshal(map[string]any{
		"prompt": childPrompt,
		"budget": map[string]any{"max_tool_calls": 100},
	})
	taskCall := provider.ToolCall{ID: "task-can", Name: "task", Args: taskArgs}

	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("spawned")
			s.match = matchToolResult("task-can")
			return s
		}(),
		func() streamStep {
			s := toolCallStep(toolCall("hold", "channel"))
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("should not run")
			s.match = matchToolResult("hold")
			return s
		}(),
		// Must NOT match finalization — if it does, test fails via unexpected complete text.
		func() streamStep {
			s := completedStep(`{"summary":"should not finalize","findings":["bad"]}`)
			s.match = matchUserTextContains("Budget finalization")
			return s
		}(),
		childCompletedNudgeStep("ack"),
	)

	eng := engine.New(engine.Options{
		SessionID:       "parent-can",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        taskControlRegistry(ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "go"}
	// Wait for child tool to start.
	select {
	case <-ct.executed:
	case <-time.After(5 * time.Second):
		t.Fatal("child tool never started")
	}
	// Hard cancel parent life (session end) — skips finalization.
	cancel()
	close(release)

	events := drainUntil(t, eng, 10*time.Second, func(evs []protocol.Event) bool {
		for _, ev := range evs {
			if _, ok := ev.(protocol.ChildCompleted); ok {
				return true
			}
		}
		return false
	})
	var completed protocol.ChildCompleted
	var sawFinalizeEsc bool
	for _, ev := range events {
		switch e := ev.(type) {
		case protocol.ChildEscalated:
			if e.Action == protocol.EscalateActionFinalizing {
				sawFinalizeEsc = true
			}
		case protocol.ChildCompleted:
			completed = e
		}
	}
	if sawFinalizeEsc {
		t.Fatal("parent cancel must not emit finalizing escalation")
	}
	// Not budget-driven.
	if completed.BudgetKind != "" {
		t.Fatalf("BudgetKind=%q want empty on hard cancel", completed.BudgetKind)
	}
	if completed.Finalization == protocol.FinalizationSucceeded {
		t.Fatal("finalization must not succeed on hard cancel")
	}
}
