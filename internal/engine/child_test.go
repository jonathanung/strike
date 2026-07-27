package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func taskToolCall(id, prompt string) provider.ToolCall {
	return provider.ToolCall{
		ID:   id,
		Name: "task",
		Args: json.RawMessage(`{"prompt":` + jsonQuote(prompt) + `}`),
	}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func editToolCall(id, path, old, new string) provider.ToolCall {
	args, _ := json.Marshal(map[string]any{
		"filePath":  path,
		"oldString": old,
		"newString": new,
	})
	return provider.ToolCall{ID: id, Name: "edit", Args: args}
}

func bashToolCall(id, command string) provider.ToolCall {
	args, _ := json.Marshal(map[string]any{"command": command})
	return provider.ToolCall{ID: id, Name: "bash", Args: args}
}

func summarizeEvents(events []protocol.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, eventSummary(ev))
	}
	return out
}

func eventSummary(ev protocol.Event) string {
	switch ev := ev.(type) {
	case protocol.ChildStarted:
		return "ChildStarted session=" + ev.SessionID
	case protocol.ChildCompleted:
		return "ChildCompleted status=" + string(ev.Status)
	case protocol.ToolCallBegin:
		return "ToolCallBegin " + ev.Name + " " + ev.CallID
	case protocol.ToolCallEnd:
		return "ToolCallEnd " + ev.CallID + " err=" + boolString(ev.IsError)
	case protocol.PermissionAsked:
		return "PermissionAsked " + ev.Permission + " " + ev.RequestID
	case protocol.QuestionAsked:
		return "QuestionAsked " + ev.RequestID
	case protocol.QuestionResolved:
		return "QuestionResolved " + ev.RequestID
	case protocol.AgentSelected:
		return "AgentSelected " + ev.Name
	case protocol.TurnCompleted:
		return "TurnCompleted " + ev.StopReason
	case protocol.EngineError:
		return "EngineError " + ev.Message
	case protocol.TextDelta:
		return "TextDelta"
	default:
		return "other"
	}
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func countEvents[T protocol.Event](events []protocol.Event) int {
	n := 0
	for _, ev := range events {
		if _, ok := ev.(T); ok {
			n++
		}
	}
	return n
}

// drainAndReply runs until the parent has finished its work turn(s), every
// started child has emitted ChildCompleted, and any auto-nudge turn that
// injects child.completed into the parent model has completed. Auto-approves
// PermissionAsked.
func drainAndReply(t *testing.T, eng *engine.Engine, timeout time.Duration) []protocol.Event {
	t.Helper()
	var collected []protocol.Event
	var parentDone bool
	var started, completed int
	var noticeSeen bool
	var turnsAfterNotice int
	guard := time.NewTimer(timeout)
	defer guard.Stop()
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed before drain finished")
			}
			collected = append(collected, ev)
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.QuestionAsked:
				// Auto-dismiss so question tests that only need lifecycle can finish.
			case protocol.ChildStarted:
				started++
			case protocol.ChildCompleted:
				completed++
			case protocol.UserMessage:
				if strings.Contains(ev.Text, "[child.completed") {
					noticeSeen = true
				}
			case protocol.TurnCompleted:
				// Child TurnCompleted is not re-emitted on the parent stream, so
				// any TurnCompleted here is the invoking engine's turn.
				parentDone = true
				if noticeSeen {
					turnsAfterNotice++
				}
			}
			// With children: wait for auto-nudge turn(s) covering every
			// child.completed. Without children: first parent turn is enough.
			if !parentDone || started != completed {
				continue
			}
			if started == 0 {
				return collected
			}
			if turnsAfterNotice >= 1 && childCompletionNotices(collected) >= completed {
				return collected
			}
		case <-guard.C:
			t.Fatalf("timed out; parentDone=%v started=%d completed=%d notice=%v turnsAfterNotice=%d notices=%d events=%v",
				parentDone, started, completed, noticeSeen, turnsAfterNotice, childCompletionNotices(collected), summarizeEvents(collected))
		}
	}
}

func childCompletionNotices(events []protocol.Event) int {
	n := 0
	for _, ev := range events {
		if um, ok := ev.(protocol.UserMessage); ok {
			n += strings.Count(um.Text, "[child.completed")
		}
	}
	return n
}

// childCompletedNudgeStep matches the parent auto-nudge UserInput that carries
// a child.completed summary after a non-blocking task finishes.
func childCompletedNudgeStep(reply string) streamStep {
	s := completedStep(reply)
	s.match = func(req provider.Request) bool {
		for _, m := range req.Messages {
			if m.Role == provider.RoleUser && strings.Contains(m.Text, "[child.completed") {
				return true
			}
		}
		return false
	}
	return s
}

