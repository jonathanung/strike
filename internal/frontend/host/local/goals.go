package local

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/product/goal"
)

// NewGoals adapts *goal.Store to host.Goals with a safe evaluate-only loop
// default (empty tool allowlist blocks all tool side effects).
func NewGoals(store *goal.Store, workDir string) host.Goals {
	if store == nil {
		return nil
	}
	return &goalsAdapter{
		store:   store,
		workDir: workDir,
		running: map[string]struct{}{},
		cancel:  map[string]context.CancelFunc{},
	}
}

type goalsAdapter struct {
	store   *goal.Store
	workDir string
	mu      sync.Mutex
	running map[string]struct{}
	cancel  map[string]context.CancelFunc
	// runStarting, when set, runs right after a run registers itself and
	// before it transitions the goal or starts the loop. It is a test seam
	// for the pause-during-start window; nil in production.
	runStarting func(id string)
}

func (a *goalsAdapter) Set(description string, criteria []string, opts host.GoalSetOptions) (host.Goal, error) {
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
	if opts.MaxTokens > 0 {
		c.MaxTokens = opts.MaxTokens
	}
	if opts.MaxVerifyFailures > 0 {
		c.MaxVerifyFailures = opts.MaxVerifyFailures
	}
	if opts.AllowedTools != nil {
		c.AllowedTools = append([]string(nil), opts.AllowedTools...)
	}
	if opts.DenyTools != nil {
		c.DenyTools = append([]string(nil), opts.DenyTools...)
	}
	if err := goal.ValidateAutopilot(opts.Autopilot, c); err != nil {
		return host.Goal{}, err
	}
	g, err := a.store.Create(description, crit, c)
	if err != nil {
		return host.Goal{}, err
	}
	if opts.Autopilot {
		g.Autopilot = true
		if err := a.store.UpdateGoal(g); err != nil {
			return host.Goal{}, err
		}
	}
	return toHostGoal(g), nil
}

func (a *goalsAdapter) List() ([]host.Goal, error) {
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

func (a *goalsAdapter) Get(id string) (host.Goal, bool, error) {
	g, ok, err := a.store.Get(id)
	if err != nil || !ok {
		return host.Goal{}, ok, err
	}
	return toHostGoal(g), true, nil
}

func (a *goalsAdapter) Run(ctx context.Context, id string) (host.Goal, error) {
	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	if _, ok := a.running[id]; ok {
		a.mu.Unlock()
		cancel()
		g, ok, err := a.Get(id)
		if err != nil || !ok {
			if err == nil {
				err = fmt.Errorf("goal: not found")
			}
			return host.Goal{}, err
		}
		return g, nil
	}
	// Register the run (and its cancel func) before any status transition,
	// so a Pause or Abort landing during start finds the cancel func and
	// is honoured below instead of being overwritten by the transition.
	a.running[id] = struct{}{}
	a.cancel[id] = cancel
	starting := a.runStarting
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		delete(a.running, id)
		delete(a.cancel, id)
		a.mu.Unlock()
	}()

	if starting != nil {
		starting(id)
	}

	// The run owns the status transition as part of starting (the loop
	// would do the same, but only after this window). Terminal goals do
	// not start; a goal a concurrent control already moved is left as
	// that control left it.
	g, ok, err := a.store.Get(id)
	if err != nil {
		return host.Goal{}, err
	}
	if !ok {
		return host.Goal{}, fmt.Errorf("goal: not found")
	}
	switch g.Status {
	case goal.StatusDone, goal.StatusFailed, goal.StatusAborted:
		return toHostGoal(g), nil
	case goal.StatusPending, goal.StatusPaused, goal.StatusBlocked:
		if _, err := a.store.SetStatus(id, goal.StatusActive, ""); err != nil {
			cur, ok, getErr := a.Get(id)
			if getErr != nil || !ok {
				return host.Goal{}, err
			}
			return cur, nil
		}
	case goal.StatusActive:
		// Already active (Resume marked it); just run.
	default:
		return host.Goal{}, fmt.Errorf("goal: cannot run status %s", g.Status)
	}

	// A stop that landed during start cancelled runCtx. Do not hand the
	// goal to the loop: it would run against an already-stopped request.
	// A goal this start activated goes back to paused; an aborted goal is
	// already terminal. Either way the stop stands.
	if runCtx.Err() != nil {
		cur, ok, getErr := a.store.Get(id)
		if getErr != nil || !ok {
			return host.Goal{}, fmt.Errorf("goal: not found")
		}
		if cur.Status == goal.StatusActive {
			if paused, perr := a.store.SetStatus(id, goal.StatusPaused, ""); perr == nil {
				return toHostGoal(paused), nil
			}
		}
		return toHostGoal(cur), nil
	}

	loop := &goal.Loop{
		Store:   a.store,
		Planner: goal.EvaluateOnlyPlanner{},
		Critic: &goal.DefaultCritic{
			WorkDir: a.workDir,
		},
		Executor: goal.ShellExecutor{WorkDir: a.workDir},
		Hooks:    goal.DefaultHooks(),
	}
	lg, lerr := loop.Run(runCtx, id)
	return a.finishRun(id, lg, lerr)
}

