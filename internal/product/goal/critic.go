package goal

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Critic evaluates criteria. The actor/planner must never call this to mark
// its own work done — only the harness invokes Critic after act.
type Critic interface {
	Evaluate(ctx context.Context, g Goal, iter IterationRecord) (EvalRecord, error)
}

// Judge is an optional LLM-as-judge for CheckJudge criteria.
// Implementations must not receive the planner's self-report as ground truth.
type Judge interface {
	Judge(ctx context.Context, goalText, criterion, evidence string) (satisfied bool, notes string, err error)
}

// PredicateFunc evaluates a named predicate over goal state.
type PredicateFunc func(ctx context.Context, g Goal, name string) (bool, string, error)

// DefaultCritic runs CheckSpecs: cmd (exit 0), predicate registry, judge.
type DefaultCritic struct {
	// WorkDir is the cwd for cmd checks.
	WorkDir string
	// Timeout bounds each cmd check (default 60s).
	Timeout time.Duration
	// Predicates maps predicate names to functions.
	Predicates map[string]PredicateFunc
	// Judge handles CheckJudge (nil → judge criteria fail closed).
	Judge Judge
	// LookPath resolves binaries; defaults to exec.LookPath.
	// CmdRunner overrides command execution (tests).
	CmdRunner func(ctx context.Context, workDir, command string) (exitCode int, output string, err error)
}

// Evaluate runs every criterion check and returns results. Criterion.Satisfied
// on the goal is NOT read as authority — only check execution matters.
func (c *DefaultCritic) Evaluate(ctx context.Context, g Goal, _ IterationRecord) (EvalRecord, error) {
	out := EvalRecord{
		Criteria: make([]CriterionResult, 0, len(g.Criteria)),
	}
	all := len(g.Criteria) > 0
	for _, cr := range g.Criteria {
		res := c.evalOne(ctx, g, cr)
		out.Criteria = append(out.Criteria, res)
		if !res.Satisfied {
			all = false
		}
	}
	out.AllSatisfied = all
	return out, nil
}

func (c *DefaultCritic) evalOne(ctx context.Context, g Goal, cr Criterion) CriterionResult {
	res := CriterionResult{Description: cr.Description}
	switch cr.Check.Kind {
	case CheckCmd:
		code, output, err := c.runCmd(ctx, cr.Check.Value)
		res.Evidence = trimEvidence(output)
		if err != nil {
			res.Error = err.Error()
			res.Satisfied = false
			return res
		}
		res.Satisfied = code == 0
		if !res.Satisfied {
			res.Error = fmt.Sprintf("exit %d", code)
		}
		return res
	case CheckPredicate:
		name := strings.TrimSpace(cr.Check.Value)
		fn := c.Predicates[name]
		if fn == nil {
			res.Error = fmt.Sprintf("unknown predicate %q", name)
			return res
		}
		ok, evidence, err := fn(ctx, g, name)
		res.Evidence = trimEvidence(evidence)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.Satisfied = ok
		return res
	case CheckJudge:
		if c.Judge == nil {
			res.Error = "no judge configured"
			return res
		}
		// Evidence: criterion description only — not planner self-report.
		ok, notes, err := c.Judge.Judge(ctx, g.Description, cr.Description, cr.Check.Value)
		res.Evidence = trimEvidence(notes)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.Satisfied = ok
		return res
	default:
		res.Error = fmt.Sprintf("unknown check kind %q", cr.Check.Kind)
		return res
	}
}

func (c *DefaultCritic) runCmd(ctx context.Context, command string) (int, string, error) {
	if c.CmdRunner != nil {
		return c.CmdRunner(ctx, c.WorkDir, command)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// #nosec G204 -- criterion commands are user-authored goal checks.
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if c.WorkDir != "" {
		cmd.Dir = c.WorkDir
	}
	out, err := cmd.CombinedOutput()
	output := string(out)
	if err == nil {
		return 0, output, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), output, nil
	}
	return -1, output, err
}

func trimEvidence(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 2048 {
		return s[:2048] + "…"
	}
	return s
}

// ApplyEval copies critic results onto goal criteria (harness-only mutation).
func ApplyEval(g *Goal, ev EvalRecord) {
	byDesc := make(map[string]bool, len(ev.Criteria))
	for _, c := range ev.Criteria {
		byDesc[c.Description] = c.Satisfied
	}
	for i := range g.Criteria {
		if sat, ok := byDesc[g.Criteria[i].Description]; ok {
			g.Criteria[i].Satisfied = sat
		}
	}
}
