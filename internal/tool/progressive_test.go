package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestProgressiveCreatePromptOnly(t *testing.T) {
	ResetCompatUseCounts()
	tc := allowAll(t.TempDir())
	var got TaskRequest
	tc.SpawnTask = func(_ context.Context, req TaskRequest) (TaskResult, error) {
		got = req
		return TaskResult{Output: "started", Status: "started", SessionID: "s1", DelegationID: "d1", Lifecycle: "working"}, nil
	}
	res, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"prompt": "do the thing",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "do the thing" {
		t.Fatalf("prompt = %q", got.Prompt)
	}
	if !strings.Contains(res.Title, "d1") {
		t.Fatalf("title = %q", res.Title)
	}
	if CompatUseCount("task") != 0 {
		t.Fatalf("task itself should not count as compat use")
	}
}

func TestProgressiveCreateAdvancedFields(t *testing.T) {
	tc := allowAll(t.TempDir())
	var got TaskRequest
	tc.SpawnTask = func(_ context.Context, req TaskRequest) (TaskResult, error) {
		got = req
		return TaskResult{Status: "queued", DelegationID: "d9", Lifecycle: "queued", Output: "queued"}, nil
	}
	_, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"prompt":         "after deps",
		"criteria":       []string{"tests green"},
		"deps":           []string{"d1"},
		"subscribe":      []string{"done"},
		"route":          "auto",
		"specialty":      "test",
		"max_concurrent": 2,
		"budget":         map[string]any{"max_tool_calls": 10},
		"verify":         []map[string]any{{"kind": "schema", "value": "handoff"}},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Criteria) != 1 || got.Criteria[0] != "tests green" {
		t.Fatalf("criteria = %#v", got.Criteria)
	}
	if got.Route != "auto" || got.Specialty != "test" || got.MaxConcurrent != 2 {
		t.Fatalf("route = %#v", got)
	}
	if got.Budget.MaxToolCalls != 10 {
		t.Fatalf("budget = %#v", got.Budget)
	}
	if len(got.Verify) != 1 || got.Verify[0].Kind != "schema" {
		t.Fatalf("verify = %#v", got.Verify)
	}
}

func TestProgressiveActionCreateExplicit(t *testing.T) {
	tc := allowAll(t.TempDir())
	called := false
	tc.SpawnTask = func(context.Context, TaskRequest) (TaskResult, error) {
		called = true
		return TaskResult{Status: "started", SessionID: "s"}, nil
	}
	if _, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "create",
		"prompt": "x",
	}), tc); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected spawn")
	}
}

func TestProgressiveGetListTransition(t *testing.T) {
	tc := allowAll(t.TempDir())
	var got DelegateRequest
	tc.Delegate = func(_ context.Context, req DelegateRequest) (DelegateResult, error) {
		got = req
		switch req.Action {
		case "list":
			return DelegateResult{Action: "list", Items: []DelegationItem{{ID: "d1", State: "working", Version: 2}}}, nil
		case "get":
			return DelegateResult{Action: "get", Item: &DelegationItem{ID: "d1", State: "working", Version: 2}}, nil
		case "transition":
			return DelegateResult{Action: "transition", Item: &DelegationItem{ID: "d1", State: req.State, Version: 3}}, nil
		default:
			t.Fatalf("unexpected action %q", req.Action)
			return DelegateResult{}, nil
		}
	}

	res, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{"action": "list"}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "list" || !strings.Contains(res.Output, `"d1"`) {
		t.Fatalf("list got=%#v out=%s", got, res.Output)
	}

	res, err = NewTask().Execute(context.Background(), mustJSON(t, map[string]any{"action": "get", "id": "d1"}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "d1" || !strings.Contains(res.Title, "d1") {
		t.Fatalf("get got=%#v title=%q", got, res.Title)
	}

	res, err = NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"action":           "transition",
		"id":               "d1",
		"state":            "done",
		"expected_version": 2,
		"reason":           "verified",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "done" || got.ExpectedVersion != 2 || got.Reason != "verified" {
		t.Fatalf("transition = %#v", got)
	}
	if !strings.Contains(res.Output, `"done"`) {
		t.Fatalf("output = %s", res.Output)
	}
}

func TestProgressiveStatusMessageCancelWait(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.TaskStatus = func(_ context.Context, req TaskStatusRequest) (TaskStatusResult, error) {
		return TaskStatusResult{
			SessionID: req.SessionID, State: "working", Elapsed: "1s",
			DelegationID: "d1", Lifecycle: "working", HasHandoff: false,
		}, nil
	}
	tc.TaskMessage = func(_ context.Context, req TaskMessageRequest) (TaskMessageResult, error) {
		return TaskMessageResult{SessionID: req.SessionID, Status: "accepted", State: "working"}, nil
	}
	tc.TaskInterrupt = func(_ context.Context, req TaskInterruptRequest) (TaskInterruptResult, error) {
		return TaskInterruptResult{SessionID: req.SessionID, State: "canceled", Detail: "ok"}, nil
	}
	tc.Wait = func(_ context.Context, req WaitRequest) (WaitResult, error) {
		return WaitResult{Outcome: WaitOutcomeMatched, Event: WaitEventTaskDone, SessionID: req.SessionID}, nil
	}

	res, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "status", "id": "child-1",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"lifecycle":"working"`) {
		t.Fatalf("status = %s", res.Output)
	}

	res, err = NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "message", "session_id": "child-1", "text": "keep going",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"accepted"`) {
		t.Fatalf("message = %s", res.Output)
	}

	res, err = NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "cancel", "id": "child-1",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"canceled"`) {
		t.Fatalf("cancel = %s", res.Output)
	}

	res, err = NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"action":          "wait",
		"events":          []string{"task.done"},
		"timeout_seconds": 5,
		"id":              "child-1",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"matched"`) {
		t.Fatalf("wait = %s", res.Output)
	}
}