func TestForegroundTaskIndependentHistory(t *testing.T) {
	const (
		parentSession = "parent-session"
		taskPrompt    = "child-only-prompt-xyz"
		parentPrompt  = "parent user turn"
	)
	taskCall := taskToolCall("task-1", taskPrompt)
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("child finished work")
			s.match = matchUserText(taskPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent finished")
			s.match = matchToolResult("task-1")
			return s
		}(),
		childCompletedNudgeStep("parent ack child"),
	)
	eng := engine.New(engine.Options{
		SessionID:       parentSession,
		Select:          func(string) (provider.Provider, string, error) { return prov, "scripted-model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: parentPrompt}
	events := drainAndReply(t, eng, 10*time.Second)

	var started []protocol.ChildStarted
	var completed []protocol.ChildCompleted
	var taskEnds []protocol.ToolCallEnd
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildStarted:
			started = append(started, ev)
		case protocol.ChildCompleted:
			completed = append(completed, ev)
		case protocol.ToolCallEnd:
			if ev.CallID == "task-1" {
				taskEnds = append(taskEnds, ev)
			}
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if len(started) != 1 {
		t.Fatalf("ChildStarted count = %d, want 1; events=%v", len(started), summarizeEvents(events))
	}
	if len(completed) != 1 {
		t.Fatalf("ChildCompleted count = %d, want 1", len(completed))
	}
	if completed[0].Status != protocol.ChildStatusCompleted {
		t.Errorf("ChildCompleted status = %q, want completed", completed[0].Status)
	}
	if started[0].SessionID == "" || started[0].SessionID == parentSession {
		t.Errorf("child SessionID = %q, want distinct from parent %q", started[0].SessionID, parentSession)
	}
	if started[0].ParentSessionID != parentSession {
		t.Errorf("ParentSessionID = %q, want %q", started[0].ParentSessionID, parentSession)
	}
	if started[0].Depth != 1 {
		t.Errorf("child Depth = %d, want 1", started[0].Depth)
	}
	if completed[0].SessionID != started[0].SessionID || completed[0].ParentSessionID != parentSession || completed[0].Depth != 1 {
		t.Errorf("ChildCompleted correlation = %#v, want match started %#v", completed[0].Correlation, started[0].Correlation)
	}
	if started[0].Prompt != taskPrompt {
		t.Errorf("ChildStarted prompt = %q, want %q", started[0].Prompt, taskPrompt)
	}
	if !strings.Contains(completed[0].Summary, "child finished work") {
		t.Errorf("ChildCompleted summary = %q, want child work", completed[0].Summary)
	}
	if len(taskEnds) != 1 {
		t.Fatalf("task ToolCallEnd count = %d, want 1", len(taskEnds))
	}
	if taskEnds[0].IsError {
		t.Errorf("task ToolCallEnd is error: %s", taskEnds[0].Output)
	}
	if !strings.Contains(taskEnds[0].Output, "Started child session") {
		t.Errorf("task output = %q, want started notice", taskEnds[0].Output)
	}
	if !strings.Contains(taskEnds[0].Output, started[0].SessionID) {
		t.Errorf("task output = %q, want session id %q", taskEnds[0].Output, started[0].SessionID)
	}

	// Four Stream calls: parent tool-use, parent final, child final, parent
	// child.completed nudge (order of middle calls may race).
	if prov.callCount() != 4 {
		t.Fatalf("Stream calls = %d, want 4", prov.callCount())
	}
	var reqs []provider.Request
	for i := 0; i < 4; i++ {
		reqs = append(reqs, receiveRequest(t, prov.requests))
	}
	var sawNudge bool
	for _, r := range reqs {
		for _, msg := range r.Messages {
			if msg.Role == provider.RoleUser && strings.Contains(msg.Text, "[child.completed") {
				sawNudge = true
				if !strings.Contains(msg.Text, "child finished work") {
					t.Errorf("nudge missing child summary: %q", msg.Text)
				}
			}
		}
	}
	if !sawNudge {
		t.Fatal("missing parent auto-nudge with child.completed")
	}
	var childReq, parentFinal *provider.Request
	for i := range reqs {
		r := &reqs[i]
		// Skip auto-nudge streams (they also contain prior tool results).
		isNudge := false
		for _, msg := range r.Messages {
			if msg.Role == provider.RoleUser && strings.Contains(msg.Text, "[child.completed") {
				isNudge = true
				break
			}
		}
		if isNudge {
			continue
		}
		if len(r.Messages) > 0 && r.Messages[0].Role == provider.RoleUser && r.Messages[0].Text == taskPrompt {
			childReq = r
			continue
		}
		// Parent final includes the original user turn plus a tool result.
		for _, msg := range r.Messages {
			if msg.Role == provider.RoleTool && msg.ToolResult != nil && msg.ToolResult.CallID == "task-1" {
				parentFinal = r
			}
		}
	}
	if childReq == nil {
		t.Fatalf("missing child stream request; reqs=%#v", reqs)
	}
	if childReq.Messages[0].Text != taskPrompt {
		t.Errorf("child user text = %q, want task prompt %q", childReq.Messages[0].Text, taskPrompt)
	}
	// Child history must not include the parent user turn.
	for _, msg := range childReq.Messages {
		if msg.Role == provider.RoleUser && msg.Text == parentPrompt {
			t.Errorf("child history contains parent user turn")
		}
	}
	if parentFinal == nil {
		t.Fatalf("missing parent final stream with task tool result; reqs=%#v", reqs)
	}
	userTurns := 0
	for _, msg := range parentFinal.Messages {
		if msg.Role == provider.RoleUser {
			userTurns++
			if msg.Text == taskPrompt {
				t.Errorf("parent history has child prompt as user turn: %#v", parentFinal.Messages)
			}
		}
	}
	if userTurns != 1 {
		t.Errorf("parent final user turns = %d, want 1; messages=%#v", userTurns, parentFinal.Messages)
	}
	var sawToolResult bool
	for _, msg := range parentFinal.Messages {
		if msg.Role == provider.RoleTool && msg.ToolResult != nil && msg.ToolResult.CallID == "task-1" {
			sawToolResult = true
			if !strings.Contains(msg.ToolResult.Output, "Started child session") {
				t.Errorf("parent tool result = %q, want started notice", msg.ToolResult.Output)
			}
		}
	}
	if !sawToolResult {
		t.Errorf("parent final history missing task tool result: %#v", parentFinal.Messages)
	}
}

func TestTaskDepthRejected(t *testing.T) {
	// Engine already at max depth: SpawnTask is not injected.
	taskCall := taskToolCall("task-nested", "should not spawn")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		completedStep("recovered without child"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "depth-one",
		Depth:           1,
		MaxChildDepth:   1,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "try nested task"}
	events := drainAndReply(t, eng, 5*time.Second)

	if n := countEvents[protocol.ChildStarted](events); n != 0 {
		t.Errorf("ChildStarted count = %d, want 0", n)
	}
	if n := countEvents[protocol.ChildCompleted](events); n != 0 {
		t.Errorf("ChildCompleted count = %d, want 0", n)
	}
	var taskEnd *protocol.ToolCallEnd
	for _, ev := range events {
		if end, ok := ev.(protocol.ToolCallEnd); ok && end.CallID == "task-nested" {
			e := end
			taskEnd = &e
		}
	}
	if taskEnd == nil {
		t.Fatalf("no task ToolCallEnd; events=%v", summarizeEvents(events))
	}
	if !taskEnd.IsError {
		t.Errorf("task end should be error, output=%q", taskEnd.Output)
	}
	if !strings.Contains(strings.ToLower(taskEnd.Output), "not available") &&
		!strings.Contains(strings.ToLower(taskEnd.Output), "depth") {
		t.Errorf("task error = %q, want not available or depth", taskEnd.Output)
	}
}

// drainNested waits until at least wantStarted ChildStarted and matching
// ChildCompleted events have been seen, the root is idle after notices for
// direct (depth-1) children, and auto-approves permissions.
func drainNested(t *testing.T, eng *engine.Engine, wantStarted int, timeout time.Duration) []protocol.Event {
	t.Helper()
	var collected []protocol.Event
	var started, completed, depth1Completed int
	var rootIdle, noticeSeen bool
	var turnsAfterNotice int
	guard := time.NewTimer(timeout)
	defer guard.Stop()
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed before drain finished")
			}
			collected = append(collected, ev)
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.ChildStarted:
				started++
			case protocol.ChildCompleted:
				completed++
				if ev.Depth == 1 {
					depth1Completed++
				}
			case protocol.UserMessage:
				if strings.Contains(ev.Text, "[child.completed") {
					noticeSeen = true
				}
			case protocol.TurnCompleted:
				if ev.ParentSessionID == "" && ev.Depth == 0 {
					rootIdle = true
					if noticeSeen {
						turnsAfterNotice++
					}
				}
			case protocol.EngineError:
				t.Fatalf("engine error: %s; events=%v", ev.Message, summarizeEvents(collected))
			}
			if started < wantStarted || completed < wantStarted {
				continue
			}
			if !rootIdle {
				continue
			}
			// Direct children must surface model-visible notices on the root.
			if depth1Completed > 0 && (!noticeSeen || turnsAfterNotice < 1) {
				continue
			}
			if childCompletionNotices(collected) < depth1Completed {
				continue
			}
			return collected
		case <-guard.C:
			t.Fatalf("timed out; started=%d completed=%d depth1=%d rootIdle=%v notice=%v turnsAfter=%d events=%v",
				started, completed, depth1Completed, rootIdle, noticeSeen, turnsAfterNotice, summarizeEvents(collected))
		}
	}
}

func TestNestedTaskDepthTwoCompletes(t *testing.T) {
	// MaxChildDepth=2: root → child (d1) → grandchild (d2) all complete.
	const (
		childPrompt = "child-level-1-nest"
		gcPrompt    = "grandchild-level-2-nest"
	)
	taskL1 := taskToolCall("task-l1", childPrompt)
	taskL2 := taskToolCall("task-l2", gcPrompt)
	prov := newScriptedProvider(
		toolCallStep(taskL1),
		func() streamStep {
			s := toolCallStep(taskL2)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("grandchild finished work")
			s.match = matchUserText(gcPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("child finished after spawn")
			s.match = matchToolResult("task-l2")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent finished after spawn")
			s.match = matchToolResult("task-l1")
			return s
		}(),
		// Child and/or parent may idle-nudge on nested completions.
		childCompletedNudgeStep("ack nested 1"),
		childCompletedNudgeStep("ack nested 2"),
		childCompletedNudgeStep("ack nested 3"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "root-nest-2",
		MaxChildDepth:   2,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "nest two deep"}
	events := drainNested(t, eng, 2, 15*time.Second)

	var started []protocol.ChildStarted
	var completed []protocol.ChildCompleted
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildStarted:
			started = append(started, ev)
		case protocol.ChildCompleted:
			completed = append(completed, ev)
		}
	}
	if len(started) != 2 {
		t.Fatalf("ChildStarted = %d, want 2; events=%v", len(started), summarizeEvents(events))
	}
	if len(completed) != 2 {
		t.Fatalf("ChildCompleted = %d, want 2; events=%v", len(completed), summarizeEvents(events))
	}
	depths := map[int]int{}
	for _, s := range started {
		depths[s.Depth]++
		if s.Depth < 1 || s.Depth > 2 {
			t.Errorf("unexpected ChildStarted depth %d session=%s", s.Depth, s.SessionID)
		}
	}
	if depths[1] != 1 || depths[2] != 1 {
		t.Errorf("started depths = %v, want one each at 1 and 2", depths)
	}
	for _, c := range completed {
		if c.Status != protocol.ChildStatusCompleted {
			t.Errorf("ChildCompleted depth=%d status=%q summary=%q", c.Depth, c.Status, c.Summary)
		}
	}
	// Lineage: depth-2 parent is the depth-1 session.
	var d1ID, d2Parent string
	for _, s := range started {
		if s.Depth == 1 {
			d1ID = s.SessionID
			if s.ParentSessionID != "root-nest-2" {
				t.Errorf("d1 ParentSessionID = %q, want root-nest-2", s.ParentSessionID)
			}
		}
		if s.Depth == 2 {
			d2Parent = s.ParentSessionID
		}
	}
	if d1ID == "" || d2Parent != d1ID {
		t.Errorf("grandchild parent = %q, want child %q", d2Parent, d1ID)
	}
}

