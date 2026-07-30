package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTaskEmptyPrompt(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.SpawnTask = func(context.Context, TaskRequest) (TaskResult, error) {
		t.Fatal("SpawnTask should not be called for empty prompt")
		return TaskResult{}, nil
	}
	for _, args := range []json.RawMessage{
		mustJSON(t, map[string]any{"prompt": ""}),
		mustJSON(t, map[string]any{"prompt": "   "}),
		mustJSON(t, map[string]any{}),
	} {
		_, err := NewTask().Execute(context.Background(), args, tc)
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Errorf("args %s: err = %v, want empty prompt error", args, err)
		}
	}
}

func TestTaskNilSpawnTask(t *testing.T) {
	tc := allowAll(t.TempDir())
	// SpawnTask left nil.
	_, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"prompt": "do work",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v, want not available", err)
	}
}

func TestTaskSuccessfulSpawn(t *testing.T) {
	tc := allowAll(t.TempDir())
	var gotReq TaskRequest
	tc.SpawnTask = func(_ context.Context, req TaskRequest) (TaskResult, error) {
		gotReq = req
		return TaskResult{Output: "Started child session abc", Status: "started", SessionID: "abc12345xyz"}, nil
	}
	res, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"prompt": "investigate flaky test",
		"agent":  "plan",
		"model":  "gpt-test",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "Started child session abc" {
		t.Errorf("output = %q, want started notice", res.Output)
	}
	if res.Title != "task abc12345" {
		t.Errorf("title = %q, want task abc12345", res.Title)
	}
	if gotReq.Prompt != "investigate flaky test" || gotReq.Agent != "plan" || gotReq.Model != "gpt-test" {
		t.Errorf("SpawnTask req = %#v", gotReq)
	}
}

func TestTaskPassesEffort(t *testing.T) {
	tc := allowAll(t.TempDir())
	var gotReq TaskRequest
	tc.SpawnTask = func(_ context.Context, req TaskRequest) (TaskResult, error) {
		gotReq = req
		return TaskResult{Output: "started", Status: "started", SessionID: "s1"}, nil
	}
	if _, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"prompt": "think hard",
		"effort": "xhigh",
	}), tc); err != nil {
		t.Fatal(err)
	}
	if gotReq.Effort != "xhigh" {
		t.Errorf("effort = %q, want xhigh", gotReq.Effort)
	}
}

func TestTaskPassesName(t *testing.T) {
	tc := allowAll(t.TempDir())
	var gotReq TaskRequest
	tc.SpawnTask = func(_ context.Context, req TaskRequest) (TaskResult, error) {
		gotReq = req
		return TaskResult{Output: "started", Status: "started", SessionID: "abc12345xyz", Name: "explorer"}, nil
	}
	res, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"prompt": "scan tree",
		"name":   "explorer",
		"agent":  "explore",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if gotReq.Name != "explorer" || gotReq.Agent != "explore" {
		t.Fatalf("SpawnTask req = %#v", gotReq)
	}
	if res.Title != "task explorer" {
		t.Fatalf("title = %q, want task explorer", res.Title)
	}
	if !strings.Contains(string(res.Metadata), `"name":"explorer"`) {
		t.Fatalf("metadata = %s, want name", res.Metadata)
	}
}

func TestTaskCanceledOrFailedStatus(t *testing.T) {
	cases := []struct {
		status string
		output string
		want   string
	}{
		{status: "canceled", output: "stopped early", want: "stopped early"},
		{status: "failed", output: "boom", want: "boom"},
		{status: "canceled", output: "", want: "task canceled"},
		{status: "failed", output: "", want: "task failed"},
		{status: "unknown", output: "", want: "task failed"},
	}
	for _, tc := range cases {
		ctx := allowAll(t.TempDir())
		ctx.SpawnTask = func(context.Context, TaskRequest) (TaskResult, error) {
			return TaskResult{Output: tc.output, Status: tc.status}, nil
		}
		res, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
			"prompt": "work",
		}), ctx)
		if err == nil {
			t.Errorf("status %q: expected error", tc.status)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("status %q: err = %v, want containing %q", tc.status, err, tc.want)
		}
		if res.Output != tc.want && res.Output != tc.output {
			// On non-empty output the tool returns that output; on empty it synthesizes.
			if tc.output != "" && res.Output != tc.output {
				t.Errorf("status %q: output = %q, want %q", tc.status, res.Output, tc.output)
			}
			if tc.output == "" && res.Output != tc.want {
				t.Errorf("status %q: output = %q, want %q", tc.status, res.Output, tc.want)
			}
		}
	}
}

func TestTaskPermissionAsk(t *testing.T) {
	dir := t.TempDir()
	var asked AskRequest
	tc := &Context{
		WorkDir: dir,
		Ask: func(_ context.Context, req AskRequest) error {
			asked = req
			return nil
		},
		SpawnTask: func(context.Context, TaskRequest) (TaskResult, error) {
			return TaskResult{Output: "ok", Status: "started", SessionID: "s1"}, nil
		},
	}
	if _, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"prompt": "go",
	}), tc); err != nil {
		t.Fatal(err)
	}
	if asked.Permission != "task" {
		t.Errorf("permission = %q, want task", asked.Permission)
	}

	tc.Ask = func(context.Context, AskRequest) error {
		return errors.New("denied")
	}
	tc.SpawnTask = func(context.Context, TaskRequest) (TaskResult, error) {
		t.Fatal("SpawnTask must not run after deny")
		return TaskResult{}, nil
	}
	if _, err := NewTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"prompt": "go",
	}), tc); err == nil {
		t.Fatal("expected permission deny error")
	}
}

func TestTaskInvalidArgs(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewTask().Execute(context.Background(), json.RawMessage(`{`), tc)
	if err == nil {
		t.Fatal("expected invalid args error")
	}
}

func TestCloneWithoutOmitsTask(t *testing.T) {
	r := NewRegistry(NewRead(), NewTask(), NewBash())
	child := r.CloneWithout("task")
	if _, ok := child.Get("task"); ok {
		t.Fatal("child registry still has task")
	}
	if _, ok := child.Get("read"); !ok {
		t.Fatal("missing read")
	}
	if _, ok := child.Get("bash"); !ok {
		t.Fatal("missing bash")
	}
	// Parent unchanged.
	if _, ok := r.Get("task"); !ok {
		t.Fatal("parent lost task")
	}
	schemas := child.Schemas()
	if len(schemas) != 2 || schemas[0].Name != "read" || schemas[1].Name != "bash" {
		t.Fatalf("child schemas = %+v", schemas)
	}
}
