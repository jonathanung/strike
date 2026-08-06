package tool

import (
	"context"
	"strings"
	"testing"
)

func TestDelegateCreateRequiresPrompt(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.Delegate = func(context.Context, DelegateRequest) (DelegateResult, error) {
		t.Fatal("should not call")
		return DelegateResult{}, nil
	}
	_, err := NewDelegate().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "create",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("err = %v", err)
	}
}

func TestDelegateTransitionRequiresState(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewDelegate().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "transition",
		"id":     "d1",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("err = %v", err)
	}
}

func TestDelegateList(t *testing.T) {
	tc := allowAll(t.TempDir())
	var got DelegateRequest
	tc.Delegate = func(_ context.Context, req DelegateRequest) (DelegateResult, error) {
		got = req
		return DelegateResult{
			Action: "list",
			Items: []DelegationItem{
				{ID: "d1", State: "working", Version: 2},
			},
		}, nil
	}
	res, err := NewDelegate().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "list",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != "list" {
		t.Fatalf("req = %#v", got)
	}
	if !strings.Contains(res.Title, "list 1") {
		t.Fatalf("title = %q", res.Title)
	}
	if !strings.Contains(res.Output, `"id":"d1"`) {
		t.Fatalf("output = %s", res.Output)
	}
}

func TestDelegateCreatePassesLifecycleFields(t *testing.T) {
	tc := allowAll(t.TempDir())
	var got DelegateRequest
	tc.Delegate = func(_ context.Context, req DelegateRequest) (DelegateResult, error) {
		got = req
		return DelegateResult{
			Action: "create",
			Status: "queued",
			Item:   &DelegationItem{ID: "d2", State: "queued", Version: 1},
			Detail: "queued",
		}, nil
	}
	res, err := NewDelegate().Execute(context.Background(), mustJSON(t, map[string]any{
		"action":    "create",
		"prompt":    "do it",
		"criteria":  []string{"tests green"},
		"deps":      []string{"d1"},
		"subscribe": []string{"done"},
		"agent":     "build",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "do it" || len(got.Criteria) != 1 || got.Criteria[0] != "tests green" {
		t.Fatalf("got = %#v", got)
	}
	if len(got.Deps) != 1 || got.Deps[0] != "d1" {
		t.Fatalf("deps = %#v", got.Deps)
	}
	if !strings.Contains(res.Title, "d2") {
		t.Fatalf("title = %q", res.Title)
	}
}

func TestDelegateNilHandler(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewDelegate().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "list",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v", err)
	}
}

func TestDelegatePermissionDenied(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.Ask = func(context.Context, AskRequest) error {
		return &UserRejectedError{Message: "no"}
	}
	tc.Delegate = func(context.Context, DelegateRequest) (DelegateResult, error) {
		t.Fatal("must not run")
		return DelegateResult{}, nil
	}
	_, err := NewDelegate().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "list",
	}), tc)
	if err == nil {
		t.Fatal("expected deny")
	}
}

func TestDelegateDescriptionMentionsLifecycle(t *testing.T) {
	d := NewDelegate().Description()
	for _, want := range []string{"queued", "review", "criteria", "deps", "expected_version"} {
		if !strings.Contains(d, want) {
			t.Errorf("description missing %q", want)
		}
	}
}
