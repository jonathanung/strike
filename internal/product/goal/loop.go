package goal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Planner produces the next plan. Inject a fake in tests; production may use
// an LLM or evaluate-only stub. The planner must never mutate criterion state.
type Planner interface {
	Plan(ctx context.Context, g Goal, obs Observation) (Plan, error)
}

// Executor runs one planned action. Must honor idempotency via store intents.
type Executor interface {
	Execute(ctx context.Context, g Goal, action Action) (result string, costUSD float64, err error)
}

// Reflector optionally compresses iteration learnings (scratchpad).
type Reflector interface {
	Reflect(ctx context.Context, g Goal, rec IterationRecord) (scratch string, err error)
}

// Clock abstracts time for tests.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

// Loop is the deterministic observe→plan→act→evaluate→guard→commit controller.
type Loop struct {
	Store    *Store
	Planner  Planner
	Executor Executor
	Critic   Critic
	Hooks    Hooks
	Reflect  Reflector
	Clock    Clock
	// Scratch is optional in-memory digest per goal id.
	scratch map[string]string
}

// Run activates a goal (if pending/paused) and loops until a terminal status.
// Safe to call after crash: resumes from LastIteration+1; completed intents skip.
func (l *Loop) Run(ctx context.Context, id string) (Goal, error) {
	if l.Store == nil {
		return Goal{}, fmt.Errorf("goal: nil store")
	}
	if l.Planner == nil {
		return Goal{}, fmt.Errorf("goal: nil planner")
	}
	if l.Critic == nil {
		return Goal{}, fmt.Errorf("goal: nil critic")
	}
	if l.Clock == nil {
		l.Clock = systemClock{}
	}
	if l.scratch == nil {
		l.scratch = make(map[string]string)
	}
	if l.Hooks.PreAct == nil {
		l.Hooks.PreAct = AllowlistPreAct
	}

	g, ok, err := l.Store.Get(id)
	if err != nil {
		return Goal{}, err
	}
	if !ok {
		return Goal{}, ErrNotFound
	}
	switch g.Status {
	case StatusDone, StatusFailed, StatusAborted:
		return g, nil
	case StatusActive:
		// resume
	case StatusPending, StatusPaused:
		g, err = l.Store.SetStatus(id, StatusActive, "")
		if err != nil {
			return Goal{}, err
		}
	default:
		return Goal{}, fmt.Errorf("goal: cannot run status %s", g.Status)
	}

	for {
		if err := ctx.Err(); err != nil {
			return g, err
		}
		// Reload for abort/pause flags written by another caller.
		fresh, ok, err := l.Store.Get(id)
		if err != nil {
			return g, err
		}
		if !ok {
			return g, ErrNotFound
		}
		g = fresh
		if g.Status == StatusPaused {
			return g, nil
		}
		if g.Status != StatusActive {
			return g, nil
		}

		// Pre-iteration abort / budget (iteration ceiling before starting next).
		history, err := l.Store.ListIterations(id)
		if err != nil {
			return g, err
		}
		if gr := CheckGuards(GuardContext{Goal: g, History: history, Now: l.Clock.Now()}); gr.Tripped {
			return l.applyGuard(g, gr)
		}

		nextN := g.LastIteration + 1
		rec, err := l.oneIteration(ctx, g, nextN, history)
		if err != nil {
			return g, err
		}

		// Reload goal after side effects; apply critic marks.
		// Preserve pause/abort written concurrently during the iteration.
		live, ok, err := l.Store.Get(id)
		if err != nil {
			return g, err
		}
		if !ok {
			return g, ErrNotFound
		}
		g = live
		ApplyEval(&g, rec.Evaluation)
		g.CostUSD += rec.CostUSD
		g.LastIteration = rec.N

		history = append(history, rec)
		// Terminal guards only when still active (do not override pause).
		if g.Status == StatusActive || g.AbortRequested {
			gr := CheckGuards(GuardContext{
				Goal:          g,
				History:       history,
				Now:           l.Clock.Now(),
				LastStateHash: rec.StateHash,
			})
			if gr.Tripped {
				g.Status = gr.NextStatus
				g.FailReason = gr.Reason
				if gr.NextStatus == StatusDone {
					g.FailReason = ""
				}
			}
		}

		if l.Hooks.PreCommit != nil {
			if err := l.Hooks.PreCommit(g, rec); err != nil {
				return g, fmt.Errorf("goal: pre_commit blocked: %w", err)
			}
		}
		if err := l.Store.CommitIteration(g, rec); err != nil {
			return g, err
		}
		// Re-read committed goal.
		g, _, err = l.Store.Get(id)
		if err != nil {
			return g, err
		}
		if g.Status != StatusActive {
			return g, nil
		}
	}
}

