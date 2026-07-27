package goal

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// GuardResult is the outcome of one termination check.
type GuardResult struct {
	Tripped bool
	Reason  string
	// NextStatus is applied when Tripped (done|failed|aborted).
	NextStatus Status
}

// GuardContext is snapshot input for ordered termination guards.
type GuardContext struct {
	Goal    Goal
	History []IterationRecord
	Now     time.Time
	// LastStateHash is the hash from the iteration just evaluated (may equal
	// the latest history entry after commit — callers pass pre-commit hash).
	LastStateHash string
}

// CheckGuards runs termination guards in order; first trip wins.
// Order: success, human_abort, budget, no_progress, irrecoverable.
func CheckGuards(ctx GuardContext) GuardResult {
	if r := guardSuccess(ctx); r.Tripped {
		return r
	}
	if r := guardHumanAbort(ctx); r.Tripped {
		return r
	}
	if r := guardBudget(ctx); r.Tripped {
		return r
	}
	if r := guardNoProgress(ctx); r.Tripped {
		return r
	}
	if r := guardIrrecoverable(ctx); r.Tripped {
		return r
	}
	return GuardResult{}
}

func guardSuccess(ctx GuardContext) GuardResult {
	if AllCriteriaSatisfied(ctx.Goal.Criteria) {
		return GuardResult{Tripped: true, Reason: "all criteria satisfied", NextStatus: StatusDone}
	}
	return GuardResult{}
}

func guardHumanAbort(ctx GuardContext) GuardResult {
	if ctx.Goal.AbortRequested || ctx.Goal.Status == StatusAborted {
		reason := ctx.Goal.FailReason
		if reason == "" {
			reason = "aborted by user"
		}
		return GuardResult{Tripped: true, Reason: reason, NextStatus: StatusAborted}
	}
	return GuardResult{}
}

func guardBudget(ctx GuardContext) GuardResult {
	c := ctx.Goal.Constraints
	// Iteration ceiling: after committing N iterations, stop if N >= max.
	if ctx.Goal.LastIteration >= c.MaxIterations {
		return GuardResult{
			Tripped:    true,
			Reason:     fmt.Sprintf("iteration budget exhausted (%d/%d)", ctx.Goal.LastIteration, c.MaxIterations),
			NextStatus: StatusFailed,
		}
	}
	if c.MaxCostUSD > 0 && ctx.Goal.CostUSD >= c.MaxCostUSD {
		return GuardResult{
			Tripped:    true,
			Reason:     fmt.Sprintf("cost budget exhausted (%.4f/%.4f USD)", ctx.Goal.CostUSD, c.MaxCostUSD),
			NextStatus: StatusFailed,
		}
	}
	if !ctx.Goal.ActiveStartedAt.IsZero() && c.MaxWallClockS > 0 {
		elapsed := ctx.Now.Sub(ctx.Goal.ActiveStartedAt)
		if elapsed >= time.Duration(c.MaxWallClockS)*time.Second {
			return GuardResult{
				Tripped:    true,
				Reason:     fmt.Sprintf("wall-clock budget exhausted (%s >= %ds)", elapsed.Round(time.Second), c.MaxWallClockS),
				NextStatus: StatusFailed,
			}
		}
	}
	return GuardResult{}
}

func guardNoProgress(ctx GuardContext) GuardResult {
	n := ctx.Goal.Constraints.MaxNoProgressIters
	if n < 1 {
		n = 3
	}
	// Build hash sequence: history + optional current.
	hashes := make([]string, 0, len(ctx.History)+1)
	for _, h := range ctx.History {
		if h.StateHash != "" {
			hashes = append(hashes, h.StateHash)
		}
	}
	if ctx.LastStateHash != "" {
		// If history already ends with this hash (post-commit call), don't duplex.
		if len(hashes) == 0 || hashes[len(hashes)-1] != ctx.LastStateHash {
			hashes = append(hashes, ctx.LastStateHash)
		}
	}
	if len(hashes) < n {
		return GuardResult{}
	}
	last := hashes[len(hashes)-1]
	if last == "" {
		return GuardResult{}
	}
	streak := 0
	for i := len(hashes) - 1; i >= 0; i-- {
		if hashes[i] == last {
			streak++
		} else {
			break
		}
	}
	if streak >= n {
		return GuardResult{
			Tripped:    true,
			Reason:     fmt.Sprintf("no progress: state_hash unchanged for %d iterations", streak),
			NextStatus: StatusFailed,
		}
	}
	// Action-sequence similarity: identical tool sequences across last n iters.
	if actionSequenceStuck(ctx.History, n) {
		return GuardResult{
			Tripped:    true,
			Reason:     fmt.Sprintf("no progress: identical action sequence for %d iterations", n),
			NextStatus: StatusFailed,
		}
	}
	return GuardResult{}
}

func actionSequenceStuck(history []IterationRecord, n int) bool {
	if n < 2 || len(history) < n {
		return false
	}
	tail := history[len(history)-n:]
	sig := actionSig(tail[0].Actions)
	if sig == "" {
		return false
	}
	for _, rec := range tail[1:] {
		if actionSig(rec.Actions) != sig {
			return false
		}
	}
	return true
}

func actionSig(actions []ActionRecord) string {
	if len(actions) == 0 {
		return ""
	}
	var b strings.Builder
	for _, a := range actions {
		b.WriteString(a.Tool)
		b.WriteByte('|')
		if len(a.Args) > 0 {
			keys := make([]string, 0, len(a.Args))
			for k := range a.Args {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				b.WriteString(k)
				b.WriteByte('=')
				b.WriteString(a.Args[k])
				b.WriteByte(';')
			}
		}
		b.WriteByte('/')
	}
	return b.String()
}

func guardIrrecoverable(ctx GuardContext) GuardResult {
	// Same tool error class 3× in a row across recent actions.
	const need = 3
	type fail struct {
		tool string
		cls  string
	}
	var recent []fail
	for i := len(ctx.History) - 1; i >= 0 && len(recent) < need; i-- {
		acts := ctx.History[i].Actions
		for j := len(acts) - 1; j >= 0 && len(recent) < need; j-- {
			a := acts[j]
			if a.OK || a.Blocked {
				continue
			}
			cls := a.Error
			if cls == "" {
				cls = "error"
			}
			// coarse class: first line / short prefix
			if len(cls) > 64 {
				cls = cls[:64]
			}
			recent = append(recent, fail{tool: a.Tool, cls: cls})
		}
	}
	if len(recent) < need {
		return GuardResult{}
	}
	first := recent[0]
	for _, f := range recent[1:need] {
		if f.tool != first.tool || f.cls != first.cls {
			return GuardResult{}
		}
	}
	return GuardResult{
		Tripped:    true,
		Reason:     fmt.Sprintf("irrecoverable: tool %q failed %d× with %q", first.tool, need, first.cls),
		NextStatus: StatusFailed,
	}
}
