package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTaskControlNilHandlers(t *testing.T) {
	tc := allowAll(t.TempDir())
	cases := []struct {
		name string
		tool Tool
		args map[string]any
		want string
	}{
		{"status", NewTaskStatus(), map[string]any{"session_id": "s1"}, "not available"},
		{"read", NewTaskRead(), map[string]any{"session_id": "s1"}, "not available"},
		{"message", NewTaskMessage(), map[string]any{"session_id": "s1", "text": "hi"}, "not available"},
		{"interrupt", NewTaskInterrupt(), map[string]any{"session_id": "s1"}, "not available"},
	}
	for _, tcase := range cases {
		t.Run(tcase.name, func(t *testing.T) {
			_, err := tcase.tool.Execute(context.Background(), mustJSON(t, tcase.args), tc)
			if err == nil || !strings.Contains(err.Error(), tcase.want) {
				t.Fatalf("err = %v, want %q", err, tcase.want)
			}
		})
	}
}

func TestTaskStatusSuccess(t *testing.T) {
	tc := allowAll(t.TempDir())
	var got TaskStatusRequest
	tc.TaskStatus = func(_ context.Context, req TaskStatusRequest) (TaskStatusResult, error) {
		got = req
		return TaskStatusResult{
			SessionID:      req.SessionID,
			State:          "working",
			Elapsed:        "5s",
			CurrentTool:    "grep",
			LatestActivity: []string{"searching"},
		}, nil
	}
	res, err := NewTaskStatus().Execute(context.Background(), mustJSON(t, map[string]any{
		"session_id":     "abc12345",
		"include_recent": true,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "abc12345" || !got.IncludeRecent {
		t.Fatalf("req = %#v", got)
	}
	if !strings.Contains(res.Output, `"state":"working"`) || !strings.Contains(res.Output, `"current_tool":"grep"`) {
		t.Fatalf("output = %s", res.Output)
	}
	if !strings.Contains(res.Title, "working") {
		t.Fatalf("title = %q", res.Title)
	}
}

func TestTaskReadBounded(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.TaskRead = func(_ context.Context, req TaskReadRequest) (TaskReadResult, error) {
		if req.Last != 3 {
			t.Fatalf("Last = %d, want 3", req.Last)
		}
		return TaskReadResult{
			SessionID: req.SessionID,
			Entries: []TaskTranscriptEntry{
				{Index: 1, Kind: "user", Summary: "hi"},
			},
			Offset: 0, Limit: 3, Total: 1, NextOffset: -1,
		}, nil
	}
	res, err := NewTaskRead().Execute(context.Background(), mustJSON(t, map[string]any{
		"session_id": "s1",
		"last":       3,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"kind":"user"`) {
		t.Fatalf("output = %s", res.Output)
	}
}

func TestTaskMessageRejectedSurfacesError(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.TaskMessage = func(context.Context, TaskMessageRequest) (TaskMessageResult, error) {
		return TaskMessageResult{
			SessionID: "s1",
			Status:    "rejected",
			State:     "completed",
			Detail:    "child session is closed (completed)",
		}, nil
	}
	_, err := NewTaskMessage().Execute(context.Background(), mustJSON(t, map[string]any{
		"session_id": "s1",
		"text":       "more work",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("err = %v", err)
	}
}

func TestTaskMessageAccepted(t *testing.T) {
	tc := allowAll(t.TempDir())
	var got TaskMessageRequest
	tc.TaskMessage = func(_ context.Context, req TaskMessageRequest) (TaskMessageResult, error) {
		got = req
		return TaskMessageResult{SessionID: req.SessionID, Status: "queued", State: "working", Detail: "queued"}, nil
	}
	res, err := NewTaskMessage().Execute(context.Background(), mustJSON(t, map[string]any{
		"session_id": "child-1",
		"text":       "focus on tests",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "focus on tests" {
		t.Fatalf("text = %q", got.Text)
	}
	if !strings.Contains(res.Output, `"status":"queued"`) {
		t.Fatalf("output = %s", res.Output)
	}
}

func TestTaskInterruptSuccess(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.TaskInterrupt = func(_ context.Context, req TaskInterruptRequest) (TaskInterruptResult, error) {
		return TaskInterruptResult{SessionID: req.SessionID, State: "canceled", Detail: "child interrupted"}, nil
	}
	res, err := NewTaskInterrupt().Execute(context.Background(), mustJSON(t, map[string]any{
		"session_id": "s1",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"state":"canceled"`) {
		t.Fatalf("output = %s", res.Output)
	}
}

func TestTaskControlValidation(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.TaskStatus = func(context.Context, TaskStatusRequest) (TaskStatusResult, error) {
		return TaskStatusResult{}, errors.New("should not run")
	}
	if _, err := NewTaskStatus().Execute(context.Background(), mustJSON(t, map[string]any{}), tc); err == nil {
		t.Fatal("expected empty session_id error")
	}
	if _, err := NewTaskMessage().Execute(context.Background(), mustJSON(t, map[string]any{
		"session_id": "s", "text": "  ",
	}), tc); err == nil {
		t.Fatal("expected empty text error")
	}
	if _, err := NewTaskStatus().Execute(context.Background(), json.RawMessage(`{`), tc); err == nil {
		t.Fatal("expected invalid json")
	}
}

func TestTaskControlPermissionDenied(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.Ask = func(context.Context, AskRequest) error { return errors.New("denied") }
	tc.TaskStatus = func(context.Context, TaskStatusRequest) (TaskStatusResult, error) {
		t.Fatal("must not run after deny")
		return TaskStatusResult{}, nil
	}
	if _, err := NewTaskStatus().Execute(context.Background(), mustJSON(t, map[string]any{
		"session_id": "s1",
	}), tc); err == nil {
		t.Fatal("expected deny")
	}
}

func TestClampTaskReadLimit(t *testing.T) {
	if got := ClampTaskReadLimit(0); got != taskReadDefaultLimit {
		t.Fatalf("default = %d", got)
	}
	if got := ClampTaskReadLimit(1000); got != taskReadMaxLimit {
		t.Fatalf("max = %d", got)
	}
	if got := ClampTaskReadLimit(7); got != 7 {
		t.Fatalf("passthrough = %d", got)
	}
}

func TestTaskControlToolNames(t *testing.T) {
	want := map[string]Tool{
		"task_status":    NewTaskStatus(),
		"task_read":      NewTaskRead(),
		"task_message":   NewTaskMessage(),
		"task_interrupt": NewTaskInterrupt(),
	}
	for name, tool := range want {
		if tool.Name() != name {
			t.Errorf("Name() = %q, want %q", tool.Name(), name)
		}
		if tool.Description() == "" || len(tool.Schema()) == 0 {
			t.Errorf("%s missing description/schema", name)
		}
	}
}
