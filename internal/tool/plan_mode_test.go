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
		if !strings.Contains(req.Questions[0].Question, "build") {
			t.Errorf("question = %q, want build", req.Questions[0].Question)
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

func TestPickPostPlanAgent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		agent      string
		steps      int
		areas      int
		multiAgent bool
		want       string
	}{
		{name: "default simple", want: "build"},
		{name: "explicit build", agent: "build", steps: 99, want: "build"},
		{name: "explicit orchestrator", agent: "orchestrator", want: "orchestrator"},
		{name: "explicit case", agent: "Build", want: "build"},
		{name: "unknown agent falls to heuristic", agent: "reviewer", want: "build"},
		{name: "steps threshold", steps: 4, want: "orchestrator"},
		{name: "steps below", steps: 3, want: "build"},
		{name: "areas threshold", areas: 3, want: "orchestrator"},
		{name: "areas below", areas: 2, want: "build"},
		{name: "multi_agent", multiAgent: true, want: "orchestrator"},
		{name: "explicit beats multi", agent: "build", multiAgent: true, want: "build"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PickPostPlanAgent(tc.agent, tc.steps, tc.areas, tc.multiAgent)
			if got != tc.want {
				t.Fatalf("PickPostPlanAgent = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExitPlanModeExplicitOrchestrator(t *testing.T) {
	var switched string
	tc := allowAll(t.TempDir())
	tc.SwitchAgent = func(name string) error {
		switched = name
		return nil
	}
	tc.AskUser = func(_ context.Context, req QuestionRequest) (QuestionResponse, error) {
		if !strings.Contains(req.Questions[0].Question, "orchestrator") {
			t.Errorf("question = %q", req.Questions[0].Question)
		}
		return QuestionResponse{Answers: []string{"Yes"}}, nil
	}
	res, err := NewExitPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{
		"agent": "orchestrator",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if switched != "orchestrator" {
		t.Errorf("SwitchAgent = %q, want orchestrator", switched)
	}
	if res.Title != "orchestrator mode" {
		t.Errorf("title = %q", res.Title)
	}
	if !strings.Contains(res.Output, "orchestrator") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestExitPlanModeHeuristicSteps(t *testing.T) {
	var switched string
	tc := allowAll(t.TempDir())
	tc.SwitchAgent = func(name string) error {
		switched = name
		return nil
	}
	// No AskUser: headless approve path.
	res, err := NewExitPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{
		"steps": 5,
		"areas": 1,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if switched != "orchestrator" {
		t.Errorf("SwitchAgent = %q, want orchestrator", switched)
	}
	if res.Title != "orchestrator mode" {
		t.Errorf("title = %q", res.Title)
	}
}

func TestExitPlanModeAdvanceThenSwitchOrchestrator(t *testing.T) {
	var advanced bool
	var switched string
	tc := allowAll(t.TempDir())
	tc.AdvancePhase = func(context.Context) error {
		advanced = true
		return nil
	}
	tc.SwitchAgent = func(name string) error {
		switched = name
		return nil
	}
	res, err := NewExitPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{
		"multi_agent": true,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !advanced {
		t.Error("AdvancePhase not called")
	}
	if switched != "orchestrator" {
		t.Errorf("SwitchAgent = %q, want orchestrator", switched)
	}
	if res.Title != "orchestrator mode" {
		t.Errorf("title = %q", res.Title)
	}
	if !strings.Contains(res.Output, "implement phase") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestExitPlanModeOrchestratorFallbackBuild(t *testing.T) {
	var switched []string
	tc := allowAll(t.TempDir())
	tc.SwitchAgent = func(name string) error {
		switched = append(switched, name)
		if name == "orchestrator" {
			return errors.New("unknown agent \"orchestrator\"")
		}
		return nil
	}
	res, err := NewExitPlanMode().Execute(context.Background(), mustJSON(t, map[string]any{
		"agent": "orchestrator",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if len(switched) != 2 || switched[0] != "orchestrator" || switched[1] != "build" {
		t.Fatalf("switches = %v, want [orchestrator build]", switched)
	}
	if res.Title != "build mode" {
		t.Errorf("title = %q after fallback", res.Title)
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
