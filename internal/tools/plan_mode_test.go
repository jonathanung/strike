package tools

import (
	"context"
	"github.com/jonathanung/strike-cli/internal/tool"
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
		t.Fatalf("title = %q", res.Title)
	}
	if !strings.Contains(res.Output, "plan mode") {
		t.Fatalf("output = %q", res.Output)
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
		t.Fatalf("title = %q", res.Title)
	}
}

func TestExitPlanModeHandoff(t *testing.T) {
	var got tool.PlanHandoffRequest
	tc := allowAll(t.TempDir())
	tc.HandoffPlan = func(ctx context.Context, req tool.PlanHandoffRequest) (tool.PlanHandoffResult, error) {
		got = req
		return tool.PlanHandoffResult{
			Agent:          "build",
			PlanID:         req.PlanID,
			PlanVersion:    req.ExpectedVersion + 1,
			ApprovalSource: "user",
			ViaPhase:       true,
		}, nil
	}
	res, err := NewExitPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{
		"plan_id":          "abcd1234",
		"expected_version": 2,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.PlanID != "abcd1234" || got.ExpectedVersion != 2 {
		t.Fatalf("handoff req = %+v", got)
	}
	if got.Agent != "build" {
		t.Fatalf("target agent = %q", got.Agent)
	}
	if !strings.Contains(res.Output, "unified handoff") {
		t.Fatalf("output = %q", res.Output)
	}
	if !strings.Contains(res.Output, "source=user") {
		t.Fatalf("output missing source: %q", res.Output)
	}
}

func TestExitPlanModeHandoffDeclined(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.HandoffPlan = func(ctx context.Context, req tool.PlanHandoffRequest) (tool.PlanHandoffResult, error) {
		return tool.PlanHandoffResult{}, &tool.UserRejectedError{
			Message: "User declined exiting plan mode. Remaining in plan mode.",
		}
	}
	_, err := NewExitPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{
		"legacy_text": "do the thing",
	}), tc)
	if err == nil {
		t.Fatal("want decline error")
	}
	if _, ok := err.(*tool.UserRejectedError); !ok {
		t.Fatalf("err type = %T (%v)", err, err)
	}
}

func TestExitPlanModeExplicitOrchestrator(t *testing.T) {
	var got tool.PlanHandoffRequest
	tc := allowAll(t.TempDir())
	tc.HandoffPlan = func(ctx context.Context, req tool.PlanHandoffRequest) (tool.PlanHandoffResult, error) {
		got = req
		return tool.PlanHandoffResult{Agent: req.Agent, ViaPhase: true, ApprovalSource: "agent"}, nil
	}
	_, err := NewExitPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{
		"agent":       "orchestrator",
		"legacy_text": "complex plan",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "orchestrator" {
		t.Fatalf("agent = %q", got.Agent)
	}
}

func TestExitPlanModeHeuristicSteps(t *testing.T) {
	var got tool.PlanHandoffRequest
	tc := allowAll(t.TempDir())
	tc.HandoffPlan = func(ctx context.Context, req tool.PlanHandoffRequest) (tool.PlanHandoffResult, error) {
		got = req
		return tool.PlanHandoffResult{Agent: req.Agent, ApprovalSource: "agent"}, nil
	}
	_, err := NewExitPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{
		"steps":       5,
		"legacy_text": "many steps",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "orchestrator" {
		t.Fatalf("agent = %q, want orchestrator", got.Agent)
	}
}

func TestExitPlanModeNilHandoff(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewExitPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil || !strings.Contains(err.Error(), "handoff") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnterPlanModeNilSwitchAgent(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewEnterPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil {
		t.Fatal("want error")
	}
}

func TestEnterPlanModePermissionDenied(t *testing.T) {
	tc := &tool.Context{
		Ask: func(ctx context.Context, req tool.AskRequest) error {
			return &tool.CodedError{Code: tool.CodePermissionDenied, Message: "no"}
		},
		EnterPlanPhase: func() error {
			t.Fatal("EnterPlanPhase must not run")
			return nil
		},
	}
	_, err := NewEnterPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil {
		t.Fatal("want deny")
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
		t.Fatalf("title = %q", res.Title)
	}
}

func TestPhaseDoneNilAdvance(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewPhaseDone().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil || !strings.Contains(err.Error(), "AdvancePhase") {
		t.Fatalf("err = %v", err)
	}
}
