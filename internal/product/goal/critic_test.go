package goal

import (
	"context"
	"testing"
)

func TestCriticCmd(t *testing.T) {
	t.Parallel()
	c := &DefaultCritic{
		CmdRunner: func(ctx context.Context, workDir, command string) (int, string, error) {
			if command == "true" {
				return 0, "ok", nil
			}
			return 1, "fail", nil
		},
	}
	g := Goal{
		Description: "t",
		Criteria: []Criterion{
			{Description: "pass", Check: CheckSpec{Kind: CheckCmd, Value: "true"}},
			{Description: "fail", Check: CheckSpec{Kind: CheckCmd, Value: "false"}},
		},
	}
	ev, err := c.Evaluate(context.Background(), g, IterationRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if ev.AllSatisfied {
		t.Fatal("should not all be satisfied")
	}
	if !ev.Criteria[0].Satisfied || ev.Criteria[1].Satisfied {
		t.Fatalf("criteria=%+v", ev.Criteria)
	}
}

func TestCriticPredicate(t *testing.T) {
	t.Parallel()
	c := &DefaultCritic{
		Predicates: map[string]PredicateFunc{
			"always_true": func(context.Context, Goal, string) (bool, string, error) {
				return true, "yes", nil
			},
		},
	}
	g := Goal{
		Criteria: []Criterion{{
			Description: "p",
			Check:       CheckSpec{Kind: CheckPredicate, Value: "always_true"},
		}},
	}
	ev, err := c.Evaluate(context.Background(), g, IterationRecord{})
	if err != nil || !ev.AllSatisfied {
		t.Fatalf("ev=%+v err=%v", ev, err)
	}
}

func TestCriticJudgeFailClosed(t *testing.T) {
	t.Parallel()
	c := &DefaultCritic{}
	g := Goal{
		Criteria: []Criterion{{
			Description: "fuzzy",
			Check:       CheckSpec{Kind: CheckJudge, Value: "nice?"},
		}},
	}
	ev, err := c.Evaluate(context.Background(), g, IterationRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if ev.AllSatisfied || ev.Criteria[0].Error == "" {
		t.Fatalf("judge without impl should fail closed: %+v", ev)
	}
}

func TestCriticIgnoresPreSatisfiedFlags(t *testing.T) {
	t.Parallel()
	// Actor cannot mark done: even if Satisfied=true on input, cmd decides.
	c := &DefaultCritic{
		CmdRunner: func(context.Context, string, string) (int, string, error) {
			return 1, "nope", nil
		},
	}
	g := Goal{
		Criteria: []Criterion{{
			Description: "x",
			Satisfied:   true, // lie
			Check:       CheckSpec{Kind: CheckCmd, Value: "false"},
		}},
	}
	ev, err := c.Evaluate(context.Background(), g, IterationRecord{})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Criteria[0].Satisfied {
		t.Fatal("critic must not trust pre-set Satisfied")
	}
}

func TestApplyEval(t *testing.T) {
	t.Parallel()
	g := Goal{Criteria: []Criterion{{Description: "a"}, {Description: "b"}}}
	ApplyEval(&g, EvalRecord{Criteria: []CriterionResult{
		{Description: "a", Satisfied: true},
		{Description: "b", Satisfied: false},
	}})
	if !g.Criteria[0].Satisfied || g.Criteria[1].Satisfied {
		t.Fatalf("%+v", g.Criteria)
	}
}
