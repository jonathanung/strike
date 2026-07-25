package tool

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestEnterPlanModeCallsEnterPlanPhase(t *testing.T) {
	var entered bool
	tc := allowAll(t.TempDir())
	tc.EnterPlanPhase = func() error {
		entered = true
		return nil
	}
	tc.SwitchAgent = func(name string) error {
		t.Fatalf("SwitchAgent should not run when EnterPlanPhase is set; got %q", name)
		return nil
	}
	res, err := NewEnterPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !entered {
		t.Error("EnterPlanPhase not called")
	}
	if res.Title != "plan mode" {
		t.Errorf("title = %q", res.Title)
	}
	if !strings.Contains(res.Output, "plan mode") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestEnterPlanModeFallsBackToSwitchAgent(t *testing.T) {
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
}

func TestExitPlanModeAdvancePhase(t *testing.T) {
	var advanced bool
	tc := allowAll(t.TempDir())
	tc.AdvancePhase = func(context.Context) error {
		advanced = true
		return nil
	}
	res, err := NewExitPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !advanced {
		t.Error("AdvancePhase not called")
	}
	if res.Title != "build mode" {
		t.Errorf("title = %q", res.Title)
	}
}

func TestExitPlanModeAdvanceDeclined(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.AdvancePhase = func(context.Context) error {
		return errors.New("user declined leaving phase \"plan\"")
	}
	res, err := NewExitPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Title != "staying in plan mode" {
		t.Errorf("title = %q", res.Title)
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
	if !strings.Contains(err.Error(), "not configured") {
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
		EnterPlanPhase: func() error {
			t.Fatal("EnterPlanPhase must not run")
			return nil
		},
	}
	_, err := NewEnterPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil {
		t.Fatal("expected deny")
	}
}

func TestPhaseDoneAdvances(t *testing.T) {
	var advanced bool
	tc := allowAll(t.TempDir())
	tc.AdvancePhase = func(context.Context) error {
		advanced = true
		return nil
	}
	res, err := NewPhaseDone().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !advanced {
		t.Error("AdvancePhase not called")
	}
	if res.Title != "phase advanced" {
		t.Errorf("title = %q", res.Title)
	}
}

func TestPhaseDoneNilAdvance(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewPhaseDone().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil || !strings.Contains(err.Error(), "AdvancePhase") {
		t.Fatalf("err = %v", err)
	}
}