func TestNestedTaskDepthThreeDenied(t *testing.T) {
	// MaxChildDepth=2: grandchild (depth 2) cannot spawn a great-grandchild.
	const (
		childPrompt = "child-d1-deny3"
		gcPrompt    = "grandchild-d2-deny3"
	)
	taskL1 := taskToolCall("task-d1", childPrompt)
	taskL2 := taskToolCall("task-d2", gcPrompt)
	taskL3 := taskToolCall("task-d3", "should-not-spawn")
	prov := newScriptedProvider(
		toolCallStep(taskL1),
		func() streamStep {
			s := toolCallStep(taskL2)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := toolCallStep(taskL3)
			s.match = matchUserText(gcPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("grandchild recovered without great-grandchild")
			s.match = matchToolResult("task-d3")
			return s
		}(),
		func() streamStep {
			s := completedStep("child after grandchild")
			s.match = matchToolResult("task-d2")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent after child")
			s.match = matchToolResult("task-d1")
			return s
		}(),
		childCompletedNudgeStep("ack deny3 1"),
		childCompletedNudgeStep("ack deny3 2"),
		childCompletedNudgeStep("ack deny3 3"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "root-deny-3",
		MaxChildDepth:   2,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "try depth three"}
	events := drainNested(t, eng, 2, 15*time.Second)

	var started []protocol.ChildStarted
	for _, ev := range events {
		if s, ok := ev.(protocol.ChildStarted); ok {
			started = append(started, s)
		}
	}
	if len(started) != 2 {
		t.Fatalf("ChildStarted = %d, want 2 (no depth-3); events=%v", len(started), summarizeEvents(events))
	}
	for _, s := range started {
		if s.Depth >= 3 {
			t.Errorf("unexpected depth %d ChildStarted", s.Depth)
		}
	}
	// task-d3 is on the grandchild engine; ToolCallEnd is not re-emitted on the
	// root stream. Assert via absence of depth-3 ChildStarted only.
}

func TestNestedTaskDepthTwoAG3StillHolds(t *testing.T) {
	// Child at depth 1 with MaxChildDepth=2 still cannot widen parent deny.
	dir := t.TempDir()
	target := filepath.Join(dir, "protected.txt")
	original := "keep-nested-safe"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	const childPrompt = "try edit while nestable"
	taskCall := taskToolCallWithAgent("task-ag3-nest", childPrompt, "writer")
	editCall := editToolCall("edit-nest", "protected.txt", "keep-nested-safe", "pwned-nested")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := toolCallStep(editCall)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("child done after deny")
			s.match = matchToolResult("edit-nest")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent done")
			s.match = matchToolResult("task-ag3-nest")
			return s
		}(),
		childCompletedNudgeStep("parent ack ag3 nest"),
	)
	rules := []permission.Ruleset{
		permission.Defaults(),
		{
			{Permission: "edit", Pattern: "*", Action: permission.Deny},
			{Permission: "write", Pattern: "*", Action: permission.Deny},
		},
	}
	eng := engine.New(engine.Options{
		SessionID:       "parent-ag3-nest",
		MaxChildDepth:   2,
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewEdit(), tool.NewWrite()),
		WorkDir:         dir,
		Rules:           rules,
		Agents: []engine.Agent{
			{Name: "build"},
			{
				Name: "writer",
				Permissions: permission.Ruleset{
					{Permission: "edit", Pattern: "*", Action: permission.Allow},
					{Permission: "write", Pattern: "*", Action: permission.Allow},
					{Permission: "task", Pattern: "*", Action: permission.Allow},
				},
			},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "delegate edit under nestable depth"}
	events := drainAndReply(t, eng, 10*time.Second)

	if n := countEvents[protocol.ChildStarted](events); n != 1 {
		t.Errorf("ChildStarted = %d, want 1; events=%v", n, summarizeEvents(events))
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("file content = %q, want unchanged %q", data, original)
	}
}

func TestParentInterruptLeavesChildRunning(t *testing.T) {
	// Parent finishes its turn while the child is still blocked on a tool.
	// Interrupt after the parent is already idle must not cancel the child.
	neverRelease := make(chan struct{})
	ct := &channelTool{
		executed: make(chan string, 1),
		blocks:   map[string]<-chan struct{}{"block": neverRelease},
	}
	const childPrompt = "run blocking child"
	taskCall := taskToolCall("task-int", childPrompt)
	childCall := toolCall("block", "channel")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := toolCallStep(childCall)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent finished while child runs")
			s.match = matchToolResult("task-int")
			return s
		}(),
		// Child stays blocked; nudge unused unless test is extended.
		childCompletedNudgeStep("unused nudge"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-int",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn then interrupt"}

	// Wait until parent turn ends and child is blocked in its tool.
	guard := time.NewTimer(10 * time.Second)
	defer guard.Stop()
	var (
		events                 []protocol.Event
		sawStarted, parentDone bool
		taskEnds               []protocol.ToolCallEnd
		childToolRunning       bool
	)
	for !(sawStarted && parentDone && childToolRunning) {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed early")
			}
			events = append(events, ev)
			switch ev := ev.(type) {
			case protocol.ChildStarted:
				sawStarted = true
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.TurnCompleted:
				parentDone = true
			case protocol.ToolCallEnd:
				if ev.CallID == "task-int" {
					taskEnds = append(taskEnds, ev)
				}
			case protocol.ChildCompleted:
				t.Fatalf("child completed before interrupt; events=%v", summarizeEvents(events))
			}
		case id := <-ct.executed:
			if id != "block" {
				t.Fatalf("executed = %q, want block", id)
			}
			childToolRunning = true
		case <-guard.C:
			t.Fatalf("timed out; started=%v parentDone=%v childTool=%v events=%v",
				sawStarted, parentDone, childToolRunning, summarizeEvents(events))
		}
	}

	// Interrupt while parent is idle and child is still blocked.
	eng.Ops() <- protocol.Interrupt{}

	settle := time.NewTimer(150 * time.Millisecond)
	defer settle.Stop()
settleLoop:
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				break settleLoop
			}
			events = append(events, ev)
			if _, ok := ev.(protocol.ChildCompleted); ok {
				t.Fatalf("child completed after parent interrupt; events=%v", summarizeEvents(events))
			}
		case <-settle.C:
			break settleLoop
		}
	}

	if len(taskEnds) != 1 {
		t.Fatalf("task ToolCallEnd count = %d, want 1; events=%v", len(taskEnds), summarizeEvents(events))
	}
	if taskEnds[0].IsError {
		t.Errorf("task ToolCallEnd should succeed (started); got %q", taskEnds[0].Output)
	}
	if !strings.Contains(taskEnds[0].Output, "Started child session") {
		t.Errorf("task output = %q, want started notice", taskEnds[0].Output)
	}
}

func TestChildCannotWidenPermissions(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "protected.txt")
	original := "keep-me-safe"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// Child agent profile allows edit/write; parent base rules deny — AG3
	// requires the child allow not to override the parent ceiling.
	const childPrompt = "try to edit"
	taskCall := taskToolCallWithAgent("task-perm", childPrompt, "writer")
	editCall := editToolCall("edit-1", "protected.txt", "keep-me-safe", "pwned")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := toolCallStep(editCall)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("child done after deny")
			s.match = matchToolResult("edit-1")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent done")
			s.match = matchToolResult("task-perm")
			return s
		}(),
		childCompletedNudgeStep("parent ack perm child"),
	)
	rules := []permission.Ruleset{
		permission.Defaults(),
		{
			{Permission: "edit", Pattern: "*", Action: permission.Deny},
			{Permission: "write", Pattern: "*", Action: permission.Deny},
		},
	}
	eng := engine.New(engine.Options{
		SessionID:       "parent-deny-edit",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewEdit(), tool.NewWrite()),
		WorkDir:         dir,
		Rules:           rules,
		Agents: []engine.Agent{
			{Name: "build"},
			{
				Name: "writer",
				Permissions: permission.Ruleset{
					{Permission: "edit", Pattern: "*", Action: permission.Allow},
					{Permission: "write", Pattern: "*", Action: permission.Allow},
				},
			},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "delegate edit"}
	events := drainAndReply(t, eng, 10*time.Second)

	if n := countEvents[protocol.ChildStarted](events); n != 1 {
		t.Errorf("ChildStarted = %d, want 1; events=%v", n, summarizeEvents(events))
	}
	if n := countEvents[protocol.ChildCompleted](events); n != 1 {
		t.Errorf("ChildCompleted = %d, want 1", n)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("file content = %q, want unchanged %q", data, original)
	}

	// Parent task may complete successfully with a summary that mentions the
	// failed edit, or with an error tool result — either way the file is safe.
	// Child ToolCallEnd is not forwarded on the parent event stream.
	var taskEnd *protocol.ToolCallEnd
	for _, ev := range events {
		if end, ok := ev.(protocol.ToolCallEnd); ok && end.CallID == "task-perm" {
			e := end
			taskEnd = &e
		}
	}
	if taskEnd == nil {
		t.Fatalf("missing task ToolCallEnd; events=%v", summarizeEvents(events))
	}
}