func (l *Loop) applyGuard(g Goal, gr GuardResult) (Goal, error) {
	g.Status = gr.NextStatus
	g.FailReason = gr.Reason
	if gr.NextStatus == StatusDone {
		g.FailReason = ""
	}
	if err := l.Store.UpdateGoal(g); err != nil {
		return g, err
	}
	_ = l.Store.AppendEvent(Event{
		At:     l.Clock.Now(),
		GoalID: g.ID,
		Type:   "guard.tripped",
		Payload: mustJSON(map[string]string{
			"reason": gr.Reason,
			"status": string(gr.NextStatus),
		}),
	})
	out, _, err := l.Store.Get(g.ID)
	return out, err
}

func (l *Loop) oneIteration(ctx context.Context, g Goal, n int, history []IterationRecord) (IterationRecord, error) {
	obs := l.observe(g, history)
	_ = l.Store.AppendEvent(Event{
		At: l.Clock.Now(), GoalID: g.ID, Type: "stage.observe", Iter: n,
		Payload: mustJSON(map[string]string{"digest": obs.Digest}),
	})

	plan, err := l.Planner.Plan(ctx, g, obs)
	if err != nil {
		return IterationRecord{}, fmt.Errorf("plan: %w", err)
	}
	_ = l.Store.AppendEvent(Event{
		At: l.Clock.Now(), GoalID: g.ID, Type: "stage.plan", Iter: n,
		Payload: mustJSON(map[string]any{"summary": plan.Summary, "actions": len(plan.Actions)}),
	})

	var actions []ActionRecord
	var cost float64
	for i, action := range plan.Actions {
		key := IntentKey(g.ID, n, i)
		rec := ActionRecord{
			Index:     i,
			Tool:      action.Tool,
			Args:      cloneArgs(action.Args),
			IntentKey: key,
		}
		if l.Store.IntentDone(key) {
			rec.OK = true
			rec.Completed = true
			rec.Result = "skipped: intent already completed"
			actions = append(actions, rec)
			continue
		}

		// pre_act
		act := action
		if l.Hooks.PreAct != nil {
			pr := l.Hooks.PreAct(g, act)
			switch pr.Decision {
			case HookBlock:
				rec.Blocked = true
				rec.BlockReason = pr.Reason
				rec.OK = false
				rec.Error = pr.Reason
				rec.Completed = true
				_ = l.Store.MarkIntent(key)
				actions = append(actions, rec)
				_ = l.Store.AppendEvent(Event{
					At: l.Clock.Now(), GoalID: g.ID, Type: "stage.act_blocked", Iter: n,
					Payload: mustJSON(map[string]string{"tool": act.Tool, "reason": pr.Reason}),
				})
				continue
			case HookTransform:
				act = pr.Action
				rec.Tool = act.Tool
				rec.Args = cloneArgs(act.Args)
			}
		}

		start := l.Clock.Now()
		if l.Executor == nil {
			rec.OK = false
			rec.Error = "no executor configured"
			rec.Completed = true
			_ = l.Store.MarkIntent(key)
			rec.Duration = l.Clock.Now().Sub(start)
			actions = append(actions, rec)
			continue
		}
		result, c, execErr := l.Executor.Execute(ctx, g, act)
		rec.Duration = l.Clock.Now().Sub(start)
		rec.Result = result
		rec.CostUSD = c
		cost += c
		if execErr != nil {
			rec.OK = false
			rec.Error = execErr.Error()
		} else {
			rec.OK = true
		}
		rec.Completed = true
		_ = l.Store.MarkIntent(key)
		if l.Hooks.PostAct != nil {
			l.Hooks.PostAct(g, act, rec)
		}
		actions = append(actions, rec)
		_ = l.Store.AppendEvent(Event{
			At: l.Clock.Now(), GoalID: g.ID, Type: "stage.act", Iter: n,
			Payload: mustJSON(map[string]any{"tool": rec.Tool, "ok": rec.OK, "cost": rec.CostUSD}),
		})
	}

	return l.finishIteration(ctx, g, n, obs, plan, actions, cost)
}

