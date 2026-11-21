// Package bestofn runs one prompt against 2–4 models and lets the user pick
// a winner (issue #1346). Each candidate is a normal delegated child
// session spawned through an injected Spawner (team.spawn in the daemon),
// in its own worktree when the prompt may edit files, under the parent's
// permission mode. Nothing is applied automatically: adopting a winner, or
// keeping none, is a separate call that returns which children to keep and
// which to archive.
package bestofn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Candidate limits from the issue.
const (
	MinCandidates = 2
	MaxCandidates = 4
)

// Candidate is one model to run.
type Candidate struct {
	Model  string
	Effort string // optional; provider-specific
}

func (c Candidate) key() string { return c.Model + "\x00" + c.Effort }

// Label is the display name, e.g. "sonnet (high)".
func (c Candidate) Label() string {
	if c.Effort == "" {
		return c.Model
	}
	return c.Model + " (" + c.Effort + ")"
}

// Budget bounds each candidate run. Zero fields mean "no limit".
type Budget struct {
	MaxTurns  int
	MaxTokens int
	Timeout   time.Duration
}

// Request describes one comparison.
type Request struct {
	Prompt     string
	Candidates []Candidate
	// Edits is true when the prompt may change files; each candidate then
	// gets its own worktree. Plan/answer prompts run shared and compare
	// answers only.
	Edits bool
	// PermissionMode is the parent's current mode, passed through verbatim.
	// Candidates never run with a wider mode.
	PermissionMode string
	Budget         Budget
}

// Validate checks the request before anything is spawned.
func (r Request) Validate() error {
	if strings.TrimSpace(r.Prompt) == "" {
		return errors.New("bestofn: prompt is required")
	}
	if n := len(r.Candidates); n < MinCandidates || n > MaxCandidates {
		return fmt.Errorf("bestofn: pick %d–%d models, got %d", MinCandidates, MaxCandidates, n)
	}
	seen := map[string]struct{}{}
	for i, c := range r.Candidates {
		if strings.TrimSpace(c.Model) == "" {
			return fmt.Errorf("bestofn: candidate %d has no model", i+1)
		}
		if _, dup := seen[c.key()]; dup {
			return fmt.Errorf("bestofn: %s is listed twice", c.Label())
		}
		seen[c.key()] = struct{}{}
	}
	if r.Budget.MaxTurns < 0 || r.Budget.MaxTokens < 0 || r.Budget.Timeout < 0 {
		return errors.New("bestofn: budget values must not be negative")
	}
	return nil
}

// CostMultiplier is how many times the single-run cost this comparison
// spends; the picker shows it up front.
func (r Request) CostMultiplier() int { return len(r.Candidates) }

// Isolation values match protocol.TeamSpawn.Isolation.
const (
	IsolationWorktree = "worktree"
	IsolationShared   = "shared"
)

// SpawnSpec is what the Spawner receives for one candidate.
type SpawnSpec struct {
	Name           string
	Objective      string
	Model          string
	Effort         string
	Isolation      string
	PermissionMode string
	Budget         Budget
}

// Specs builds one spawn spec per candidate, in request order.
func (r Request) Specs() []SpawnSpec {
	iso := IsolationShared
	if r.Edits {
		iso = IsolationWorktree
	}
	out := make([]SpawnSpec, len(r.Candidates))
	for i, c := range r.Candidates {
		out[i] = SpawnSpec{
			Name:           fmt.Sprintf("best-of-%d-%d", len(r.Candidates), i+1),
			Objective:      r.Prompt,
			Model:          c.Model,
			Effort:         c.Effort,
			Isolation:      iso,
			PermissionMode: r.PermissionMode,
			Budget:         r.Budget,
		}
	}
	return out
}

// Spawner is the delegation seam (team.spawn / team.child_interrupt).
type Spawner interface {
	Spawn(ctx context.Context, spec SpawnSpec) (childID string, err error)
	Cancel(childID string) error
}

// Status is one candidate's lifecycle state.
type Status string