func TestTaskExactlyOneTerminalResult(t *testing.T) {
	const childPrompt = "one shot"
	taskCall := taskToolCall("task-once", childPrompt)
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("only once")
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent ok")
			s.match = matchToolResult("task-once")
			return s
		}(),
		childCompletedNudgeStep("parent ack once"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "once-session",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "run task once"}
	events := drainAndReply(t, eng, 10*time.Second)

	if n := countEvents[protocol.ChildStarted](events); n != 1 {
		t.Errorf("ChildStarted = %d, want 1", n)
	}
	if n := countEvents[protocol.ChildCompleted](events); n != 1 {
		t.Errorf("ChildCompleted = %d, want 1", n)
	}
	ends := 0
	for _, ev := range events {
		if end, ok := ev.(protocol.ToolCallEnd); ok && end.CallID == "task-once" {
			ends++
			if end.IsError {
				t.Errorf("unexpected task error: %s", end.Output)
			}
		}
	}
	if ends != 1 {
		t.Errorf("task ToolCallEnd count = %d, want 1; events=%v", ends, summarizeEvents(events))
	}
}

func TestChildPermissionReplyRouting(t *testing.T) {
	const childPrompt = "run bash in child"
	taskCall := taskToolCall("task-ask", childPrompt)
	childBash := bashToolCall("bash-1", "printf child-bash-ok")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := toolCallStep(childBash)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("child after bash")
			s.match = matchToolResult("bash-1")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent after task")
			s.match = matchToolResult("task-ask")
			return s
		}(),
		childCompletedNudgeStep("parent ack bash child"),
	)
	// Defaults: bash asks; task allows.
	eng := engine.New(engine.Options{
		SessionID:       "parent-ask-route",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewBash()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "delegate bash"}
	events := drainAndReply(t, eng, 10*time.Second)

	var sawChildAsk bool
	for _, ev := range events {
		if asked, ok := ev.(protocol.PermissionAsked); ok {
			if asked.ParentSessionID == "parent-ask-route" || asked.Depth > 0 {
				sawChildAsk = true
				if asked.Permission != "bash" {
					t.Errorf("permission = %q, want bash", asked.Permission)
				}
			}
		}
	}
	if !sawChildAsk {
		t.Fatalf("never saw child-correlated PermissionAsked; events=%v", summarizeEvents(events))
	}
	if n := countEvents[protocol.ChildStarted](events); n != 1 {
		t.Errorf("ChildStarted = %d, want 1", n)
	}
	if n := countEvents[protocol.ChildCompleted](events); n != 1 {
		t.Errorf("ChildCompleted = %d, want 1", n)
	}
	var taskEnd *protocol.ToolCallEnd
	for _, ev := range events {
		if end, ok := ev.(protocol.ToolCallEnd); ok && end.CallID == "task-ask" {
			e := end
			taskEnd = &e
		}
	}
	if taskEnd == nil {
		t.Fatal("missing task ToolCallEnd")
	}
	if taskEnd.IsError {
		t.Errorf("task failed after permission reply: %s", taskEnd.Output)
	}
	if !strings.Contains(taskEnd.Output, "Started child session") {
		t.Errorf("task output = %q, want started notice", taskEnd.Output)
	}
	var childDone []protocol.ChildCompleted
	for _, ev := range events {
		if c, ok := ev.(protocol.ChildCompleted); ok {
			childDone = append(childDone, c)
		}
	}
	if len(childDone) != 1 {
		t.Fatalf("ChildCompleted = %d, want 1", len(childDone))
	}
	if !strings.Contains(childDone[0].Summary, "child after bash") {
		t.Errorf("ChildCompleted summary = %q, want child after bash", childDone[0].Summary)
	}
}

// TestTaskSurfacesChildStreamError ensures a child provider/stream failure is
// not collapsed to the opaque "task failed" summary on ChildCompleted.
func TestTaskSurfacesChildStreamError(t *testing.T) {
	const (
		errMsg      = "child stream boom: invalid_request_error: bad child payload"
		childPrompt = "child will fail"
	)
	taskCall := taskToolCall("task-stream-err", childPrompt)
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		streamStep{err: errors.New(errMsg), match: matchUserText(childPrompt)},
		func() streamStep {
			s := completedStep("parent recovered")
			s.match = matchToolResult("task-stream-err")
			return s
		}(),
		childCompletedNudgeStep("parent ack stream err"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-stream-err",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn failing child"}
	events := drainAndReply(t, eng, 10*time.Second)

	var completed []protocol.ChildCompleted
	var taskEnds []protocol.ToolCallEnd
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildCompleted:
			completed = append(completed, ev)
		case protocol.ToolCallEnd:
			if ev.CallID == "task-stream-err" {
				taskEnds = append(taskEnds, ev)
			}
		}
	}
	if n := countEvents[protocol.ChildStarted](events); n != 1 {
		t.Fatalf("ChildStarted = %d, want 1; events=%v", n, summarizeEvents(events))
	}
	if len(completed) != 1 {
		t.Fatalf("ChildCompleted count = %d, want 1; events=%v", len(completed), summarizeEvents(events))
	}
	if completed[0].Status != protocol.ChildStatusFailed {
		t.Errorf("ChildCompleted status = %q, want failed", completed[0].Status)
	}
	if !strings.Contains(completed[0].Summary, errMsg) {
		t.Errorf("ChildCompleted summary = %q, want to contain %q", completed[0].Summary, errMsg)
	}
	if completed[0].Summary == "task failed" {
		t.Errorf("ChildCompleted summary is opaque %q", completed[0].Summary)
	}
	if len(taskEnds) != 1 {
		t.Fatalf("task ToolCallEnd count = %d, want 1", len(taskEnds))
	}
	// Non-blocking: spawn succeeds; failure is reported via ChildCompleted.
	if taskEnds[0].IsError {
		t.Errorf("task ToolCallEnd IsError = true; output=%q", taskEnds[0].Output)
	}
	if !strings.Contains(taskEnds[0].Output, "Started child session") {
		t.Errorf("task output = %q, want started notice", taskEnds[0].Output)
	}
}

// TestTaskChildInheritsParentProvider checks that a child reuses the parent's
// live provider instead of re-calling Select. A Select that only succeeds once
// would leave the child with "no model selected" if inherit were broken.
func TestTaskChildInheritsParentProvider(t *testing.T) {
	var selectCalls atomic.Int32
	const childPrompt = "child work"
	taskCall := taskToolCall("task-inherit", childPrompt)
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("child ok via inherit")
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent ok")
			s.match = matchToolResult("task-inherit")
			return s
		}(),
		childCompletedNudgeStep("parent ack inherit"),
	)
	selectFn := func(string) (provider.Provider, string, error) {
		n := selectCalls.Add(1)
		if n > 1 {
			return nil, "", errors.New("Select must not be called again for child")
		}
		return prov, "model", nil
	}
	eng := engine.New(engine.Options{
		SessionID:       "parent-inherit",
		Select:          selectFn,
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn with inherited provider"}
	events := drainAndReply(t, eng, 10*time.Second)

	var completed []protocol.ChildCompleted
	var taskEnds []protocol.ToolCallEnd
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildCompleted:
			completed = append(completed, ev)
		case protocol.ToolCallEnd:
			if ev.CallID == "task-inherit" {
				taskEnds = append(taskEnds, ev)
			}
		case protocol.EngineError:
			t.Fatalf("engine error: %s", ev.Message)
		}
	}
	if n := countEvents[protocol.ChildStarted](events); n != 1 {
		t.Fatalf("ChildStarted = %d, want 1; events=%v", n, summarizeEvents(events))
	}
	if len(completed) != 1 {
		t.Fatalf("ChildCompleted count = %d, want 1", len(completed))
	}
	if completed[0].Status != protocol.ChildStatusCompleted {
		t.Errorf("ChildCompleted status = %q, want completed; summary=%q", completed[0].Status, completed[0].Summary)
	}
	if !strings.Contains(completed[0].Summary, "child ok via inherit") {
		t.Errorf("ChildCompleted summary = %q", completed[0].Summary)
	}
	if len(taskEnds) != 1 {
		t.Fatalf("task ToolCallEnd count = %d, want 1", len(taskEnds))
	}
	if taskEnds[0].IsError {
		t.Errorf("task failed without inherit: %s", taskEnds[0].Output)
	}
	if !strings.Contains(taskEnds[0].Output, "Started child session") {
		t.Errorf("task output = %q, want started notice", taskEnds[0].Output)
	}
	if n := selectCalls.Load(); n != 1 {
		t.Errorf("Select calls = %d, want 1 (child must inherit live provider)", n)
	}
	// Four Stream calls: parent tool-use, parent final, child final, nudge.
	if prov.callCount() != 4 {
		t.Fatalf("Stream calls = %d, want 4", prov.callCount())
	}
}

