package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskImplementsProgressive(t *testing.T) {
	var _ Progressive = taskTool{}
	tt := NewTask()
	p, ok := tt.(Progressive)
	if !ok {
		t.Fatal("task must implement Progressive")
	}
	basic := p.BasicSchema()
	adv := tt.Schema()
	if len(basic) == 0 || len(adv) == 0 {
		t.Fatal("empty schemas")
	}
	if string(basic) == string(adv) {
		t.Fatal("basic and advanced schemas must differ")
	}
	// Basic enum is create/status/wait/cancel only.
	var basicObj map[string]any
	if err := json.Unmarshal(basic, &basicObj); err != nil {
		t.Fatal(err)
	}
	props := basicObj["properties"].(map[string]any)
	action := props["action"].(map[string]any)
	enum := action["enum"].([]any)
	got := map[string]bool{}
	for _, e := range enum {
		got[e.(string)] = true
	}
	for _, want := range []string{"create", "status", "wait", "cancel"} {
		if !got[want] {
			t.Errorf("basic enum missing %q", want)
		}
	}
	for _, no := range []string{"get", "list", "read", "message", "transition"} {
		if got[no] {
			t.Errorf("basic enum should not include advanced action %q", no)
		}
	}
	// Advanced keeps full action set.
	if !strings.Contains(string(adv), `"transition"`) || !strings.Contains(string(adv), `"context_bundle"`) {
		t.Fatal("advanced schema missing full fields")
	}
	if p.BasicDescription() == "" || !strings.Contains(p.BasicDescription(), "basic schema") {
		t.Fatalf("BasicDescription = %q", p.BasicDescription())
	}
	// Single tool name.
	if tt.Name() != "task" {
		t.Fatalf("Name = %q", tt.Name())
	}
}

func TestSchemasForProviderStartsBasicTask(t *testing.T) {
	reg := NewRegistry(NewRead(), NewTask())
	reg.Register(NewToolSearch(reg))

	got := reg.SchemasForProvider()
	var taskSchema json.RawMessage
	taskCount := 0
	for _, s := range got {
		if s.Name == "task" {
			taskCount++
			taskSchema = s.InputSchema
			if !strings.Contains(s.Description, "basic schema") && !strings.Contains(s.Description, "prompt-only") {
				// BasicDescription should be used
				if s.Description == NewTask().Description() {
					t.Fatal("provider should use basic description before promotion")
				}
			}
		}
	}
	if taskCount != 1 {
		t.Fatalf("want exactly one task tool, got %d", taskCount)
	}
	if strings.Contains(string(taskSchema), `"transition"`) {
		t.Fatal("basic provider schema should not expose transition")
	}
	if !strings.Contains(string(taskSchema), `"status"`) {
		t.Fatal("basic provider schema should expose status")
	}

	// Schemas() always full for toolsearch.
	full := reg.Schemas()
	for _, s := range full {
		if s.Name == "task" {
			if !strings.Contains(string(s.InputSchema), `"transition"`) {
				t.Fatal("Schemas() must expose advanced task schema")
			}
			if s.Description != NewTask().Description() {
				t.Fatal("Schemas() must use full Description")
			}
		}
	}

	if reg.SchemaLevel("task") != SchemaBasic {
		t.Fatalf("SchemaLevel = %v, want basic", reg.SchemaLevel("task"))
	}
	if reg.SchemaAdvanced("task") {
		t.Fatal("task should not be advanced yet")
	}
}

func TestPromoteSchemaElevatesTask(t *testing.T) {
	reg := NewRegistry(NewTask(), NewRead())
	reg.PromoteSchema("task")
	if !reg.SchemaAdvanced("task") {
		t.Fatal("expected advanced after PromoteSchema")
	}
	for _, s := range reg.SchemasForProvider() {
		if s.Name == "task" {
			if !strings.Contains(string(s.InputSchema), `"transition"`) {
				t.Fatal("promoted schema missing transition")
			}
			if s.Description != NewTask().Description() {
				t.Fatal("promoted description should be full")
			}
		}
	}
	// Idempotent.
	reg.PromoteSchema("task", "nope", "read")
	if !reg.SchemaAdvanced("task") {
		t.Fatal("still advanced")
	}
	if reg.SchemaLevel("read") != SchemaAdvanced {
		t.Fatal("non-progressive stays advanced")
	}
}

func TestToolSearchPromotesTaskAdvanced(t *testing.T) {
	reg := NewRegistry(NewRead(), NewTask())
	ts := NewToolSearch(reg)
	reg.Register(ts)

	if reg.SchemaAdvanced("task") {
		t.Fatal("task should start basic")
	}
	res, err := ts.Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "task",
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "- task:") {
		t.Fatalf("output = %q", res.Output)
	}
	if !reg.SchemaAdvanced("task") {
		t.Fatal("toolsearch should promote task to advanced")
	}
	if !strings.Contains(res.Output, "advanced") && !strings.Contains(res.Output, "full schemas") {
		t.Fatalf("output should mention schema promotion: %q", res.Output)
	}
}

