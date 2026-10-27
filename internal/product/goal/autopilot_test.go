package goal

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDenyBeatsAllowlistAndAutopilot(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "deny")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: true")
	g, err := s.Create("deny bash", []Criterion{{Description: "ok", Check: spec}}, Constraints{
		MaxIterations: 3, MaxCostUSD: 1, MaxWallClockS: 60, MaxNoProgressIters: 5,
		AllowedTools: []string{"bash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Hand-edited record: deny the same tool the allowlist names.
	// Create rejects that overlap; the hook must still block it.
	g.Autopilot = true
	g.Constraints.DenyTools = []string{"bash"}
	if err := s.UpdateGoal(g); err != nil {
		t.Fatal(err)
	}
	var execs atomic.Int32
	loop := &Loop{
		Store: s,
		Planner: FuncPlanner(func(context.Context, Goal, Observation) (Plan, error) {
			return Plan{Actions: []Action{{Tool: "bash", Args: map[string]string{"command": "true"}}}}, nil
		}),
		Executor: FuncExecutor(func(context.Context, Goal, Action) (string, float64, error) {
			execs.Add(1)
			return "ran", 0, nil
		}),
		Critic: &DefaultCritic{
			CmdRunner: func(context.Context, string, string) (int, string, error) {
				return 0, "", nil
			},
		},
		Hooks: DefaultHooks(),
	}
	got, err := loop.Run(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if execs.Load() != 0 {
		t.Fatalf("deny rule executed bash %d times", execs.Load())
	}
	if got.Status != StatusDone {
		t.Fatalf("status=%s reason=%s", got.Status, got.FailReason)
	}
	iters, err := s.ListIterations(g.ID)
	if err != nil || len(iters) < 1 || len(iters[0].Actions) < 1 || !iters[0].Actions[0].Blocked {
		t.Fatalf("expected a blocked action: %+v err=%v", iters, err)
	}
	if !strings.Contains(iters[0].Actions[0].BlockReason, "deny rule") {
		t.Fatalf("reason=%q", iters[0].Actions[0].BlockReason)
	}
}

func TestAutopilotOffDoesNotRunTools(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "off")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: true")
	g, err := s.Create("off", []Criterion{{Check: spec}}, Constraints{
		MaxIterations: 2, MaxCostUSD: 1, MaxWallClockS: 60, MaxNoProgressIters: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	var execs atomic.Int32
	loop := &Loop{
		Store: s,
		Planner: FuncPlanner(func(context.Context, Goal, Observation) (Plan, error) {
			return Plan{Actions: []Action{{Tool: "bash", Args: map[string]string{"command": "true"}}}}, nil
		}),
		Executor: FuncExecutor(func(context.Context, Goal, Action) (string, float64, error) {
			execs.Add(1)
			return "", 0, nil
		}),
		Critic: &DefaultCritic{CmdRunner: func(context.Context, string, string) (int, string, error) {
			return 0, "", nil
		}},
	}
	if _, err := loop.Run(context.Background(), g.ID); err != nil {
		t.Fatal(err)
	}
	if execs.Load() != 0 {
		t.Fatal("autopilot off with an empty allowlist ran a tool")
	}
}

func TestCreateRejectsDenyAndAllowOverlap(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "overlap")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: true")
	_, err = s.Create("overlap", []Criterion{{Check: spec}}, Constraints{
		MaxIterations: 2, MaxCostUSD: 1, MaxWallClockS: 60, MaxNoProgressIters: 3,
		AllowedTools: []string{"bash"}, DenyTools: []string{"Bash"},
	})
	if err == nil || !strings.Contains(err.Error(), "deny wins") {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateAutopilotRequiresAllowlist(t *testing.T) {
	t.Parallel()
	err := ValidateAutopilot(true, DefaultConstraints())
	if err == nil {
		t.Fatal("expected error")
	}
	err = ValidateAutopilot(true, Constraints{AllowedTools: []string{"bash"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateAutopilot(false, DefaultConstraints()); err != nil {
		t.Fatal(err)
	}
}

func TestLoopTokenBudgetStops(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "tokens")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: false")
	g, err := s.Create("tokens", []Criterion{{Description: "x", Check: spec}}, Constraints{
		MaxIterations: 20, MaxCostUSD: 10, MaxWallClockS: 600, MaxNoProgressIters: 50,
		MaxVerifyFailures: 50, MaxTokens: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	loop := &Loop{
		Store:   s,
		Planner: EvaluateOnlyPlanner{},
		Critic: &DefaultCritic{CmdRunner: func(context.Context, string, string) (int, string, error) {
			return 1, "no", nil
		}},
	}
	got, err := loop.Run(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusFailed || !strings.Contains(got.FailReason, "token budget") {
		t.Fatalf("status=%s reason=%s tokens=%d", got.Status, got.FailReason, got.TokensUsed)
	}
	if got.LastIteration < 1 || got.LastIteration >= 20 {
		t.Fatalf("iters=%d", got.LastIteration)
	}
}

func TestLoopVerifyFailureStopsWhenEvidenceChanges(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "verify")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: false")
	g, err := s.Create("verify", []Criterion{{Description: "x", Check: spec}}, Constraints{
		MaxIterations: 20, MaxCostUSD: 10, MaxWallClockS: 600, MaxNoProgressIters: 50,
		MaxVerifyFailures: 3, MaxTokens: 200_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	loop := &Loop{
		Store:   s,
		Planner: EvaluateOnlyPlanner{},
		Critic: &DefaultCritic{CmdRunner: func(context.Context, string, string) (int, string, error) {
			i := n.Add(1)
			return 1, strings.Repeat("x", int(i)*8), nil
		}},
	}
	got, err := loop.Run(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusBlocked || got.Escalation != "human" {
		t.Fatalf("status=%s escalation=%s reason=%s", got.Status, got.Escalation, got.FailReason)
	}
	if !strings.Contains(got.FailReason, "verification failed") {
		t.Fatalf("reason=%s", got.FailReason)
	}
	if got.LastIteration != 3 {
		t.Fatalf("iters=%d", got.LastIteration)
	}
}

func TestResumeFromBlockedSetsGuardFloor(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "floor")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: false")
	g, err := s.Create("floor", []Criterion{{Description: "x", Check: spec}}, Constraints{
		MaxIterations: 20, MaxCostUSD: 10, MaxWallClockS: 600, MaxNoProgressIters: 50,
		MaxVerifyFailures: 2, MaxTokens: 200_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	loop := &Loop{
		Store:   s,
		Planner: EvaluateOnlyPlanner{},
		Critic: &DefaultCritic{CmdRunner: func(context.Context, string, string) (int, string, error) {
			return 1, "no", nil
		}},
	}
	got, err := loop.Run(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusBlocked || got.LastIteration != 2 {
		t.Fatalf("first stop %+v", got)
	}
	resumed, err := s.SetStatus(g.ID, StatusActive, "")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.GuardFloor != 2 || resumed.Escalation != "" || resumed.Status != StatusActive {
		t.Fatalf("resumed=%+v", resumed)
	}
}