// finishIteration is split for clarity after evaluate.
func (l *Loop) finishIteration(ctx context.Context, g Goal, n int, obs Observation, plan Plan, actions []ActionRecord, cost float64) (IterationRecord, error) {
	draft := IterationRecord{
		N:                 n,
		ObservationDigest: obs.Digest,
		Plan:              plan.Summary,
		Actions:           actions,
		CostUSD:           cost,
	}
	ev, err := l.Critic.Evaluate(ctx, g, draft)
	if err != nil {
		return IterationRecord{}, fmt.Errorf("evaluate: %w", err)
	}
	_ = l.Store.AppendEvent(Event{
		At: l.Clock.Now(), GoalID: g.ID, Type: "stage.evaluate", Iter: n,
		Payload: mustJSON(ev),
	})
	draft.Evaluation = ev

	// state_hash: criteria booleans + check identities + action outcomes — not transcript.
	draft.StateHash = stateHash(g, ev, actions)

	if l.Reflect != nil {
		if scratch, err := l.Reflect.Reflect(ctx, g, draft); err == nil {
			l.scratch[g.ID] = scratch
		}
	}
	_ = l.Store.AppendEvent(Event{
		At: l.Clock.Now(), GoalID: g.ID, Type: "stage.reflect", Iter: n,
		Payload: mustJSON(map[string]string{"state_hash": draft.StateHash}),
	})
	return draft, nil
}

func (l *Loop) observe(g Goal, history []IterationRecord) Observation {
	obs := Observation{
		GoalID:      g.ID,
		Description: g.Description,
		Status:      g.Status,
		Iteration:   g.LastIteration + 1,
		Criteria:    append([]Criterion(nil), g.Criteria...),
		Scratch:     l.scratch[g.ID],
	}
	if len(history) > 0 {
		last := history[len(history)-1]
		ev := last.Evaluation
		obs.LastEval = &ev
	}
	// Digest excludes volatile timestamps.
	payload, _ := json.Marshal(struct {
		ID       string      `json:"id"`
		Desc     string      `json:"desc"`
		Criteria []Criterion `json:"criteria"`
		LastN    int         `json:"last_n"`
		Scratch  string      `json:"scratch"`
	}{g.ID, g.Description, g.Criteria, g.LastIteration, obs.Scratch})
	sum := sha256.Sum256(payload)
	obs.Digest = hex.EncodeToString(sum[:8])
	return obs
}

func stateHash(g Goal, ev EvalRecord, actions []ActionRecord) string {
	type wire struct {
		Criteria []CriterionResult `json:"criteria"`
		Tools    []string          `json:"tools"`
		OK       []bool            `json:"ok"`
	}
	w := wire{Criteria: ev.Criteria}
	for _, a := range actions {
		w.Tools = append(w.Tools, a.Tool)
		w.OK = append(w.OK, a.OK && !a.Blocked)
	}
	b, _ := json.Marshal(w)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

func cloneArgs(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// EvaluateOnlyPlanner returns an empty plan so the critic can score criteria.
// Safe default when no LLM planner is wired.
type EvaluateOnlyPlanner struct{}

func (EvaluateOnlyPlanner) Plan(context.Context, Goal, Observation) (Plan, error) {
	return Plan{Summary: "evaluate-only"}, nil
}

// FuncPlanner adapts a function to Planner.
type FuncPlanner func(ctx context.Context, g Goal, obs Observation) (Plan, error)

func (f FuncPlanner) Plan(ctx context.Context, g Goal, obs Observation) (Plan, error) {
	return f(ctx, g, obs)
}

// FuncExecutor adapts a function to Executor.
type FuncExecutor func(ctx context.Context, g Goal, action Action) (string, float64, error)

func (f FuncExecutor) Execute(ctx context.Context, g Goal, action Action) (string, float64, error) {
	return f(ctx, g, action)
}

// ShellExecutor runs action tool "bash" or "sh" with args["command"].
type ShellExecutor struct {
	WorkDir string
	Runner  func(ctx context.Context, workDir, command string) (string, error)
}

func (e ShellExecutor) Execute(ctx context.Context, _ Goal, action Action) (string, float64, error) {
	tool := strings.ToLower(strings.TrimSpace(action.Tool))
	if tool != "bash" && tool != "sh" && tool != "shell" {
		return "", 0, fmt.Errorf("shell executor: unsupported tool %q", action.Tool)
	}
	cmd := action.Args["command"]
	if strings.TrimSpace(cmd) == "" {
		cmd = action.Args["cmd"]
	}
	if strings.TrimSpace(cmd) == "" {
		return "", 0, fmt.Errorf("shell executor: missing command arg")
	}
	if e.Runner != nil {
		out, err := e.Runner(ctx, e.WorkDir, cmd)
		return out, 0, err
	}
	// Reuse critic cmd runner path via DefaultCritic.
	c := &DefaultCritic{WorkDir: e.WorkDir}
	code, out, err := c.runCmd(ctx, cmd)
	if err != nil {
		return out, 0, err
	}
	if code != 0 {
		return out, 0, fmt.Errorf("exit %d", code)
	}
	return out, 0, nil
}
