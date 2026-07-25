package tool

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestEnterPlanModeCallsSwitchAgent(t *testing.T) {
	var switched string
	tc := allowAll(t.TempDir())
	tc.SwitchAgent = func(name string) error {
		switched = name
		return nil
	}
	res, err := NewEnterPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if switched != "plan" {
		t.Errorf("SwitchAgent = %q, want plan", switched)
	}
	if res.Title != "plan mode" {
		t.Errorf("title = %q", res.Title)
	}
	if !strings.Contains(res.Output, "plan mode") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestExitPlanModeYesSwitchesBuild(t *testing.T) {
	var switched string
	tc := allowAll(t.TempDir())
	tc.SwitchAgent = func(name string) error {
		switched = name
		return nil
	}
	tc.AskUser = func(_ context.Context, req QuestionRequest) (QuestionResponse, error) {
		if len(req.Questions) != 1 {
			t.Fatalf("questions = %d", len(req.Questions))
		}
		return QuestionResponse{Answers: []string{"Yes"}}, nil
	}
	res, err := NewExitPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if switched != "build" {
		t.Errorf("SwitchAgent = %q, want build", switched)
	}
	if res.Title != "build mode" {
		t.Errorf("title = %q", res.Title)
	}
}

func TestExitPlanModeNoStays(t *testing.T) {
	var switched string
	tc := allowAll(t.TempDir())
	tc.SwitchAgent = func(name string) error {
		switched = name
		return nil
	}
	tc.AskUser = func(context.Context, QuestionRequest) (QuestionResponse, error) {
		return QuestionResponse{Answers: []string{"No"}}, nil
	}
	res, err := NewExitPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if switched != "" {
		t.Errorf("SwitchAgent called with %q, want no switch", switched)
	}
	if res.Title != "staying in plan mode" {
		t.Errorf("title = %q", res.Title)
	}
	if !strings.Contains(res.Output, "declined") && !strings.Contains(res.Output, "Remaining") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestEnterPlanModeNilSwitchAgent(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewEnterPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "SwitchAgent") {
		t.Errorf("err = %v", err)
	}
}

func TestExitPlanModeNilSwitchAgent(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewExitPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "SwitchAgent") {
		t.Errorf("err = %v", err)
	}
}

func TestEnterPlanModePermissionDenied(t *testing.T) {
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, AskRequest) error { return errors.New("denied") },
		SwitchAgent: func(string) error {
			t.Fatal("SwitchAgent must not run")
			return nil
		},
	}
	_, err := NewEnterPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil {
		t.Fatal("expected deny")
	}
}