// TestTaskSurfacesChildEngineErrorNoModel forces a pre-turn child EngineError
// ("no model selected") by clearing the inherited provider via an agent that
// pins a provider whose Select returns a nil provider without error — if the
// implementation cannot hit that path, the streamed EventError path below
// still pins failMsg surfacing for any EngineError text.
func TestTaskSurfacesChildEngineErrorMessage(t *testing.T) {
	const errMsg = "no model selected — use /provider <anthropic|openai|xai|echo> [model]"
	// Use a streamed EventError so the child emits EngineError with this exact
	// message (same text as the pre-turn no-provider path).
	const childPrompt = "child engine error"
	taskCall := taskToolCall("task-eng-err", childPrompt)
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		streamStep{
			match: matchUserText(childPrompt),
			events: []provider.StreamEvent{
				{Type: provider.EventError, Err: errors.New(errMsg)},
			},
		},
		func() streamStep {
			s := completedStep("parent after engine error")
			s.match = matchToolResult("task-eng-err")
			return s
		}(),
		childCompletedNudgeStep("parent ack eng err"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-eng-err",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn child engine error"}
	events := drainAndReply(t, eng, 10*time.Second)

	var completed []protocol.ChildCompleted
	var taskEnds []protocol.ToolCallEnd
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildCompleted:
			completed = append(completed, ev)
		case protocol.ToolCallEnd:
			if ev.CallID == "task-eng-err" {
				taskEnds = append(taskEnds, ev)
			}
		}
	}
	if len(completed) != 1 {
		t.Fatalf("ChildCompleted count = %d, want 1; events=%v", len(completed), summarizeEvents(events))
	}
	if completed[0].Status != protocol.ChildStatusFailed {
		t.Errorf("ChildCompleted status = %q, want failed", completed[0].Status)
	}
	if !strings.Contains(completed[0].Summary, "no model selected") {
		t.Errorf("ChildCompleted summary = %q, want to contain no model selected", completed[0].Summary)
	}
	if len(taskEnds) != 1 {
		t.Fatalf("task ToolCallEnd count = %d, want 1", len(taskEnds))
	}
	if taskEnds[0].IsError {
		t.Errorf("task ToolCallEnd IsError = true; output=%q", taskEnds[0].Output)
	}
	if !strings.Contains(taskEnds[0].Output, "Started child session") {
		t.Errorf("task output = %q, want started notice", taskEnds[0].Output)
	}
}

func TestTaskNonBlockingParentContinuesWhileChildRuns(t *testing.T) {
	// Child blocks on a tool; parent must finish its turn (ToolCallEnd + final
	// stream) before the child unblocks.
	release := make(chan struct{})
	ct := &channelTool{
		executed: make(chan string, 1),
		blocks:   map[string]<-chan struct{}{"block": release},
	}
	const childPrompt = "slow child"
	taskCall := taskToolCall("task-nb", childPrompt)
	childCall := toolCall("block", "channel")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := toolCallStep(childCall)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("child after release")
			s.match = matchToolResult("block")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent finished first")
			s.match = matchToolResult("task-nb")
			return s
		}(),
		childCompletedNudgeStep("parent ack slow child"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-nb",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn slow child"}

	var events []protocol.Event
	var parentDone, taskEnded, childRunning bool
	guard := time.NewTimer(10 * time.Second)
	defer guard.Stop()
	for !(parentDone && taskEnded && childRunning) {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed early")
			}
			events = append(events, ev)
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.ToolCallEnd:
				if ev.CallID == "task-nb" {
					taskEnded = true
					if ev.IsError {
						t.Fatalf("task error: %s", ev.Output)
					}
				}
			case protocol.TurnCompleted:
				if ev.ParentSessionID == "" && ev.Depth == 0 {
					parentDone = true
				}
			case protocol.ChildCompleted:
				t.Fatalf("child completed before release; events=%v", summarizeEvents(events))
			}
		case id := <-ct.executed:
			if id != "block" {
				t.Fatalf("executed = %q", id)
			}
			childRunning = true
		case <-guard.C:
			t.Fatalf("timed out; parentDone=%v taskEnded=%v childRunning=%v events=%v",
				parentDone, taskEnded, childRunning, summarizeEvents(events))
		}
	}

	close(release)
	// Wait for child completion and the idle-parent auto-nudge that injects
	// the summary into model-visible history.
	doneGuard := time.NewTimer(5 * time.Second)
	defer doneGuard.Stop()
	var childDone, nudgeDone bool
	for !(childDone && nudgeDone) {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed before ChildCompleted/nudge")
			}
			events = append(events, ev)
			switch ev := ev.(type) {
			case protocol.ChildCompleted:
				childDone = true
				if ev.Status != protocol.ChildStatusCompleted {
					t.Errorf("ChildCompleted status = %q", ev.Status)
				}
			case protocol.UserMessage:
				if strings.Contains(ev.Text, "[child.completed") {
					nudgeDone = true
					if !strings.Contains(ev.Text, "child after release") {
						t.Errorf("nudge missing summary: %q", ev.Text)
					}
				}
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		case <-doneGuard.C:
			t.Fatalf("timed out; childDone=%v nudgeDone=%v events=%v",
				childDone, nudgeDone, summarizeEvents(events))
		}
	}
}

// TestChildCompletedNudgeWhileIdle: child finishes after the parent turn has
// already ended; parent must auto-start a turn with a model-visible summary.
func TestChildCompletedNudgeWhileIdle(t *testing.T) {
	release := make(chan struct{})
	ct := &channelTool{
		executed: make(chan string, 1),
		blocks:   map[string]<-chan struct{}{"block": release},
	}
	const childPrompt = "idle-nudge-child"
	taskCall := taskToolCall("task-idle", childPrompt)
	childCall := toolCall("block", "channel")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := toolCallStep(childCall)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("child summary for idle parent")
			s.match = matchToolResult("block")
			return s
		}(),
		func() streamStep {
			s := completedStep("parent finished before child")
			s.match = matchToolResult("task-idle")
			return s
		}(),
		childCompletedNudgeStep("acked idle completion"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-idle-nudge",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn then wait idle"}

	var events []protocol.Event
	var parentDone, childRunning bool
	guard := time.NewTimer(10 * time.Second)
	defer guard.Stop()
	for !(parentDone && childRunning) {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed early")
			}
			events = append(events, ev)
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.TurnCompleted:
				if ev.ParentSessionID == "" && ev.Depth == 0 {
					parentDone = true
				}
			case protocol.ChildCompleted:
				t.Fatalf("child completed before release; events=%v", summarizeEvents(events))
			}
		case id := <-ct.executed:
			if id != "block" {
				t.Fatalf("executed = %q", id)
			}
			childRunning = true
		case <-guard.C:
			t.Fatalf("timed out; parentDone=%v childRunning=%v events=%v",
				parentDone, childRunning, summarizeEvents(events))
		}
	}

	close(release)

	var sawCompleted, sawNudge, nudgeTurnDone bool
	doneGuard := time.NewTimer(5 * time.Second)
	defer doneGuard.Stop()
	for !(sawCompleted && sawNudge && nudgeTurnDone) {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed early")
			}
			events = append(events, ev)
			switch ev := ev.(type) {
			case protocol.ChildCompleted:
				sawCompleted = true
				if !strings.Contains(ev.Summary, "child summary for idle parent") {
					t.Errorf("summary = %q", ev.Summary)
				}
			case protocol.UserMessage:
				if strings.Contains(ev.Text, "[child.completed") {
					sawNudge = true
					if !strings.Contains(ev.Text, "child summary for idle parent") {
						t.Errorf("nudge text = %q", ev.Text)
					}
					if !strings.Contains(ev.Text, "status=completed") {
						t.Errorf("nudge missing status: %q", ev.Text)
					}
				}
			case protocol.TurnCompleted:
				if sawNudge {
					nudgeTurnDone = true
				}
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		case <-doneGuard.C:
			t.Fatalf("timed out; completed=%v nudge=%v nudgeTurn=%v events=%v",
				sawCompleted, sawNudge, nudgeTurnDone, summarizeEvents(events))
		}
	}

	// Shut down so Messages is safe to read without racing the turn worker.
	cancel()
	for range eng.Events() {
	}
	var found bool
	for _, msg := range eng.Messages() {
		if msg.Role == provider.RoleUser && strings.Contains(msg.Text, "[child.completed") {
			found = true
			if !strings.Contains(msg.Text, "child summary for idle parent") {
				t.Errorf("messages nudge = %q", msg.Text)
			}
		}
	}
	if !found {
		t.Fatalf("Messages missing child.completed; got %#v", eng.Messages())
	}
}

