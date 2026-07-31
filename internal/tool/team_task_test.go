package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestTeamTaskNilHandler(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewTeamTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "list",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v", err)
	}
}

func TestTeamTaskListAndCreate(t *testing.T) {
	tc := allowAll(t.TempDir())
	var got TeamTaskRequest
	tc.TeamTask = func(_ context.Context, req TeamTaskRequest) (TeamTaskResult, error) {
		got = req
		if req.Action == "create" {
			return TeamTaskResult{
				LeadID: "L",
				Action: "create",
				Task:   &TeamTaskItem{ID: "t1", Content: req.Content, Status: "pending", Version: 1},
				Tasks:  []TeamTaskItem{{ID: "t1", Content: req.Content, Status: "pending", Version: 1}},
			}, nil
		}
		return TeamTaskResult{LeadID: "L", Action: "list", Tasks: []TeamTaskItem{}}, nil
	}

	res, err := NewTeamTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"action":  "create",
		"content": "ship board",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "create" || got.Content != "ship board" {
		t.Fatalf("req = %+v", got)
	}
	if !strings.Contains(res.Title, "t1") {
		t.Fatalf("title = %q", res.Title)
	}
	var parsed TeamTaskResult
	if err := json.Unmarshal([]byte(res.Output), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Task == nil || parsed.Task.ID != "t1" {
		t.Fatalf("output = %s", res.Output)
	}
}

func TestTeamTaskClaimConflictSurfaced(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.TeamTask = func(context.Context, TeamTaskRequest) (TeamTaskResult, error) {
		return TeamTaskResult{
			Action:   "claim",
			Conflict: true,
			Detail:   `task "t1" is claimed by A`,
			Task:     &TeamTaskItem{ID: "t1", Owner: "A", Status: "claimed", Version: 2},
		}, nil
	}
	res, err := NewTeamTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "claim",
		"id":     "t1",
	}), tc)
	if err != nil {
		t.Fatalf("conflict should not be tool error: %v", err)
	}
	if !strings.Contains(res.Title, "conflict") {
		t.Fatalf("title = %q", res.Title)
	}
	if !strings.Contains(res.Output, `"conflict":true`) && !strings.Contains(res.Output, `"conflict": true`) {
		t.Fatalf("output = %s", res.Output)
	}
}

func TestTeamTaskValidation(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.TeamTask = func(context.Context, TeamTaskRequest) (TeamTaskResult, error) {
		t.Fatal("handler should not run")
		return TeamTaskResult{}, nil
	}
	cases := []map[string]any{
		{},
		{"action": "create"},
		{"action": "claim"},
		{"action": "complete"},
		{"action": "update", "id": "t1"},
		{"action": "nope"},
	}
	for _, args := range cases {
		if _, err := NewTeamTask().Execute(context.Background(), mustJSON(t, args), tc); err == nil {
			t.Fatalf("expected validation error for %#v", args)
		}
	}
}

func TestTeamTaskPermissionDenied(t *testing.T) {
	called := false
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask: func(context.Context, AskRequest) error {
			return errors.New("denied")
		},
		TeamTask: func(context.Context, TeamTaskRequest) (TeamTaskResult, error) {
			called = true
			return TeamTaskResult{}, nil
		},
	}
	if _, err := NewTeamTask().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "list",
	}), tc); err == nil {
		t.Fatal("expected permission error")
	}
	if called {
		t.Fatal("handler must not run when denied")
	}
}

func TestTeamTaskDescriptionMentionsTodoWrite(t *testing.T) {
	d := NewTeamTask().Description()
	for _, needle := range []string{"todowrite", "claim", "expected_version", "lead"} {
		if !strings.Contains(d, needle) {
			t.Fatalf("description missing %q:\n%s", needle, d)
		}
	}
}