const (
	StatusRunning   Status = "running"
	StatusDone      Status = "done"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

func (s Status) terminal() bool { return s != StatusRunning }

// Outcome is what a finished child reports.
type Outcome struct {
	Summary      string
	FilesChanged int
	Added        int
	Removed      int
	Tokens       int
	CostUSD      float64
	// Verified is nil when no verification command was configured.
	Verified *bool
	// Err is set when the child failed; Finish then marks it failed.
	Err string
}

// CandidateRun is one candidate's state in a comparison.
type CandidateRun struct {
	Candidate Candidate
	ChildID   string // empty when spawning failed
	Status    Status
	Started   time.Time
	Ended     time.Time
	Outcome   Outcome
}

// Decision says which children to keep and which to archive (worktrees
// cleaned up, sessions kept read-only). Keep is empty for "keep none".
type Decision struct {
	Keep    string
	Archive []string
}

// Run is one live comparison. Safe for concurrent use.
type Run struct {
	mu       sync.Mutex
	sp       Spawner
	now      func() time.Time
	req      Request
	runs     []CandidateRun
	decision *Decision
}

// Start validates req and spawns every candidate. A candidate whose spawn
// fails is recorded as failed and the rest still run; the comparison then
// completes honestly with whichever candidates finish.
func Start(ctx context.Context, sp Spawner, req Request) (*Run, error) {
	return start(ctx, sp, req, time.Now)
}

func start(ctx context.Context, sp Spawner, req Request, now func() time.Time) (*Run, error) {
	if sp == nil {
		return nil, errors.New("bestofn: spawner is required")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	r := &Run{sp: sp, now: now, req: req, runs: make([]CandidateRun, len(req.Candidates))}
	for i, spec := range req.Specs() {
		cr := CandidateRun{Candidate: req.Candidates[i], Started: now()}
		id, err := sp.Spawn(ctx, spec)
		switch {
		case err != nil:
			cr.Status, cr.Ended, cr.Outcome.Err = StatusFailed, now(), err.Error()
		case id == "":
			cr.Status, cr.Ended, cr.Outcome.Err = StatusFailed, now(), "spawner returned no child id"
		default:
			cr.ChildID, cr.Status = id, StatusRunning
		}
		r.runs[i] = cr
	}
	return r, nil
}

// Request returns the request this run was started with.
func (r *Run) Request() Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	req := r.req
	req.Candidates = append([]Candidate(nil), r.req.Candidates...)
	return req
}

// Finish records a child's outcome. A non-empty Outcome.Err marks it failed.
// Finishing a child that is already terminal is an error (no overwrite).
func (r *Run) Finish(childID string, out Outcome) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cr, err := r.findLocked(childID)
	if err != nil {
		return err
	}
	if cr.Status.terminal() {
		return fmt.Errorf("bestofn: %s already %s", childID, cr.Status)
	}
	cr.Outcome, cr.Ended = out, r.now()
	cr.Status = StatusDone
	if out.Err != "" {
		cr.Status = StatusFailed
	}
	return nil
}

// Cancel interrupts one running candidate.
func (r *Run) Cancel(childID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cr, err := r.findLocked(childID)
	if err != nil {
		return err
	}
	if cr.Status.terminal() {
		return fmt.Errorf("bestofn: %s already %s", childID, cr.Status)
	}
	return r.cancelLocked(cr)
}

// CancelAll interrupts every running candidate. It returns the first
// interrupt error but still attempts and marks every candidate.
func (r *Run) CancelAll() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var first error
	for i := range r.runs {
		if r.runs[i].Status.terminal() {
			continue
		}
		if err := r.cancelLocked(&r.runs[i]); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// cancelLocked marks cr cancelled even if the interrupt fails, so the card
// never shows a candidate as running after the user cancelled it.
func (r *Run) cancelLocked(cr *CandidateRun) error {
	err := r.sp.Cancel(cr.ChildID)
	cr.Status, cr.Ended = StatusCancelled, r.now()
	if err != nil {
		cr.Outcome.Err = err.Error()
		return fmt.Errorf("bestofn: cancel %s: %w", cr.ChildID, err)
	}
	return nil
}

// Complete reports whether every candidate has stopped.
func (r *Run) Complete() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cr := range r.runs {
		if !cr.Status.terminal() {
			return false
		}
	}
	return true
}

// Candidates returns a copy of every candidate's state, in request order.
func (r *Run) Candidates() []CandidateRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]CandidateRun, len(r.runs))
	for i, cr := range r.runs {
		out[i] = cr
		if cr.Outcome.Verified != nil {
			v := *cr.Outcome.Verified
			out[i].Outcome.Verified = &v
		}
	}
	return out
}

// Adopt picks childID as the winner. It requires every candidate to have
// stopped and the winner to have finished successfully. Every other child
// that was spawned is archived. A run can be decided once.
func (r *Run) Adopt(childID string) (Decision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.decidableLocked(); err != nil {
		return Decision{}, err
	}
	cr, err := r.findLocked(childID)
	if err != nil {
		return Decision{}, err
	}
	if cr.Status != StatusDone {
		return Decision{}, fmt.Errorf("bestofn: cannot adopt %s: it is %s", childID, cr.Status)
	}
	return r.decideLocked(childID), nil
}

// KeepNone discards every candidate.
func (r *Run) KeepNone() (Decision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.decidableLocked(); err != nil {
		return Decision{}, err
	}
	return r.decideLocked(""), nil
}

// Decision returns the recorded decision, if any.
func (r *Run) Decision() (Decision, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.decision == nil {
		return Decision{}, false
	}
	d := *r.decision
	d.Archive = append([]string(nil), d.Archive...)
	return d, true
}

func (r *Run) decidableLocked() error {
	if r.decision != nil {
		return errors.New("bestofn: a winner was already decided for this run")
	}
	for _, cr := range r.runs {
		if !cr.Status.terminal() {
			return errors.New("bestofn: candidates are still running; wait or cancel them first")
		}
	}
	return nil
}

func (r *Run) decideLocked(keep string) Decision {
	d := Decision{Keep: keep}
	for _, cr := range r.runs {
		if cr.ChildID != "" && cr.ChildID != keep {
			d.Archive = append(d.Archive, cr.ChildID)
		}
	}
	r.decision = &d
	out := d
	out.Archive = append([]string(nil), d.Archive...)
	return out
}

func (r *Run) findLocked(childID string) (*CandidateRun, error) {
	if childID == "" {
		return nil, errors.New("bestofn: child id is required")
	}
	for i := range r.runs {
		if r.runs[i].ChildID == childID {
			return &r.runs[i], nil
		}
	}
	return nil, fmt.Errorf("bestofn: unknown child %q", childID)
}