// finishRun records a loop error on a goal that is still active. Pause and
// abort set their own status before cancelling the check, so those returns
// are not rewritten into a failure.
func (a *goalsAdapter) finishRun(id string, g goal.Goal, err error) (host.Goal, error) {
	if err == nil {
		return toHostGoal(g), nil
	}
	cur, ok, getErr := a.store.Get(id)
	if getErr != nil || !ok {
		return host.Goal{}, err
	}
	if cur.Status == goal.StatusActive {
		cur.Status = goal.StatusFailed
		cur.FailReason = err.Error()
		cur.FreezeWallClock(time.Now().UTC())
		if uerr := a.store.UpdateGoal(cur); uerr != nil {
			return host.Goal{}, err
		}
		return toHostGoal(cur), err
	}
	return toHostGoal(cur), nil
}

func (a *goalsAdapter) cancelRun(id string) {
	a.mu.Lock()
	cancel := a.cancel[id]
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *goalsAdapter) Pause(id string) (host.Goal, error) {
	g, err := a.store.SetStatus(id, goal.StatusPaused, "")
	a.cancelRun(id)
	if err != nil {
		// A run that is still starting has registered its cancel func but
		// may not have transitioned the goal to active yet, so the store
		// rejects the pause. The cancel above still stops the start (Run
		// checks its context after transitioning), so the pause is not
		// lost: report the current goal instead of an error.
		a.mu.Lock()
		_, starting := a.running[id]
		a.mu.Unlock()
		if starting {
			if cur, ok, getErr := a.Get(id); getErr == nil && ok {
				switch cur.Status {
				case "done", "failed", "aborted":
					// Terminal: the original error stands.
				default:
					return cur, nil
				}
			}
		}
		return host.Goal{}, err
	}
	return toHostGoal(g), nil
}

func (a *goalsAdapter) Resume(id string) (host.Goal, error) {
	g, err := a.store.SetStatus(id, goal.StatusActive, "")
	if err != nil {
		return host.Goal{}, err
	}
	return toHostGoal(g), nil
}

func (a *goalsAdapter) Abort(id string) (host.Goal, error) {
	g, err := a.store.SetStatus(id, goal.StatusAborted, "aborted by user")
	a.cancelRun(id)
	if err != nil {
		return host.Goal{}, err
	}
	return toHostGoal(g), nil
}

func (a *goalsAdapter) Log(id string, iter int) ([]host.GoalIteration, error) {
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
			Tokens:    r.Tokens,
			Verified:  r.Evaluation.AllSatisfied,
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
		ID:                 g.ID,
		Description:        g.Description,
		Status:             string(g.Status),
		MaxIterations:      g.Constraints.MaxIterations,
		MaxCostUSD:         g.Constraints.MaxCostUSD,
		MaxWallClockS:      g.Constraints.MaxWallClockS,
		MaxTokens:          g.Constraints.MaxTokens,
		MaxVerifyFailures:  g.Constraints.MaxVerifyFailures,
		MaxNoProgressIters: g.Constraints.MaxNoProgressIters,
		AllowedTools:       append([]string(nil), g.Constraints.AllowedTools...),
		DenyTools:          append([]string(nil), g.Constraints.DenyTools...),
		Autopilot:          g.Autopilot,
		CostUSD:            g.CostUSD,
		TokensUsed:         g.TokensUsed,
		LastIteration:      g.LastIteration,
		FailReason:         g.FailReason,
		Escalation:         g.Escalation,
		CreatedAt:          g.CreatedAt,
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