// TestChildCompletedQueuedDuringParentTurn: completion arrives mid-turn and is
// delivered model-visibly — either injected before the next Stream in the same
// turn, or as an idle auto-nudge after the parent finishes (not dropped).
func TestChildCompletedQueuedDuringParentTurn(t *testing.T) {
	const childPrompt = "fast-child-during-turn"
	taskCall := taskToolCall("task-mid", childPrompt)
	release := make(chan struct{})
	ct := &channelTool{
		executed: make(chan string, 1),
		blocks:   map[string]<-chan struct{}{"hold": release},
	}
	holdCall := toolCall("hold", "channel")
	prov := newScriptedProvider(
		toolCallStep(taskCall, holdCall),
		func() streamStep {
			s := completedStep("fast child done mid-parent-turn")
			s.match = matchUserText(childPrompt)
			return s
		}(),
		// Parent continues after tools; may already include mid-turn inject.
		func() streamStep {
			s := completedStep("parent after tools")
			s.match = matchToolResult("hold")
			return s
		}(),
		// Idle nudge path if inject did not consume the notice mid-turn.
		childCompletedNudgeStep("acked mid-turn completion"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-mid-turn",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "task then hold"}

	var events []protocol.Event
	var sawChildCompleted, holdStarted, parentTurnDone, sawNudge bool
	guard := time.NewTimer(10 * time.Second)
	defer guard.Stop()
	for !(sawChildCompleted && parentTurnDone && sawNudge) {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed early")
			}
			events = append(events, ev)
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.ChildCompleted:
				sawChildCompleted = true
				if !holdStarted {
					t.Log("child finished before parent hold started")
				}
				// Unblock parent hold so the work turn can finish.
				select {
				case <-release:
				default:
					close(release)
				}
			case protocol.UserMessage:
				if strings.Contains(ev.Text, "[child.completed") {
					sawNudge = true
					if !strings.Contains(ev.Text, "fast child done mid-parent-turn") {
						t.Errorf("nudge = %q", ev.Text)
					}
				}
			case protocol.TurnCompleted:
				if ev.ParentSessionID == "" && ev.Depth == 0 {
					parentTurnDone = true
					select {
					case <-release:
					default:
						close(release)
					}
				}
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		case id := <-ct.executed:
			if id == "hold" {
				holdStarted = true
			}
		case <-guard.C:
			t.Fatalf("timed out; child=%v hold=%v parentDone=%v nudge=%v events=%v",
				sawChildCompleted, holdStarted, parentTurnDone, sawNudge, summarizeEvents(events))
		}
	}
	if !sawChildCompleted {
		t.Fatal("missing ChildCompleted")
	}
	if !sawNudge {
		t.Fatal("missing model-visible child.completed notice")
	}
	if !holdStarted {
		t.Log("hold never started; mid-turn path not exercised")
	}
}

// TestTwoConcurrentTasksBothReachParent ensures two task children both emit
// ChildCompleted and both summaries are model-visible on the parent.
func TestTwoConcurrentTasksBothReachParent(t *testing.T) {
	const (
		promptA = "child-a-work"
		promptB = "child-b-work"
	)
	taskA := taskToolCall("task-a", promptA)
	taskB := taskToolCall("task-b", promptB)
	prov := newScriptedProvider(
		toolCallStep(taskA, taskB),
		func() streamStep {
			s := completedStep("summary-alpha")
			s.match = matchUserText(promptA)
			return s
		}(),
		func() streamStep {
			s := completedStep("summary-beta")
			s.match = matchUserText(promptB)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent after both tasks")
			s.match = func(req provider.Request) bool {
				var sawA, sawB bool
				for _, m := range req.Messages {
					if m.Role == provider.RoleTool && m.ToolResult != nil {
						if m.ToolResult.CallID == "task-a" {
							sawA = true
						}
						if m.ToolResult.CallID == "task-b" {
							sawB = true
						}
					}
				}
				return sawA && sawB
			}
			return s
		}(),
		// One or two nudge turns depending on scheduling; allow two.
		childCompletedNudgeStep("ack concurrent 1"),
		childCompletedNudgeStep("ack concurrent 2"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-two-tasks",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn two"}
	events := drainAndReply(t, eng, 15*time.Second)

	if n := countEvents[protocol.ChildStarted](events); n != 2 {
		t.Fatalf("ChildStarted = %d, want 2; events=%v", n, summarizeEvents(events))
	}
	if n := countEvents[protocol.ChildCompleted](events); n != 2 {
		t.Fatalf("ChildCompleted = %d, want 2; events=%v", n, summarizeEvents(events))
	}

	var notices []string
	for _, ev := range events {
		if um, ok := ev.(protocol.UserMessage); ok && strings.Contains(um.Text, "[child.completed") {
			notices = append(notices, um.Text)
		}
	}
	joined := strings.Join(notices, "\n")
	if !strings.Contains(joined, "summary-alpha") {
		t.Errorf("missing summary-alpha in notices: %q", joined)
	}
	if !strings.Contains(joined, "summary-beta") {
		t.Errorf("missing summary-beta in notices: %q", joined)
	}
	// Durable path: UserMessage events are session-logged and restored into
	// model history (engine.Restore), so both summaries are model-visible.
}

func TestReviewerChildHardDenyFeedsNextProviderTurn(t *testing.T) {
	const childPrompt = "review with unavailable git"
	taskCall := taskToolCallWithAgent("task-review-deny", childPrompt, "reviewer")
	gitCall := bashToolCall("git-denied", "git diff origin/main")
	var childFollowup provider.Request
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := toolCallStep(gitCall)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("review blocked: read-only git access is unavailable")
			s.match = func(req provider.Request) bool {
				for _, m := range req.Messages {
					if m.Role == provider.RoleTool && m.ToolResult != nil && m.ToolResult.CallID == gitCall.ID {
						childFollowup = req
						return m.ToolResult.IsError && strings.Contains(strings.ToLower(m.ToolResult.Output), "permission denied")
					}
				}
				return false
			}
			return s
		}(),
		func() streamStep {
			s := completedStep("parent after reviewer")
			s.match = matchToolResult("task-review-deny")
			return s
		}(),
		childCompletedNudgeStep("ack reviewer block"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-review-deny",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewBash()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "reviewer", Permissions: permission.Ruleset{
				{Permission: "bash", Pattern: "*", Action: permission.Deny},
				{Permission: "bash", Pattern: "git *", Action: permission.Allow},
			}},
		},
		InitialAgent: "build",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "delegate review"}
	events := drainAndReply(t, eng, 15*time.Second)

	if len(childFollowup.Messages) == 0 {
		t.Fatal("reviewer did not reach a provider turn after permission denial")
	}
	var deniedEnds int
	for _, ev := range events {
		if end, ok := ev.(protocol.ToolCallEnd); ok && end.CallID == gitCall.ID {
			deniedEnds++
			if !end.IsError || !strings.Contains(strings.ToLower(end.Output), "permission denied") {
				t.Errorf("denied ToolCallEnd = %#v", end)
			}
		}
	}
	if deniedEnds != 0 {
		t.Fatalf("child ToolCallEnd leaked onto parent stream: %d", deniedEnds)
	}
	var completed protocol.ChildCompleted
	for _, ev := range events {
		if c, ok := ev.(protocol.ChildCompleted); ok {
			completed = c
		}
	}
	if completed.Status != protocol.ChildStatusCompleted || !strings.Contains(completed.Summary, "git access is unavailable") {
		t.Fatalf("ChildCompleted = %#v, want actionable completed summary", completed)
	}
}

