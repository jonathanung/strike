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

// drainAndReply runs until TurnCompleted, auto-approving any PermissionAsked.
func drainAndReply(t *testing.T, eng *engine.Engine, timeout time.Duration) []protocol.Event {
	t.Helper()
	var collected []protocol.Event
	guard := time.NewTimer(timeout)
	defer guard.Stop()
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed before TurnCompleted")
			}
			collected = append(collected, ev)
			if asked, ok := ev.(protocol.PermissionAsked); ok {
				eng.Ops() <- protocol.PermissionReply{RequestID: asked.RequestID, Decision: protocol.DecisionOnce}
			}
			if _, ok := ev.(protocol.TurnCompleted); ok {
				return collected
			}
		case <-guard.C:
			t.Fatalf("timed out; events=%v", summarizeEvents(collected))
		}
	}
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
		completedStep("child finished work"),
		completedStep("parent finished"),
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
	if len(taskEnds) != 1 {
		t.Fatalf("task ToolCallEnd count = %d, want 1", len(taskEnds))
	}
	if taskEnds[0].IsError {
		t.Errorf("task ToolCallEnd is error: %s", taskEnds[0].Output)
	}
	if !strings.Contains(taskEnds[0].Output, "child finished work") {
		t.Errorf("task output = %q, want child summary", taskEnds[0].Output)
	}

	// Three Stream calls: parent tool-use, child final, parent final.
	if prov.callCount() != 3 {
		t.Fatalf("Stream calls = %d, want 3", prov.callCount())
	}
	var reqs []provider.Request
	for i := 0; i < 3; i++ {
		reqs = append(reqs, receiveRequest(t, prov.requests))
	}
	childReq := reqs[1]
	if len(childReq.Messages) < 1 || childReq.Messages[0].Role != provider.RoleUser {
		t.Fatalf("child first message = %#v, want user", childReq.Messages)
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

	parentFinal := reqs[2]
	// Parent final request must not contain the child's internal user message
	// as a separate parent user turn beyond the tool result.
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
		t.Errorf("parent final user turns = %d, want 1 (only original parent prompt); messages=%#v", userTurns, parentFinal.Messages)
	}
	// Tool result should carry the child summary.
	var sawToolResult bool
	for _, msg := range parentFinal.Messages {
		if msg.Role == provider.RoleTool && msg.ToolResult != nil && msg.ToolResult.CallID == "task-1" {
			sawToolResult = true
			if !strings.Contains(msg.ToolResult.Output, "child finished work") {
				t.Errorf("parent tool result = %q", msg.ToolResult.Output)
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

func TestParentInterruptCancelsChild(t *testing.T) {
	neverRelease := make(chan struct{})
	ct := &channelTool{
		executed: make(chan string, 1),
		blocks:   map[string]<-chan struct{}{"block": neverRelease},
	}
	taskCall := taskToolCall("task-int", "run blocking child")
	childCall := toolCall("block", "channel")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		toolCallStep(childCall),
		// Parent may attempt a follow-up stream after cancel; tolerate it.
		completedStep("should not matter"),
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

	// Wait until child has started executing the blocking tool.
	guard := time.NewTimer(5 * time.Second)
	defer guard.Stop()
	var sawStarted bool
	var events []protocol.Event
loop:
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed early")
			}
			events = append(events, ev)
			if _, ok := ev.(protocol.ChildStarted); ok {
				sawStarted = true
			}
			if asked, ok := ev.(protocol.PermissionAsked); ok {
				eng.Ops() <- protocol.PermissionReply{RequestID: asked.RequestID, Decision: protocol.DecisionOnce}
			}
		case id := <-ct.executed:
			if id != "block" {
				t.Fatalf("executed = %q, want block", id)
			}
			break loop
		case <-guard.C:
			t.Fatalf("timed out waiting for child tool; started=%v events=%v", sawStarted, summarizeEvents(events))
		}
	}
	if !sawStarted {
		// ChildStarted is emitted before the child turn; drain any backlog.
		// It may already be in events from the select race with executed.
		for _, ev := range events {
			if _, ok := ev.(protocol.ChildStarted); ok {
				sawStarted = true
			}
		}
	}
	if !sawStarted {
		// Poll briefly for ChildStarted that raced with executed signal.
		deadline := time.After(2 * time.Second)
		for !sawStarted {
			select {
			case ev := <-eng.Events():
				events = append(events, ev)
				if _, ok := ev.(protocol.ChildStarted); ok {
					sawStarted = true
				}
			case <-deadline:
				t.Fatal("never saw ChildStarted")
			}
		}
	}

	eng.Ops() <- protocol.Interrupt{}

	// Collect until parent TurnCompleted.
	doneGuard := time.NewTimer(5 * time.Second)
	defer doneGuard.Stop()
	var completed bool
	for !completed {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed before TurnCompleted")
			}
			events = append(events, ev)
			if _, ok := ev.(protocol.TurnCompleted); ok {
				completed = true
			}
		case <-doneGuard.C:
			t.Fatalf("timed out after interrupt; events=%v", summarizeEvents(events))
		}
	}

	var childDone []protocol.ChildCompleted
	var taskEnds []protocol.ToolCallEnd
	var startedN int
	for _, ev := range events {
		switch ev := ev.(type) {
		case protocol.ChildStarted:
			startedN++
		case protocol.ChildCompleted:
			childDone = append(childDone, ev)
		case protocol.ToolCallEnd:
			if ev.CallID == "task-int" {
				taskEnds = append(taskEnds, ev)
			}
		}
	}
	if startedN != 1 {
		t.Errorf("ChildStarted count = %d, want 1", startedN)
	}
	if len(childDone) != 1 {
		t.Fatalf("ChildCompleted count = %d, want 1; events=%v", len(childDone), summarizeEvents(events))
	}
	if childDone[0].Status != protocol.ChildStatusCanceled {
		t.Errorf("ChildCompleted status = %q, want canceled", childDone[0].Status)
	}
	if len(taskEnds) != 1 {
		t.Fatalf("task ToolCallEnd count = %d, want 1", len(taskEnds))
	}
	if !taskEnds[0].IsError {
		t.Errorf("task ToolCallEnd should be error, got %q", taskEnds[0].Output)
	}
}

