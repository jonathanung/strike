package local

import (
	"context"
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/internal/goal"
	"github.com/jonathanung/strike-cli/internal/host"
)

// NewGoals adapts *goal.Store to host.Goals with a safe evaluate-only loop
// default (empty tool allowlist blocks all tool side effects).
func NewGoals(store *goal.Store, workDir string) host.Goals {
	if store == nil {
		return nil
	}
	return goalsAdapter{store: store, workDir: workDir}
}

type goalsAdapter struct {
	store   *goal.Store
	workDir string
}

func (a goalsAdapter) Set(description string, criteria []string, opts host.GoalSetOptions) (host.Goal, error) {
	crit := make([]goal.Criterion, 0, len(criteria))
	for _, raw := range criteria {
		spec, err := goal.ParseCheckSpec(raw)
		if err != nil {
			return host.Goal{}, err
		}
		crit = append(crit, goal.Criterion{
			Description: goal.FormatCheckSpec(spec),
			Check:       spec,
		})
	}
	c := goal.DefaultConstraints()
	if opts.MaxIterations > 0 {
		c.MaxIterations = opts.MaxIterations
	}
	if opts.MaxCostUSD > 0 {
		c.MaxCostUSD = opts.MaxCostUSD
	}
	if opts.MaxWallClockS > 0 {
		c.MaxWallClockS = opts.MaxWallClockS
	}
	if opts.MaxNoProgressIters > 0 {
		c.MaxNoProgressIters = opts.MaxNoProgressIters
	}
	if opts.AllowedTools != nil {
		c.AllowedTools = append([]string(nil), opts.AllowedTools...)
	}
	g, err := a.store.Create(description, crit, c)
	if err != nil {
		return host.Goal{}, err
	}
	return toHostGoal(g), nil
}

func (a goalsAdapter) List() ([]host.Goal, error) {
	list, err := a.store.List()
	if err != nil {
		return nil, err
	}
	out := make([]host.Goal, len(list))
	for i, g := range list {
		out[i] = toHostGoal(g)
	}
	return out, nil
}

func (a goalsAdapter) Get(id string) (host.Goal, bool, error) {
	g, ok, err := a.store.Get(id)
	if err != nil || !ok {
		return host.Goal{}, ok, err
	}
	return toHostGoal(g), true, nil
}

func (a goalsAdapter) Run(ctx context.Context, id string) (host.Goal, error) {
	loop := &goal.Loop{
		Store:   a.store,
		Planner: goal.EvaluateOnlyPlanner{},
		Critic: &goal.DefaultCritic{
			WorkDir: a.workDir,
		},
		Executor: goal.ShellExecutor{WorkDir: a.workDir},
		Hooks:    goal.DefaultHooks(),
	}
	g, err := loop.Run(ctx, id)
	if err != nil {
		return host.Goal{}, err
	}
	return toHostGoal(g), nil
}

func (a goalsAdapter) Pause(id string) (host.Goal, error) {
	g, err := a.store.SetStatus(id, goal.StatusPaused, "")
	if err != nil {
		return host.Goal{}, err
	}
	return toHostGoal(g), nil
}

func (a goalsAdapter) Resume(id string) (host.Goal, error) {
	g, err := a.store.SetStatus(id, goal.StatusActive, "")
	if err != nil {
		return host.Goal{}, err
	}
	return toHostGoal(g), nil
}

func (a goalsAdapter) Abort(id string) (host.Goal, error) {
	g, err := a.store.SetStatus(id, goal.StatusAborted, "aborted by user")
	if err != nil {
		return host.Goal{}, err
	}
	return toHostGoal(g), nil
}

func (a goalsAdapter) Log(id string, iter int) ([]host.GoalIteration, error) {
	recs, err := a.store.ListIterations(id)
	if err != nil {
		return nil, err
	}
	out := make([]host.GoalIteration, 0, len(recs))
	for _, r := range recs {
		if iter > 0 && r.N != iter {
			continue
		}
		out = append(out, host.GoalIteration{
			N:         r.N,
			Plan:      r.Plan,
			StateHash: r.StateHash,
			CostUSD:   r.CostUSD,
			Summary:   formatIterSummary(r),
		})
	}
	return out, nil
}

func formatIterSummary(r goal.IterationRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "iter %d plan=%q hash=%s cost=%.4f actions=%d",
		r.N, r.Plan, shortHash(r.StateHash), r.CostUSD, len(r.Actions))
	if len(r.Evaluation.Criteria) > 0 {
		b.WriteString(" [")
		for i, c := range r.Evaluation.Criteria {
			if i > 0 {
				b.WriteByte(' ')
			}
			mark := "FAIL"
			if c.Satisfied {
				mark = "OK"
			}
			fmt.Fprintf(&b, "%s:%s", mark, c.Description)
		}
		b.WriteByte(']')
	}
	return b.String()
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

func toHostGoal(g goal.Goal) host.Goal {
	out := host.Goal{
		ID:            g.ID,
		Description:   g.Description,
		Status:        string(g.Status),
		MaxIterations: g.Constraints.MaxIterations,
		MaxCostUSD:    g.Constraints.MaxCostUSD,
		AllowedTools:  append([]string(nil), g.Constraints.AllowedTools...),
		CostUSD:       g.CostUSD,
		LastIteration: g.LastIteration,
		FailReason:    g.FailReason,
		CreatedAt:     g.CreatedAt,
	}
	out.Criteria = make([]host.GoalCriterion, len(g.Criteria))
	for i, c := range g.Criteria {
		out.Criteria[i] = host.GoalCriterion{
			Description: c.Description,
			Check:       goal.FormatCheckSpec(c.Check),
			Satisfied:   c.Satisfied,
		}
	}
	return out
}