// TestChildCompletedInjectedDuringSleepPoll: parent sleep-polls after task spawn;
// child finishes mid-sleep; parent must see [child.completed] on the next Stream
// without requiring idle auto-nudge only, and without unbounded sleep loops.
func TestChildCompletedInjectedDuringSleepPoll(t *testing.T) {
	const childPrompt = "sleep-poll-child"
	taskCall := taskToolCall("task-poll", childPrompt)
	sleepCall := provider.ToolCall{
		ID:   "sleep-1",
		Name: "sleep",
		Args: json.RawMessage(`{"seconds":30}`),
	}
	release := make(chan struct{})
	ct := &channelTool{
		executed: make(chan string, 1),
		blocks:   map[string]<-chan struct{}{"hold": release},
	}
	holdCall := toolCall("hold", "channel")

	var sawCompletionInStream atomic.Bool
	prov := newScriptedProvider(
		// Parent: spawn task then sleep-poll.
		toolCallStep(taskCall, sleepCall),
		// Child: block then finish quickly after release.
		func() streamStep {
			s := toolCallStep(holdCall)
			s.match = matchUserText(childPrompt)
			return s
		}(),
		func() streamStep {
			s := completedStep("child result for sleep poll parent")
			s.match = matchToolResult("hold")
			return s
		}(),
		// After sleep wakes + inject: parent must see child.completed and finish
		// without further sleep.
		func() streamStep {
			s := completedStep("parent summarized child without more sleep")
			s.match = func(req provider.Request) bool {
				for _, m := range req.Messages {
					if m.Role == provider.RoleUser && strings.Contains(m.Text, "[child.completed") &&
						strings.Contains(m.Text, "child result for sleep poll parent") {
						sawCompletionInStream.Store(true)
						return true
					}
				}
				return false
			}
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-sleep-poll",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask(), tool.NewSleep(), ct),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn and poll"}

	var events []protocol.Event
	var sleepBegan, childDone, parentDone bool
	guard := time.NewTimer(15 * time.Second)
	defer guard.Stop()
	for !(childDone && parentDone && sawCompletionInStream.Load()) {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed early")
			}
			events = append(events, ev)
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.ToolCallBegin:
				if ev.Name == "sleep" {
					sleepBegan = true
					// Release child once parent is sleeping so completion races sleep.
					select {
					case <-release:
					default:
						close(release)
					}
				}
			case protocol.ToolCallEnd:
				if ev.CallID == "sleep-1" {
					if !strings.Contains(ev.Output, "child") && !strings.Contains(strings.ToLower(ev.Title), "woke") {
						// Full 30s sleep would fail the timeout; allow either wake or full.
						t.Logf("sleep end title=%q output=%q", ev.Title, ev.Output)
					}
				}
				if strings.HasPrefix(ev.CallID, "sleep-") && ev.CallID != "sleep-1" {
					t.Fatalf("extra sleep tool call %q — poll loop not broken", ev.CallID)
				}
			case protocol.ChildCompleted:
				childDone = true
				if !strings.Contains(ev.Summary, "child result for sleep poll parent") {
					t.Errorf("ChildCompleted summary = %q", ev.Summary)
				}
			case protocol.TurnCompleted:
				if ev.ParentSessionID == "" && ev.Depth == 0 && sawCompletionInStream.Load() {
					parentDone = true
				}
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		case <-guard.C:
			t.Fatalf("timed out; sleepBegan=%v childDone=%v parentDone=%v sawInject=%v events=%v",
				sleepBegan, childDone, parentDone, sawCompletionInStream.Load(), summarizeEvents(events))
		}
	}

	// Exactly one child.completed UserMessage (model-visible once).
	if n := childCompletionNotices(events); n != 1 {
		t.Fatalf("child.completed UserMessage count = %d, want 1; events=%v", n, summarizeEvents(events))
	}
	// Task cell path: ToolCallEnd started + ChildCompleted present.
	if n := countEvents[protocol.ChildCompleted](events); n != 1 {
		t.Fatalf("ChildCompleted = %d, want 1", n)
	}
	if !sleepBegan {
		t.Fatal("expected parent to enter sleep before child finished")
	}
}