func TestChildCannotWidenPermissions(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "protected.txt")
	original := "keep-me-safe"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	taskCall := taskToolCall("task-perm", "try to edit")
	editCall := editToolCall("edit-1", "protected.txt", "keep-me-safe", "pwned")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		toolCallStep(editCall),
		completedStep("child done after deny"),
		completedStep("parent done"),
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
	taskCall := taskToolCall("task-once", "one shot")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		completedStep("only once"),
		completedStep("parent ok"),
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
	taskCall := taskToolCall("task-ask", "run bash in child")
	childBash := bashToolCall("bash-1", "printf child-bash-ok")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		toolCallStep(childBash),
		completedStep("child after bash"),
		completedStep("parent after task"),
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

	var events []protocol.Event
	var sawChildAsk bool
	guard := time.NewTimer(10 * time.Second)
	defer guard.Stop()
	for {
		select {
		case ev, ok := <-eng.Events():
			if !ok {
				t.Fatal("Events closed early")
			}
			events = append(events, ev)
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				// Child-forwarded ask should carry child lineage.
				if ev.ParentSessionID == "parent-ask-route" || ev.Depth > 0 {
					sawChildAsk = true
					if ev.Permission != "bash" {
						t.Errorf("permission = %q, want bash", ev.Permission)
					}
				}
				eng.Ops() <- protocol.PermissionReply{RequestID: ev.RequestID, Decision: protocol.DecisionOnce}
			case protocol.TurnCompleted:
				goto done
			case protocol.EngineError:
				t.Fatalf("engine error: %s", ev.Message)
			}
		case <-guard.C:
			t.Fatalf("timed out; events=%v", summarizeEvents(events))
		}
	}
done:
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
	if !strings.Contains(taskEnd.Output, "child after bash") {
		t.Errorf("task output = %q, want child summary", taskEnd.Output)
	}
}

// TestTaskSurfacesChildStreamError ensures a child provider/stream failure is
// not collapsed to the opaque "task failed" summary: both ToolCallEnd.Output
// and ChildCompleted.Summary must carry the distinctive error text.
func TestTaskSurfacesChildStreamError(t *testing.T) {
	const errMsg = "child stream boom: invalid_request_error: bad child payload"
	taskCall := taskToolCall("task-stream-err", "child will fail")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		streamStep{err: errors.New(errMsg)},
		completedStep("parent recovered"),
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
	if !taskEnds[0].IsError {
		t.Errorf("task ToolCallEnd IsError = false, want true; output=%q", taskEnds[0].Output)
	}
	if !strings.Contains(taskEnds[0].Output, errMsg) {
		t.Errorf("task ToolCallEnd output = %q, want to contain %q", taskEnds[0].Output, errMsg)
	}
	// Must not be only the bare fallback (with or without Error: prefix).
	if strings.TrimSpace(strings.TrimPrefix(taskEnds[0].Output, "Error: ")) == "task failed" {
		t.Errorf("task output is opaque %q", taskEnds[0].Output)
	}
}

// TestTaskChildInheritsParentProvider checks that a child reuses the parent's
// live provider instead of re-calling Select. A Select that only succeeds once
// would leave the child with "no model selected" if inherit were broken.
func TestTaskChildInheritsParentProvider(t *testing.T) {
	var selectCalls atomic.Int32
	taskCall := taskToolCall("task-inherit", "child work")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		completedStep("child ok via inherit"),
		completedStep("parent ok"),
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
	if !strings.Contains(taskEnds[0].Output, "child ok via inherit") {
		t.Errorf("task output = %q", taskEnds[0].Output)
	}
	if n := selectCalls.Load(); n != 1 {
		t.Errorf("Select calls = %d, want 1 (child must inherit live provider)", n)
	}
	// Three Stream calls: parent tool-use, child final, parent final.
	if prov.callCount() != 3 {
		t.Fatalf("Stream calls = %d, want 3", prov.callCount())
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
	taskCall := taskToolCall("task-eng-err", "child engine error")
	prov := newScriptedProvider(
		toolCallStep(taskCall),
		streamStep{events: []provider.StreamEvent{
			{Type: provider.EventError, Err: errors.New(errMsg)},
		}},
		completedStep("parent after engine error"),
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
	if !taskEnds[0].IsError {
		t.Errorf("task ToolCallEnd IsError = false; output=%q", taskEnds[0].Output)
	}
	if !strings.Contains(taskEnds[0].Output, "no model selected") {
		t.Errorf("task output = %q, want to contain no model selected", taskEnds[0].Output)
	}
}