func TestProgressiveEmptyArgsRequirePrompt(t *testing.T) {
	tc := allowAll(t.TempDir())
	// Omitted action defaults to create → empty prompt error.
	_, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("err = %v", err)
	}
}

func TestCompatDelegateCreateParityAndTelemetry(t *testing.T) {
	ResetCompatUseCounts()
	tc := allowAll(t.TempDir())
	var got TaskRequest
	tc.SpawnTask = func(_ context.Context, req TaskRequest) (TaskResult, error) {
		got = req
		return TaskResult{Status: "started", SessionID: "s", DelegationID: "d3", Lifecycle: "working", Output: "ok"}, nil
	}
	// delegate create goes through progressive → SpawnTask when action=create
	// Actually progressiveLifecycle is used for get/list/transition, create uses progressiveCreate via SpawnTask.
	// But delegate create uses executeProgressive with action create → progressiveCreate → SpawnTask.
	// Wait - progressiveCreate uses SpawnTask, not Delegate. Good.
	// But for delegate create we need SpawnTask. Engine wires both.
	// For lifecycle create via DelegateRequest path in engine, Delegate is used.
	// Looking at progressiveCreate - it uses SpawnTask. So delegate create uses SpawnTask.
	// Engine's e.delegate create also uses spawnChild. Both paths OK.
	// But wait - when using Delegate tool with action create, executeProgressive → progressiveCreate → SpawnTask.
	// That means Delegate handler is NOT called for create! Previously it was.
	// Engine injects both SpawnTask and Delegate. SpawnTask = spawnChild which creates lifecycle.
	// So delegate create via progressiveCreate → SpawnTask is CORRECT and has field parity.
	// Good.

	res, err := NewDelegate().Execute(context.Background(), mustJSON(t, map[string]any{
		"action":    "create",
		"prompt":    "via delegate",
		"route":     "auto",
		"specialty": "explore",
		"budget":    map[string]any{"max_tokens": 100},
		"criteria":  []string{"ok"},
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "via delegate" || got.Route != "auto" || got.Specialty != "explore" {
		t.Fatalf("got = %#v", got)
	}
	if got.Budget.MaxTokens != 100 || len(got.Criteria) != 1 {
		t.Fatalf("advanced = %#v", got)
	}
	if CompatUseCount(CompatToolDelegate) != 1 {
		t.Fatalf("compat count = %d", CompatUseCount(CompatToolDelegate))
	}
	if !strings.Contains(string(res.Metadata), `"deprecatedTool":"delegate"`) {
		t.Fatalf("metadata = %s", res.Metadata)
	}
	if !strings.Contains(string(res.Metadata), `"prefer":"task"`) {
		t.Fatalf("metadata missing prefer: %s", res.Metadata)
	}
}

func TestCompatTaskStatusTelemetry(t *testing.T) {
	ResetCompatUseCounts()
	tc := allowAll(t.TempDir())
	tc.TaskStatus = func(context.Context, TaskStatusRequest) (TaskStatusResult, error) {
		return TaskStatusResult{SessionID: "s1", State: "completed", HasHandoff: true, Handoff: CompletionHandoff{Summary: "done"}}, nil
	}
	res, err := NewTaskStatus().Execute(context.Background(), mustJSON(t, map[string]any{
		"session_id": "s1",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if CompatUseCount(CompatToolTaskStatus) != 1 {
		t.Fatalf("count = %d", CompatUseCount(CompatToolTaskStatus))
	}
	if !strings.Contains(res.Output, `"summary":"done"`) {
		t.Fatalf("output = %s", res.Output)
	}
	if !strings.Contains(string(res.Metadata), "deprecatedTool") {
		t.Fatalf("metadata = %s", res.Metadata)
	}
}

func TestCompatTaskInterruptAndWait(t *testing.T) {
	ResetCompatUseCounts()
	tc := allowAll(t.TempDir())
	tc.TaskInterrupt = func(context.Context, TaskInterruptRequest) (TaskInterruptResult, error) {
		return TaskInterruptResult{SessionID: "s1", State: "canceled"}, nil
	}
	tc.Wait = func(context.Context, WaitRequest) (WaitResult, error) {
		return WaitResult{Outcome: WaitOutcomeTimeout, TimeoutSeconds: 1}, nil
	}
	if _, err := NewTaskInterrupt().Execute(context.Background(), mustJSON(t, map[string]any{
		"session_id": "s1",
	}), tc); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWait().Execute(context.Background(), mustJSON(t, map[string]any{
		"events":          []string{"task.done"},
		"timeout_seconds": 1,
	}), tc); err != nil {
		t.Fatal(err)
	}
	if CompatUseCount(CompatToolTaskInterrupt) != 1 || CompatUseCount(CompatToolWait) != 1 {
		t.Fatalf("counts = %#v", CompatUseSnapshot())
	}
}

func TestProgressiveAndCompatSameStatusPayload(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.TaskStatus = func(context.Context, TaskStatusRequest) (TaskStatusResult, error) {
		return TaskStatusResult{
			SessionID: "s1", State: "completed", Elapsed: "2s",
			HasHandoff: true, Handoff: CompletionHandoff{Summary: "shipped", FilesChanged: []string{"a.go"}},
			DelegationID: "d1", Lifecycle: "done", Version: 3,
		}, nil
	}
	viaTask, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "status", "id": "s1",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	viaCompat, err := NewTaskStatus().Execute(context.Background(), mustJSON(t, map[string]any{
		"session_id": "s1",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	// Strip metadata differences — compare model-facing Output.
	var a, b map[string]any
	if err := json.Unmarshal([]byte(viaTask.Output), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(viaCompat.Output), &b); err != nil {
		t.Fatal(err)
	}
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if string(ab) != string(bb) {
		t.Fatalf("status payload mismatch:\ntask=%s\ncompat=%s", ab, bb)
	}
}

func TestProgressiveDescriptionDocumentsAPI(t *testing.T) {
	d := NewTask().Description()
	for _, needle := range []string{
		"Progressive",
		"prompt only",
		"action",
		"transition",
		"cancel",
		"wait",
		"compatibility",
		"team_task",
		"plan_delegate",
	} {
		if !strings.Contains(strings.ToLower(d), strings.ToLower(needle)) {
			t.Errorf("task description missing %q", needle)
		}
	}
}

func TestGuidanceSingleDecisionPath(t *testing.T) {
	text := BuildGuidance([]GuidanceEntry{
		{Name: "task"}, {Name: "task_status"}, {Name: "delegate"}, {Name: "wait"},
		{Name: "task_read"}, {Name: "task_message"}, {Name: "task_interrupt"},
	})
	if !strings.Contains(text, "progressive `task`") {
		t.Fatalf("missing progressive guidance:\n%s", text)
	}
	if !strings.Contains(text, "compatibility shims") {
		t.Fatalf("missing compat note:\n%s", text)
	}
	// Should not recommend delegate as primary lifecycle path when task is present.
	if strings.Contains(text, "Use `delegate` for first-class lifecycle") {
		t.Fatalf("old overlapping delegate recommendation still present:\n%s", text)
	}
}

func TestNormalizeProgressiveAction(t *testing.T) {
	a, err := normalizeProgressiveAction("", "hi")
	if err != nil || a != ProgressiveCreate {
		t.Fatalf("got %q err=%v", a, err)
	}
	a, err = normalizeProgressiveAction("interrupt", "")
	if err != nil || a != ProgressiveCancel {
		t.Fatalf("interrupt alias = %q err=%v", a, err)
	}
	if _, err := normalizeProgressiveAction("nope", ""); err == nil {
		t.Fatal("expected error")
	}
}