func TestNoteToolCallPromotesOnAdvancedArgs(t *testing.T) {
	reg := NewRegistry(NewTask(), NewSleep())
	reg.SetDeferLoading(true)

	// Basic create does not promote.
	reg.NoteToolCall("task", mustJSON(t, map[string]any{"prompt": "do thing"}))
	if reg.SchemaAdvanced("task") {
		t.Fatal("prompt-only create should stay basic")
	}

	// Advanced action promotes.
	reg.NoteToolCall("task", mustJSON(t, map[string]any{"action": "transition", "id": "d1", "state": "done"}))
	if !reg.SchemaAdvanced("task") {
		t.Fatal("transition should promote advanced")
	}

	// Deferred tool still discovered.
	if reg.Discovered("sleep") {
		t.Fatal("sleep not called")
	}
	reg.NoteToolCall("sleep", mustJSON(t, map[string]any{"seconds": 1}))
	if !reg.Discovered("sleep") {
		t.Fatal("sleep should be discovered")
	}
}

func TestArgsNeedAdvancedSchema(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want bool
	}{
		{"empty", nil, false},
		{"prompt only", map[string]any{"prompt": "x"}, false},
		{"create", map[string]any{"action": "create", "prompt": "x"}, false},
		{"status", map[string]any{"action": "status", "id": "s"}, false},
		{"wait", map[string]any{"action": "wait", "timeout_seconds": 5}, false},
		{"cancel", map[string]any{"action": "cancel", "id": "s"}, false},
		{"get", map[string]any{"action": "get", "id": "s"}, true},
		{"list", map[string]any{"action": "list"}, true},
		{"read", map[string]any{"action": "read", "id": "s"}, true},
		{"message", map[string]any{"action": "message", "id": "s", "text": "hi"}, true},
		{"transition", map[string]any{"action": "transition", "id": "s", "state": "done"}, true},
		{"agent pin", map[string]any{"prompt": "x", "agent": "explore"}, true},
		{"budget", map[string]any{"prompt": "x", "budget": map[string]any{"max_tokens": 100}}, true},
		{"force false ignored", map[string]any{"prompt": "x", "force_delegate": false}, false},
		{"force true", map[string]any{"prompt": "x", "force_delegate": true}, true},
	}
	for _, tc := range cases {
		var raw json.RawMessage
		if tc.args != nil {
			raw = mustJSON(t, tc.args)
		}
		got := ArgsNeedAdvancedSchema("task", raw)
		if got != tc.want {
			t.Errorf("%s: ArgsNeedAdvancedSchema = %v, want %v", tc.name, got, tc.want)
		}
	}
	if ArgsNeedAdvancedSchema("read", mustJSON(t, map[string]any{"path": "a"})) {
		t.Fatal("non-progressive should be false")
	}
}

func TestTaskExecuteUnchangedUnderBasicOrAdvanced(t *testing.T) {
	// Executor accepts full contract regardless of schema level.
	var got TaskRequest
	tc := allowAll(t.TempDir())
	tc.SpawnTask = func(_ context.Context, req TaskRequest) (TaskResult, error) {
		got = req
		return TaskResult{SessionID: "child-1", Status: "started", Output: "ok"}, nil
	}

	// Advanced fields still execute when schema is basic (model may send them).
	res, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"prompt": "ship it",
		"agent":  "explore",
		"name":   "scout",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "ship it" || got.Agent != "explore" || got.Name != "scout" {
		t.Fatalf("got %+v", got)
	}
	if res.Output != "ok" {
		t.Fatalf("output = %q", res.Output)
	}

	// status path
	tc.TaskStatus = func(_ context.Context, req TaskStatusRequest) (TaskStatusResult, error) {
		return TaskStatusResult{SessionID: req.SessionID, State: "working"}, nil
	}
	res, err = NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "status",
		"id":     "child-1",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "working") {
		t.Fatalf("status output = %q", res.Output)
	}
}

func TestCloneWithoutCopiesAdvanced(t *testing.T) {
	reg := NewRegistry(NewRead(), NewTask(), NewWebFetch())
	reg.PromoteSchema("task")
	reg.SetDeferLoading(true)
	reg.Discover("webfetch")

	child := reg.CloneWithout("task")
	if _, ok := child.Get("task"); ok {
		t.Fatal("task should be stripped from child")
	}
	// Parent still advanced.
	if !reg.SchemaAdvanced("task") {
		t.Fatal("parent advanced lost")
	}
	// Clone with task keeps advanced.
	child2 := reg.CloneWithout("webfetch")
	if !child2.SchemaAdvanced("task") {
		t.Fatal("child should copy advanced task")
	}
	if child2.Discovered("webfetch") {
		t.Fatal("webfetch should be stripped")
	}
}

func TestInvalidTaskInputsStable(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "nope",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "action must be") {
		t.Fatalf("want stable action error, got %v", err)
	}
	_, err = NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "create",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "prompt is empty") {
		t.Fatalf("want empty prompt error, got %v", err)
	}
}