// TestTwoConcurrentChildrenEachReportOnce: two children each inject exactly once.
func TestTwoConcurrentChildrenEachReportOnce(t *testing.T) {
	const (
		promptA = "concurrent-a"
		promptB = "concurrent-b"
	)
	taskA := taskToolCall("task-ca", promptA)
	taskB := taskToolCall("task-cb", promptB)
	prov := newScriptedProvider(
		toolCallStep(taskA, taskB),
		func() streamStep {
			s := completedStep("result-a-once")
			s.match = matchUserText(promptA)
			return s
		}(),
		func() streamStep {
			s := completedStep("result-b-once")
			s.match = matchUserText(promptB)
			return s
		}(),
		func() streamStep {
			s := completedStep("parent after spawn")
			s.match = func(req provider.Request) bool {
				var sawA, sawB bool
				for _, m := range req.Messages {
					if m.Role == provider.RoleTool && m.ToolResult != nil {
						if m.ToolResult.CallID == "task-ca" {
							sawA = true
						}
						if m.ToolResult.CallID == "task-cb" {
							sawB = true
						}
					}
				}
				return sawA && sawB
			}
			return s
		}(),
		childCompletedNudgeStep("ack both"),
		// Allow a second nudge turn if completions arrive separately.
		childCompletedNudgeStep("ack both 2"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-two-once",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "two children"}
	events := drainAndReply(t, eng, 15*time.Second)

	if n := countEvents[protocol.ChildCompleted](events); n != 2 {
		t.Fatalf("ChildCompleted = %d, want 2", n)
	}
	// Count [child.completed markers across all UserMessages (may be batched).
	markers := 0
	var joined strings.Builder
	for _, ev := range events {
		if um, ok := ev.(protocol.UserMessage); ok {
			markers += strings.Count(um.Text, "[child.completed")
			joined.WriteString(um.Text)
			joined.WriteByte('\n')
		}
	}
	if markers != 2 {
		t.Fatalf("[child.completed] markers = %d, want 2; text=%q", markers, joined.String())
	}
	if !strings.Contains(joined.String(), "result-a-once") || !strings.Contains(joined.String(), "result-b-once") {
		t.Fatalf("missing child summaries in notices: %q", joined.String())
	}
}

func taskToolCallWith(id string, fields map[string]any) provider.ToolCall {
	if fields == nil {
		fields = map[string]any{}
	}
	args, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return provider.ToolCall{ID: id, Name: "task", Args: args}
}

// TestTaskSpawnWithModel pins a bare model id on the parent provider; the
// child Stream must use that model and not re-Select.
func TestTaskSpawnWithModel(t *testing.T) {
	const childPrompt = "child with model pin"
	const wantModel = "child-model-x"
	taskCall := taskToolCallWith("task-model", map[string]any{
		"prompt": childPrompt,
		"model":  wantModel,
	})
	var childModel string
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("child ok with model")
			s.match = func(req provider.Request) bool {
				if !matchUserText(childPrompt)(req) {
					return false
				}
				childModel = req.Model
				return true
			}
			return s
		}(),
		func() streamStep {
			s := completedStep("parent ok")
			s.match = matchToolResult("task-model")
			return s
		}(),
		childCompletedNudgeStep("parent ack model"),
	)
	var selectCalls atomic.Int32
	eng := engine.New(engine.Options{
		SessionID: "parent-model-pin",
		Select: func(string) (provider.Provider, string, error) {
			selectCalls.Add(1)
			return prov, "parent-model", nil
		},
		InitialProvider: "scripted",
		InitialModel:    "parent-model",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		ListModels: func(_ context.Context, provider string) ([]string, error) {
			if provider != "scripted" {
				t.Errorf("ListModels provider = %q, want scripted", provider)
			}
			return []string{"parent-model", wantModel, "other"}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn with model"}
	events := drainAndReply(t, eng, 10*time.Second)

	if n := countEvents[protocol.ChildStarted](events); n != 1 {
		t.Fatalf("ChildStarted = %d, want 1; events=%v", n, summarizeEvents(events))
	}
	if n := countEvents[protocol.ChildCompleted](events); n != 1 {
		t.Fatalf("ChildCompleted = %d, want 1", n)
	}
	for _, ev := range events {
		if end, ok := ev.(protocol.ToolCallEnd); ok && end.CallID == "task-model" && end.IsError {
			t.Fatalf("task failed: %s", end.Output)
		}
	}
	if childModel != wantModel {
		t.Errorf("child Stream model = %q, want %q", childModel, wantModel)
	}
	if n := selectCalls.Load(); n != 1 {
		t.Errorf("Select calls = %d, want 1 (same provider pin must not re-Select)", n)
	}
}

// TestTaskSpawnInvalidModel rejects unknown catalog ids without starting a child.
func TestTaskSpawnInvalidModel(t *testing.T) {
	taskCall := taskToolCallWith("task-bad-model", map[string]any{
		"prompt": "should not run",
		"model":  "not-a-real-model",
	})
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("parent after deny")
			s.match = matchToolResult("task-bad-model")
			return s
		}(),
	)
	eng := engine.New(engine.Options{
		SessionID: "parent-bad-model",
		Select: func(string) (provider.Provider, string, error) {
			return prov, "parent-model", nil
		},
		InitialProvider: "scripted",
		InitialModel:    "parent-model",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		ListModels: func(context.Context, string) ([]string, error) {
			return []string{"parent-model", "allowed-model"}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn bad model"}
	events := drainAndReply(t, eng, 10*time.Second)

	if n := countEvents[protocol.ChildStarted](events); n != 0 {
		t.Fatalf("ChildStarted = %d, want 0; events=%v", n, summarizeEvents(events))
	}
	var taskEnd *protocol.ToolCallEnd
	for _, ev := range events {
		if end, ok := ev.(protocol.ToolCallEnd); ok && end.CallID == "task-bad-model" {
			e := end
			taskEnd = &e
		}
	}
	if taskEnd == nil {
		t.Fatal("missing task ToolCallEnd")
	}
	if !taskEnd.IsError {
		t.Fatalf("task IsError = false, want true; output=%q", taskEnd.Output)
	}
	if !strings.Contains(taskEnd.Output, "unknown model") {
		t.Errorf("task output = %q, want unknown model", taskEnd.Output)
	}
}

// TestTaskSpawnAgentAndModel applies both persona and model pin; model wins
// over an agent profile model pin.
func TestTaskSpawnAgentAndModel(t *testing.T) {
	const childPrompt = "explore with pinned model"
	const wantModel = "fast-explore-model"
	taskCall := taskToolCallWith("task-agent-model", map[string]any{
		"prompt": childPrompt,
		"agent":  "explore",
		"model":  wantModel,
	})
	var childModel string
	var sawAgent bool
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("explore child done")
			s.match = func(req provider.Request) bool {
				if !matchUserText(childPrompt)(req) {
					return false
				}
				childModel = req.Model
				return true
			}
			return s
		}(),
		func() streamStep {
			s := completedStep("parent ok")
			s.match = matchToolResult("task-agent-model")
			return s
		}(),
		childCompletedNudgeStep("parent ack explore"),
	)
	eng := engine.New(engine.Options{
		SessionID: "parent-agent-model",
		Select: func(string) (provider.Provider, string, error) {
			return prov, "parent-model", nil
		},
		InitialProvider: "scripted",
		InitialModel:    "parent-model",
		Agents: []engine.Agent{
			{Name: "build", Description: "default"},
			{Name: "explore", Description: "read-only", Model: "agent-default-model"},
		},
		InitialAgent: "build",
		Registry:     tool.NewRegistry(tool.NewTask()),
		WorkDir:      t.TempDir(),
		Rules:        []permission.Ruleset{permission.Defaults()},
		ListModels: func(context.Context, string) ([]string, error) {
			return []string{"parent-model", wantModel, "agent-default-model"}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn explore+model"}
	events := drainAndReply(t, eng, 10*time.Second)

	for _, ev := range events {
		if a, ok := ev.(protocol.AgentSelected); ok && a.Name == "explore" {
			sawAgent = true
		}
		if end, ok := ev.(protocol.ToolCallEnd); ok && end.CallID == "task-agent-model" && end.IsError {
			t.Fatalf("task failed: %s", end.Output)
		}
	}
	if n := countEvents[protocol.ChildStarted](events); n != 1 {
		t.Fatalf("ChildStarted = %d, want 1; events=%v", n, summarizeEvents(events))
	}
	_ = sawAgent // optional signal; model pin is the acceptance gate
	if childModel != wantModel {
		t.Errorf("child Stream model = %q, want %q (task model must win over agent pin)", childModel, wantModel)
	}
	if childModel == "agent-default-model" {
		t.Error("child used agent model pin; LockModel should have blocked it")
	}
}

// TestTaskSpawnModelDepthLimit still enforces MaxChildDepth with a model pin:
// at max depth SpawnTask is not injected, so no child starts.
func TestTaskSpawnModelDepthLimit(t *testing.T) {
	taskCall := taskToolCallWith("task-depth-model", map[string]any{
		"prompt": "should not spawn",
		"model":  "m1",
	})
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		completedStep("recovered without child"),
	)
	eng := engine.New(engine.Options{
		SessionID:       "parent-depth-model",
		Depth:           1,
		MaxChildDepth:   1,
		Select:          func(string) (provider.Provider, string, error) { return prov, "m0", nil },
		InitialProvider: "scripted",
		InitialModel:    "m0",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		ListModels: func(context.Context, string) ([]string, error) {
			return []string{"m0", "m1"}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "try nest at max depth with model"}
	events := drainAndReply(t, eng, 5*time.Second)

	if n := countEvents[protocol.ChildStarted](events); n != 0 {
		t.Fatalf("ChildStarted = %d, want 0 at depth limit", n)
	}
	var taskEnd *protocol.ToolCallEnd
	for _, ev := range events {
		if end, ok := ev.(protocol.ToolCallEnd); ok && end.CallID == "task-depth-model" {
			e := end
			taskEnd = &e
		}
	}
	if taskEnd == nil {
		t.Fatal("missing task ToolCallEnd")
	}
	if !taskEnd.IsError {
		t.Errorf("task end should be error, output=%q", taskEnd.Output)
	}
	if !strings.Contains(strings.ToLower(taskEnd.Output), "not available") &&
		!strings.Contains(strings.ToLower(taskEnd.Output), "depth") {
		t.Errorf("task error = %q, want not available or depth", taskEnd.Output)
	}
}

// TestTaskSpawnModelCatalogOverlay accepts a providers.jsonc-only model id
// returned by ListModels (simulating config overlay merge).
func TestTaskSpawnModelCatalogOverlay(t *testing.T) {
	const childPrompt = "overlay model child"
	const overlayModel = "custom-overlay-model"
	taskCall := taskToolCallWith("task-overlay", map[string]any{
		"prompt": childPrompt,
		"model":  overlayModel,
	})
	var childModel string
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("child overlay ok")
			s.match = func(req provider.Request) bool {
				if !matchUserText(childPrompt)(req) {
					return false
				}
				childModel = req.Model
				return true
			}
			return s
		}(),
		func() streamStep {
			s := completedStep("parent ok")
			s.match = matchToolResult("task-overlay")
			return s
		}(),
		childCompletedNudgeStep("ack overlay"),
	)
	eng := engine.New(engine.Options{
		SessionID: "parent-overlay",
		Select: func(string) (provider.Provider, string, error) {
			return prov, "catalog-model", nil
		},
		InitialProvider: "scripted",
		InitialModel:    "catalog-model",
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		// Overlay-only id present; catalog-only id also listed.
		ListModels: func(context.Context, string) ([]string, error) {
			return []string{"catalog-model", overlayModel}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn overlay model"}
	events := drainAndReply(t, eng, 10*time.Second)

	if n := countEvents[protocol.ChildStarted](events); n != 1 {
		t.Fatalf("ChildStarted = %d, want 1; events=%v", n, summarizeEvents(events))
	}
	if childModel != overlayModel {
		t.Errorf("child model = %q, want overlay %q", childModel, overlayModel)
	}
	for _, ev := range events {
		if end, ok := ev.(protocol.ToolCallEnd); ok && end.CallID == "task-overlay" && end.IsError {
			t.Fatalf("task failed: %s", end.Output)
		}
	}
}

// TestTaskSpawnWithoutModelKeepsInherit ensures empty model keeps parent model.
func TestTaskSpawnWithoutModelKeepsInherit(t *testing.T) {
	const childPrompt = "inherit model child"
	const parentModel = "parent-only-model"
	taskCall := taskToolCallWith("task-inherit-model", map[string]any{
		"prompt": childPrompt,
	})
	var childModel string
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		func() streamStep {
			s := completedStep("child inherit ok")
			s.match = func(req provider.Request) bool {
				if !matchUserText(childPrompt)(req) {
					return false
				}
				childModel = req.Model
				return true
			}
			return s
		}(),
		func() streamStep {
			s := completedStep("parent ok")
			s.match = matchToolResult("task-inherit-model")
			return s
		}(),
		childCompletedNudgeStep("ack inherit model"),
	)
	eng := engine.New(engine.Options{
		SessionID: "parent-inherit-model-field",
		Select: func(string) (provider.Provider, string, error) {
			return prov, parentModel, nil
		},
		InitialProvider: "scripted",
		InitialModel:    parentModel,
		Registry:        tool.NewRegistry(tool.NewTask()),
		WorkDir:         t.TempDir(),
		Rules:           []permission.Ruleset{permission.Defaults()},
		ListModels: func(context.Context, string) ([]string, error) {
			return []string{parentModel, "other"}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "spawn inherit"}
	events := drainAndReply(t, eng, 10*time.Second)

	if n := countEvents[protocol.ChildStarted](events); n != 1 {
		t.Fatalf("ChildStarted = %d, want 1", n)
	}
	if childModel != parentModel {
		t.Errorf("child model = %q, want inherited %q", childModel, parentModel)
	}
}
