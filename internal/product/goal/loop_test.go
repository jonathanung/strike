package goal

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	t time.Time
}

func (f *fakeClock) Now() time.Time { return f.t }

func TestLoopCompletesWithEvaluateOnly(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "loop")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: true")
	g, err := s.Create("already good", []Criterion{{Description: "ok", Check: spec}}, Constraints{
		MaxIterations: 5, MaxCostUSD: 1, MaxWallClockS: 60, MaxNoProgressIters: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	loop := &Loop{
		Store:   s,
		Planner: EvaluateOnlyPlanner{},
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
	if got.Status != StatusDone {
		t.Fatalf("status=%s reason=%s", got.Status, got.FailReason)
	}
	if got.LastIteration < 1 {
		t.Fatal("expected at least one iteration")
	}
	evs, err := s.ListEvents(g.ID)
	if err != nil || len(evs) == 0 {
		t.Fatalf("events empty: %v", err)
	}
}

func TestLoopNoProgressTrips(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "np")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: false")
	g, err := s.Create("never", []Criterion{{Description: "x", Check: spec}}, Constraints{
		MaxIterations: 10, MaxCostUSD: 10, MaxWallClockS: 600, MaxNoProgressIters: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	loop := &Loop{
		Store:   s,
		Planner: EvaluateOnlyPlanner{},
		Critic: &DefaultCritic{
			CmdRunner: func(context.Context, string, string) (int, string, error) {
				return 1, "still failing", nil
			},
		},
	}
	got, err := loop.Run(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusFailed {
		t.Fatalf("status=%s reason=%s", got.Status, got.FailReason)
	}
	if got.LastIteration < 3 {
		t.Fatalf("iters=%d", got.LastIteration)
	}
}

func TestLoopActorCannotMarkDone(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "lie")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: false")
	g, err := s.Create("lie", []Criterion{{Description: "x", Check: spec}}, Constraints{
		MaxIterations: 3, MaxCostUSD: 10, MaxWallClockS: 600, MaxNoProgressIters: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Planner that "claims" success in summary — critic still fails.
	loop := &Loop{
		Store: s,
		Planner: FuncPlanner(func(context.Context, Goal, Observation) (Plan, error) {
			return Plan{Summary: "all done!"}, nil
		}),
		Critic: &DefaultCritic{
			CmdRunner: func(context.Context, string, string) (int, string, error) {
				return 1, "no", nil
			},
		},
	}
	got, err := loop.Run(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == StatusDone {
		t.Fatal("lying planner must not complete goal")
	}
}

func TestLoopPreActBlocks(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "block")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var execs atomic.Int32
	spec, _ := ParseCheckSpec("predicate: done")
	// Goal completes when predicate says so after blocked acts don't matter.
	var n atomic.Int32
	g, err := s.Create("block tools", []Criterion{{Description: "d", Check: spec}}, Constraints{
		MaxIterations: 5, MaxCostUSD: 10, MaxWallClockS: 600, MaxNoProgressIters: 5,
		AllowedTools: []string{"bash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	loop := &Loop{
		Store: s,
		Planner: FuncPlanner(func(context.Context, Goal, Observation) (Plan, error) {
			return Plan{
				Summary: "try write",
				Actions: []Action{{Tool: "write", Args: map[string]string{"path": "x"}}},
			}, nil
		}),
		Executor: FuncExecutor(func(context.Context, Goal, Action) (string, float64, error) {
			execs.Add(1)
			return "ran", 0, nil
		}),
		Critic: &DefaultCritic{
			Predicates: map[string]PredicateFunc{
				"done": func(context.Context, Goal, string) (bool, string, error) {
					// complete after first iteration observed
					if n.Add(1) >= 1 {
						return true, "ok", nil
					}
					return false, "", nil
				},
			},
		},
		Hooks: DefaultHooks(),
	}
	got, err := loop.Run(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if execs.Load() != 0 {
		t.Fatalf("executor should not run blocked tool, ran %d", execs.Load())
	}
	if got.Status != StatusDone {
		t.Fatalf("status=%s", got.Status)
	}
	iters, _ := s.ListIterations(g.ID)
	if len(iters) < 1 || len(iters[0].Actions) < 1 || !iters[0].Actions[0].Blocked {
		t.Fatalf("expected blocked action: %+v", iters)
	}
}

func TestLoopEmptyAllowlistBlocksAllTools(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "empty")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var execs atomic.Int32
	spec, _ := ParseCheckSpec("cmd: true")
	g, err := s.Create("eval only", []Criterion{{Check: spec}}, Constraints{
		MaxIterations: 2, MaxCostUSD: 1, MaxWallClockS: 60, MaxNoProgressIters: 3,
		// empty allowlist
	})
	if err != nil {
		t.Fatal(err)
	}
	loop := &Loop{
		Store: s,
		Planner: FuncPlanner(func(context.Context, Goal, Observation) (Plan, error) {
			return Plan{Actions: []Action{{Tool: "bash", Args: map[string]string{"command": "echo hi"}}}}, nil
		}),
		Executor: FuncExecutor(func(context.Context, Goal, Action) (string, float64, error) {
			execs.Add(1)
			return "", 0, nil
		}),
		Critic: &DefaultCritic{
			CmdRunner: func(context.Context, string, string) (int, string, error) { return 0, "", nil },
		},
		Hooks: DefaultHooks(),
	}
	got, err := loop.Run(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if execs.Load() != 0 {
		t.Fatal("empty allowlist must not execute tools")
	}
	if got.Status != StatusDone {
		t.Fatalf("status=%s", got.Status)
	}
}

func TestLoopIdempotentResumeSkipsCompletedIntent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s, err := Open(root, "idemp")
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := ParseCheckSpec("predicate: once")
	g, err := s.Create("idemp", []Criterion{{Description: "p", Check: spec}}, Constraints{
		MaxIterations: 5, MaxCostUSD: 10, MaxWallClockS: 600, MaxNoProgressIters: 5,
		AllowedTools: []string{"bash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int32
	// Pre-mark intent as if crash after execute before commit of full iteration...
	// Simpler: run once partially by marking intent then run loop with planner that uses same action index.
	key := IntentKey(g.ID, 1, 0)
	if err := s.MarkIntent(key); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s2, err := Open(root, "idemp")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	loop := &Loop{
		Store: s2,
		Planner: FuncPlanner(func(context.Context, Goal, Observation) (Plan, error) {
			return Plan{Actions: []Action{{Tool: "bash", Args: map[string]string{"command": "echo"}}}}, nil
		}),
		Executor: FuncExecutor(func(context.Context, Goal, Action) (string, float64, error) {
			runs.Add(1)
			return "x", 0, nil
		}),
		Critic: &DefaultCritic{
			Predicates: map[string]PredicateFunc{
				"once": func(context.Context, Goal, string) (bool, string, error) {
					return true, "", nil
				},
			},
		},
		Hooks: DefaultHooks(),
	}
	got, err := loop.Run(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 0 {
		t.Fatalf("executor ran %d times; want 0 (intent skip)", runs.Load())
	}
	if got.Status != StatusDone {
		t.Fatalf("status=%s", got.Status)
	}
}

func TestLoopAbortMidway(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "abort")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: false")
	g, err := s.Create("abort me", []Criterion{{Check: spec}}, Constraints{
		MaxIterations: 20, MaxCostUSD: 10, MaxWallClockS: 600, MaxNoProgressIters: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	loop := &Loop{
		Store: s,
		Planner: FuncPlanner(func(context.Context, Goal, Observation) (Plan, error) {
			if n.Add(1) == 2 {
				_ = s.RequestAbort(g.ID)
			}
			return Plan{Summary: "x"}, nil
		}),
		Critic: &DefaultCritic{
			CmdRunner: func(context.Context, string, string) (int, string, error) {
				return 1, "", nil
			},
		},
	}
	got, err := loop.Run(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusAborted {
		t.Fatalf("status=%s reason=%s", got.Status, got.FailReason)
	}
}

func TestLoopPause(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "pause")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: false")
	g, err := s.Create("pause me", []Criterion{{Check: spec}}, Constraints{
		MaxIterations: 20, MaxCostUSD: 10, MaxWallClockS: 600, MaxNoProgressIters: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	loop := &Loop{
		Store: s,
		Planner: FuncPlanner(func(context.Context, Goal, Observation) (Plan, error) {
			if n.Add(1) == 1 {
				cur, _, _ := s.Get(g.ID)
				if cur.Status == StatusActive {
					_, _ = s.SetStatus(g.ID, StatusPaused, "")
				}
			}
			return Plan{}, nil
		}),
		Critic: &DefaultCritic{
			CmdRunner: func(context.Context, string, string) (int, string, error) {
				return 1, "", nil
			},
		},
	}
	got, err := loop.Run(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPaused {
		t.Fatalf("want paused after mid-loop pause, got %s (iters=%d reason=%s)", got.Status, got.LastIteration, got.FailReason)
	}
}

func TestLoopRaceRun(t *testing.T) {
	s, err := Open(t.TempDir(), "race-loop")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: true")
	g, err := s.Create("r", []Criterion{{Check: spec}}, DefaultConstraints())
	if err != nil {
		t.Fatal(err)
	}
	loop := &Loop{
		Store:   s,
		Planner: EvaluateOnlyPlanner{},
		Critic: &DefaultCritic{
			CmdRunner: func(context.Context, string, string) (int, string, error) {
				return 0, "", nil
			},
		},
	}
	// Single run is enough under -race with concurrent list.
	done := make(chan error, 1)
	go func() {
		_, err := loop.Run(context.Background(), g.ID)
		done <- err
	}()
	for i := 0; i < 20; i++ {
		_, _ = s.List()
		_, _, _ = s.Get(g.ID)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
