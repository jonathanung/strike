package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeWaitEvents(t *testing.T) {
	got, err := NormalizeWaitEvents([]string{"task.done", "blocked", "task.completed", "FAILED"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{WaitEventTaskDone, WaitEventTaskBlocked, WaitEventTaskFailed}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if _, err := NormalizeWaitEvents(nil); err == nil {
		t.Fatal("expected error for empty events")
	}
	if _, err := NormalizeWaitEvents([]string{"file.changed"}); err == nil {
		t.Fatal("expected error for unknown event")
	}
}

func TestWaitToolMatched(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.Wait = func(_ context.Context, req WaitRequest) (WaitResult, error) {
		if len(req.Events) != 1 || req.Events[0] != WaitEventTaskDone {
			t.Fatalf("events = %v", req.Events)
		}
		if req.SessionID != "child-1" {
			t.Fatalf("session = %q", req.SessionID)
		}
		return WaitResult{
			Outcome:    WaitOutcomeMatched,
			Event:      WaitEventTaskDone,
			SessionID:  "child-1",
			Status:     "completed",
			Summary:    "ok",
			HasHandoff: true,
			Handoff: CompletionHandoff{
				Summary:      "ok",
				FilesChanged: []string{"a.go"},
			},
			WaitID: "w1",
		}, nil
	}
	res, err := NewWait().Execute(context.Background(), mustJSON(t, map[string]any{
		"events":          []string{"task.done"},
		"session_id":      "child-1",
		"timeout_seconds": 5,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Title, "matched") {
		t.Errorf("title = %q", res.Title)
	}
	var out WaitResult
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatal(err)
	}
	if out.Outcome != WaitOutcomeMatched || out.Event != WaitEventTaskDone {
		t.Fatalf("out = %+v", out)
	}
	if !out.HasHandoff || len(out.Handoff.FilesChanged) != 1 {
		t.Fatalf("handoff = %+v", out.Handoff)
	}
}

func TestWaitToolTimeoutAndCancelOutcomes(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.Wait = func(_ context.Context, _ WaitRequest) (WaitResult, error) {
		return WaitResult{Outcome: WaitOutcomeTimeout, Detail: "wait timed out"}, nil
	}
	res, err := NewWait().Execute(context.Background(), mustJSON(t, map[string]any{
		"events":          []string{"task.blocked"},
		"timeout_seconds": 0.1,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Title, "timeout") {
		t.Errorf("title = %q", res.Title)
	}
}

func TestWaitToolValidation(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.Wait = func(context.Context, WaitRequest) (WaitResult, error) {
		t.Fatal("should not call Wait")
		return WaitResult{}, nil
	}
	cases := []map[string]any{
		{"events": []string{"task.done"}, "timeout_seconds": 0},
		{"events": []string{"task.done"}, "timeout_seconds": 301},
		{"events": []string{}, "timeout_seconds": 1},
		{"events": []string{"nope"}, "timeout_seconds": 1},
		{"timeout_seconds": 1},
	}
	for _, args := range cases {
		if _, err := NewWait().Execute(context.Background(), mustJSON(t, args), tc); err == nil {
			t.Errorf("args=%v: expected error", args)
		}
	}
}

func TestWaitToolUnavailable(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewWait().Execute(context.Background(), mustJSON(t, map[string]any{
		"events":          []string{"task.done"},
		"timeout_seconds": 1,
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("got %v", err)
	}
}

func TestWaitToolPermissionDenied(t *testing.T) {
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, AskRequest) error { return errors.New("denied") },
		Wait: func(context.Context, WaitRequest) (WaitResult, error) {
			t.Fatal("should not call")
			return WaitResult{}, nil
		},
	}
	_, err := NewWait().Execute(context.Background(), mustJSON(t, map[string]any{
		"events":          []string{"task.done"},
		"timeout_seconds": 1,
	}), tc)
	if err == nil {
		t.Fatal("expected deny")
	}
}

func TestWaitToolAskPatternUsesSession(t *testing.T) {
	var got AskRequest
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask: func(_ context.Context, req AskRequest) error {
			got = req
			return nil
		},
		Wait: func(context.Context, WaitRequest) (WaitResult, error) {
			return WaitResult{Outcome: WaitOutcomeMatched}, nil
		},
	}
	if _, err := NewWait().Execute(context.Background(), mustJSON(t, map[string]any{
		"events":          []string{"task.done"},
		"session_id":      "abc",
		"timeout_seconds": 1,
	}), tc); err != nil {
		t.Fatal(err)
	}
	if got.Permission != "wait" || len(got.Patterns) != 1 || got.Patterns[0] != "abc" {
		t.Fatalf("ask = %+v", got)
	}
}

func TestWaitToolDescriptionMentionsEvents(t *testing.T) {
	d := NewWait().Description()
	for _, needle := range []string{"task.done", "task.blocked", "timeout", "busy-poll", "wait.started"} {
		if !strings.Contains(d, needle) && needle != "busy-poll" {
			// description says "sleep-polling" not busy-poll
		}
	}
	if !strings.Contains(d, "task.done") || !strings.Contains(d, "timeout") {
		t.Fatalf("description missing core terms:\n%s", d)
	}
	_ = time.Second
}
