package server

// Best-of-N (#1346 / #1881): run one prompt across 2–4 models as normal
// team.spawn children, track them live, then adopt one winner or keep none.
// The comparison core lives in internal/daemon/bestofn; this file is the
// HTTP surface plus the team.spawn / team.child_interrupt seam, completion
// tracking from child.completed, applying the winner's patch, and removing
// every candidate's strike-managed worktree once the user decides.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/internal/daemon/bestofn"
	"github.com/jonathanung/strike/internal/product/project"
	"github.com/jonathanung/strike/internal/protocol"
)

const (
	// bestOfNKeep bounds the in-memory registry; the oldest decided runs go first.
	bestOfNKeep = 32
	// bestOfNWatchMax stops the live watcher; GET still reconciles later
	// completions from the session log.
	bestOfNWatchMax = 2 * time.Hour
	// bestOfNMaxPatchBytes caps a stored winner patch.
	bestOfNMaxPatchBytes = 4 << 20
)

// bestOfNEntry is one comparison owned by a root session.
type bestOfNEntry struct {
	id      string
	owner   string
	cwd     string // owner's working directory at start; adopt applies here
	created time.Time
	run     *bestofn.Run

	mu      sync.Mutex // serialises adopt / keep-none and guards fields below
	patches map[string]string
	note    string
}

type bestOfNRegistry struct {
	mu    sync.Mutex
	runs  map[string]*bestOfNEntry
	order []string
}

func (g *bestOfNRegistry) put(e *bestOfNEntry) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.runs == nil {
		g.runs = map[string]*bestOfNEntry{}
	}
	g.runs[e.id] = e
	g.order = append(g.order, e.id)
	for len(g.order) > bestOfNKeep {
		victim := -1
		for i, id := range g.order {
			if _, decided := g.runs[id].run.Decision(); decided || g.runs[id].run.Complete() {
				victim = i
				break
			}
		}
		if victim < 0 {
			victim = 0
		}
		delete(g.runs, g.order[victim])
		g.order = append(g.order[:victim], g.order[victim+1:]...)
	}
}

func (g *bestOfNRegistry) get(id string) *bestOfNEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.runs[id]
}

func (g *bestOfNRegistry) list(owner string) []*bestOfNEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*bestOfNEntry, 0, len(g.order))
	for i := len(g.order) - 1; i >= 0; i-- {
		e := g.runs[g.order[i]]
		if owner == "" || e.owner == owner {
			out = append(out, e)
		}
	}
	return out
}

// liveBestOfNSpawner implements bestofn.Spawner over team.spawn and
// team.child_interrupt on the owner's live engine.
type liveBestOfNSpawner struct {
	s     *Server
	owner string
	runID string
}

func (l liveBestOfNSpawner) Spawn(ctx context.Context, spec bestofn.SpawnSpec) (string, error) {
	reply := make(chan protocol.TeamOpOutcome, 1)
	op := protocol.TeamSpawn{
		Objective: spec.Objective,
		// Teammate names are unique per session team; the run id keeps a
		// second comparison in the same session from colliding.
		Name:      spec.Name + "-" + l.runID,
		Isolation: spec.Isolation,
		Model:     spec.Model,
		Effort:    spec.Effort,
		Reply:     reply,
	}
	if spec.Budget.MaxTurns > 0 || spec.Budget.MaxTokens > 0 {
		op.Budget = &protocol.TeamSpawnBudget{MaxTurns: spec.Budget.MaxTurns, MaxTokens: spec.Budget.MaxTokens}
	}
	op.IdempotencyKey = "bestofn-" + l.runID + "-" + spec.Name
	op.RootSessionID = l.owner
	out, err := l.s.submitTeamOp(ctx, l.owner, op, reply)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out.ChildSessionID) == "" {
		return "", errors.New("team.spawn returned no child")
	}
	return out.ChildSessionID, nil
}

func (l liveBestOfNSpawner) Cancel(childID string) error {
	reply := make(chan protocol.TeamOpOutcome, 1)
	op := protocol.TeamChildInterrupt{ChildSessionID: childID, Reason: "best-of-N cancelled", Reply: reply}
	op.IdempotencyKey = "bestofn-cancel-" + l.runID + "-" + childID
	op.RootSessionID = l.owner
	_, err := l.s.submitTeamOp(context.Background(), l.owner, op, reply)
	return err
}

// submitTeamOp submits a team-control op to the owner's live engine and
// waits for its in-process reply.
func (s *Server) submitTeamOp(ctx context.Context, owner string, op protocol.Op, reply <-chan protocol.TeamOpOutcome) (protocol.TeamOpOutcome, error) {
	live := s.liveForOwner(owner)
	if live == nil {
		return protocol.TeamOpOutcome{}, errors.New("owning session is not live")
	}
	submitCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := live.Submit(submitCtx, op); err != nil {
		return protocol.TeamOpOutcome{}, err
	}
	select {
	case out := <-reply:
		if !out.OK {
			msg := out.Error
			if msg == "" {
				msg = out.Code
			}
			return out, errors.New(msg)
		}
		return out, nil
	case <-submitCtx.Done():
		return protocol.TeamOpOutcome{}, errors.New("team op timed out")
	}
}

func newBestOfNID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// startBestOfN validates req, spawns every candidate through sp and
// registers the run. cwd is where an adopted patch is applied.
func (s *Server) startBestOfN(ctx context.Context, owner, cwd string, sp bestofn.Spawner, req bestofn.Request) (*bestOfNEntry, error) {
	run, err := bestofn.Start(ctx, sp, req)
	if err != nil {
		return nil, err
	}
	return s.registerBestOfN(newBestOfNID(), owner, cwd, run), nil
}

func (s *Server) registerBestOfN(id, owner, cwd string, run *bestofn.Run) *bestOfNEntry {
	e := &bestOfNEntry{
		id:      id,
		owner:   owner,
		cwd:     cwd,
		created: time.Now(),
		run:     run,
		patches: map[string]string{},
	}
	s.bestOfN.put(e)
	return e
}

// applyBestOfNChild records a child.completed for a candidate of e.
// Completions for other children, or for candidates already stopped, are
// ignored.
func (s *Server) applyBestOfNChild(e *bestOfNEntry, cc protocol.ChildCompleted) {
	if e == nil || strings.TrimSpace(cc.SessionID) == "" {
		return
	}
	for _, cr := range e.run.Candidates() {
		if cr.ChildID != cc.SessionID || cr.Status != bestofn.StatusRunning {
			continue
		}
		if err := e.run.Finish(cc.SessionID, bestOfNOutcome(cc)); err != nil {
			return
		}
		if p := cc.Handoff.Patch; strings.TrimSpace(p) != "" && len(p) <= bestOfNMaxPatchBytes {
			e.mu.Lock()
			e.patches[cc.SessionID] = p
			e.mu.Unlock()
		}
		return
	}
}

// bestOfNOutcome maps a child completion onto the comparison outcome.
func bestOfNOutcome(cc protocol.ChildCompleted) bestofn.Outcome {
	h := cc.Handoff
	summary := strings.TrimSpace(h.Summary)
	if summary == "" {
		summary = strings.TrimSpace(cc.Summary)
	}
	files, added, removed := bestofn.Diffstat(h.Patch)
	if files == 0 {
		files = len(h.FilesChanged)
	}
	out := bestofn.Outcome{
		Summary:      summary,
		FilesChanged: files,
		Added:        added,
		Removed:      removed,
		Worktree:     strings.TrimSpace(h.WorktreePath),
	}
	if v := cc.Verification; v != nil {
		passed := v.Passed
		out.Verified = &passed
	}
	switch cc.Status {
	case protocol.ChildStatusCompleted:
	case protocol.ChildStatusCanceled:
		out.Err = "cancelled"
	case protocol.ChildStatusBlocked:
		out.Err = "blocked"
		if len(h.Blockers) > 0 {
			out.Err += ": " + strings.Join(h.Blockers, "; ")
		} else if cc.Verification != nil && !cc.Verification.Passed {
			out.Err += ": verification failed"
		}
	default:
		out.Err = string(cc.Status)
		if out.Err == "" {
			out.Err = "failed"
		}
		if summary != "" {
			out.Err += ": " + summary
		}
	}
	if cc.BudgetKind != "" && out.Err != "" {
		out.Err += " (budget: " + cc.BudgetKind + ")"
	}
	return out
}

// watchBestOfN feeds child.completed events into e until every candidate
// stops. A positive timeout cancels candidates still running when it fires.
func (s *Server) watchBestOfN(ctx context.Context, cancel context.CancelFunc, e *bestOfNEntry, sub <-chan protocol.Event, timeout time.Duration) {
	defer cancel()
	var deadline <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		deadline = t.C
	}
	for !e.run.Complete() {
		select {
		case ev, ok := <-sub:
			if !ok {
				return
			}
			if cc, ok := ev.(protocol.ChildCompleted); ok {
				s.applyBestOfNChild(e, cc)
			}
		case <-deadline:
			_ = e.run.CancelAll()
			e.mu.Lock()
			e.note = "Per-run time budget reached; candidates still running were cancelled."
			e.mu.Unlock()
			return
		case <-ctx.Done():
			return
		}
	}
}

// reconcileBestOfN applies completions already in the owner's session log,
// so a GET after the watcher stopped still shows the truth.
func (s *Server) reconcileBestOfN(e *bestOfNEntry) {
	if e.run.Complete() || s.opts.Services == nil || s.opts.Services.Sessions == nil {
		return
	}
	raw, err := s.opts.Services.Sessions.ReplayJSONL(e.owner)
	if err != nil || len(raw) == 0 {
		return
	}
	evs, err := decodeSessionJSONL(raw)
	if err != nil {
		return
	}
	for _, ev := range evs {
		if cc, ok := ev.(protocol.ChildCompleted); ok {
			s.applyBestOfNChild(e, cc)
		}
	}
}

// adoptBestOfN applies the winner's patch (when it has one) to the owner's
// working tree, records the decision, then removes every candidate's
// worktree. A patch that does not apply leaves the run undecided.
func (s *Server) adoptBestOfN(ctx context.Context, e *bestOfNEntry, childID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.run.CheckAdopt(childID); err != nil {
		return err
	}
	note := "Adopted " + childID + "."
	if patch := e.patches[childID]; strings.TrimSpace(patch) != "" {
		if strings.TrimSpace(e.cwd) == "" {
			return errors.New("bestofn: no working directory to apply the winner's changes to")
		}
		if err := s.gitApplyPatch(ctx, e.cwd, patch); err != nil {
			return fmt.Errorf("bestofn: winner's changes do not apply cleanly; nothing was changed: %w", err)
		}
		files, added, removed := bestofn.Diffstat(patch)
		note = fmt.Sprintf("Adopted %s: applied %d file(s), +%d −%d.", childID, files, added, removed)
	}
	d, err := e.run.Adopt(childID)
	if err != nil {
		return err
	}
	e.note = note + s.cleanupBestOfNWorktrees(ctx, e, d)
	return nil
}

func (s *Server) keepNoneBestOfN(ctx context.Context, e *bestOfNEntry) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	d, err := e.run.KeepNone()
	if err != nil {
		return err
	}
	e.note = "Kept none." + s.cleanupBestOfNWorktrees(ctx, e, d)
	return nil
}

// cleanupBestOfNWorktrees removes the strike-managed worktree (and branch)
// of every spawned candidate, winner included: the winner's changes are in
// the owner's tree now. Missing worktrees are fine. Returns a note suffix.
func (s *Server) cleanupBestOfNWorktrees(ctx context.Context, e *bestOfNEntry, d bestofn.Decision) string {
	if !e.run.Request().Edits || strings.TrimSpace(e.cwd) == "" {
		return ""
	}
	repo, err := project.MainRoot(ctx, e.cwd)
	if err != nil {
		return " Worktree cleanup skipped: " + err.Error()
	}
	ids := append([]string(nil), d.Archive...)
	if d.Keep != "" {
		ids = append(ids, d.Keep)
	}
	paths := map[string]string{}
	for _, cr := range e.run.Candidates() {
		paths[cr.ChildID] = cr.Outcome.Worktree
	}
	var failed []string
	for _, id := range ids {
		path := paths[id]
		if path == "" {
			path = project.WorktreePath(repo, id)
		}
		if err := s.removeStrikeWorktree(ctx, e.cwd, path); err != nil {
			failed = append(failed, id+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		return " Some worktrees were not removed: " + strings.Join(failed, "; ")
	}
	return fmt.Sprintf(" Removed %d candidate worktree(s).", len(ids))
}

// gitApplyPatch checks then applies a unified diff at the repository top
// level of cwd (child patches are repo-root relative).
func (s *Server) gitApplyPatch(ctx context.Context, cwd, patch string) error {
	s.gitMu.Lock()
	defer s.gitMu.Unlock()
	out, stderr, err := gitRun(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New(gitErr("rev-parse", err, stderr))
	}
	top := strings.TrimSpace(string(out))
	for _, check := range []bool{true, false} {
		args := []string{"-C", top, "apply", "--whitespace=nowarn"}
		if check {
			args = append(args, "--check")
		}
		args = append(args, "-")
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = gitEnv()
		cmd.Stdin = strings.NewReader(patch)
		var errBuf bytes.Buffer
		cmd.Stderr = &errBuf
		if err := cmd.Run(); err != nil {
			return errors.New(gitErr("apply", err, errBuf.String()))
		}
	}
	return nil
}

// ---- HTTP ----

type bestOfNCandidateBody struct {
	Model  string `json:"model"`
	Effort string `json:"effort,omitempty"`
}

type bestOfNBudgetDTO struct {
	MaxTurns   int `json:"maxTurns,omitempty"`
	MaxTokens  int `json:"maxTokens,omitempty"`
	TimeoutSec int `json:"timeoutSec,omitempty"`
}

type bestOfNStartBody struct {
	OwnerRoot  string                 `json:"ownerRoot"`
	Prompt     string                 `json:"prompt"`
	Candidates []bestOfNCandidateBody `json:"candidates"`
	Edits      bool                   `json:"edits"`
	Budget     bestOfNBudgetDTO       `json:"budget"`
}

type bestOfNCandidateDTO struct {
	Model        string    `json:"model"`
	Effort       string    `json:"effort,omitempty"`
	Label        string    `json:"label"`
	ChildID      string    `json:"childId,omitempty"`
	Status       string    `json:"status"`
	StartedAt    time.Time `json:"startedAt"`
	EndedAt      time.Time `json:"endedAt,omitzero"`
	ElapsedMs    int64     `json:"elapsedMs"`
	Summary      string    `json:"summary,omitempty"`
	FilesChanged int       `json:"filesChanged"`
	Added        int       `json:"added"`
	Removed      int       `json:"removed"`
	Tokens       int       `json:"tokens,omitempty"`
	CostUSD      float64   `json:"costUsd,omitempty"`
	Verified     *bool     `json:"verified,omitempty"`
	Error        string    `json:"error,omitempty"`
	HasPatch     bool      `json:"hasPatch"`
}

type bestOfNDecisionDTO struct {
	Keep     string   `json:"keep,omitempty"`
	Archived []string `json:"archived"`
}

type bestOfNRunDTO struct {
	ID             string                `json:"id"`
	OwnerRoot      string                `json:"ownerRoot"`
	Prompt         string                `json:"prompt"`
	Edits          bool                  `json:"edits"`
	PermissionMode string                `json:"permissionMode,omitempty"`
	CostMultiplier int                   `json:"costMultiplier"`
	Budget         bestOfNBudgetDTO      `json:"budget"`
	Candidates     []bestOfNCandidateDTO `json:"candidates"`
	Complete       bool                  `json:"complete"`
	Decision       *bestOfNDecisionDTO   `json:"decision,omitempty"`
	Note           string                `json:"note,omitempty"`
	CreatedAt      time.Time             `json:"createdAt"`
}

func (s *Server) bestOfNDTO(e *bestOfNEntry) bestOfNRunDTO {
	req := e.run.Request()
	now := time.Now()
	e.mu.Lock()
	note := e.note
	hasPatch := map[string]bool{}
	for id, p := range e.patches {
		hasPatch[id] = strings.TrimSpace(p) != ""
	}
	e.mu.Unlock()
	out := bestOfNRunDTO{
		ID:             e.id,
		OwnerRoot:      e.owner,
		Prompt:         req.Prompt,
		Edits:          req.Edits,
		PermissionMode: req.PermissionMode,
		CostMultiplier: req.CostMultiplier(),
		Budget: bestOfNBudgetDTO{
			MaxTurns:   req.Budget.MaxTurns,
			MaxTokens:  req.Budget.MaxTokens,
			TimeoutSec: int(req.Budget.Timeout / time.Second),
		},
		Complete:  e.run.Complete(),
		Note:      note,
		CreatedAt: e.created,
	}
	for _, cr := range e.run.Candidates() {
		end := cr.Ended
		if end.IsZero() {
			end = now
		}
		out.Candidates = append(out.Candidates, bestOfNCandidateDTO{
			Model:        cr.Candidate.Model,
			Effort:       cr.Candidate.Effort,
			Label:        cr.Candidate.Label(),
			ChildID:      cr.ChildID,
			Status:       string(cr.Status),
			StartedAt:    cr.Started,
			EndedAt:      cr.Ended,
			ElapsedMs:    end.Sub(cr.Started).Milliseconds(),
			Summary:      cr.Outcome.Summary,
			FilesChanged: cr.Outcome.FilesChanged,
			Added:        cr.Outcome.Added,
			Removed:      cr.Outcome.Removed,
			Tokens:       cr.Outcome.Tokens,
			CostUSD:      cr.Outcome.CostUSD,
			Verified:     cr.Outcome.Verified,
			Error:        cr.Outcome.Err,
			HasPatch:     hasPatch[cr.ChildID],
		})
	}
	if d, ok := e.run.Decision(); ok {
		archived := d.Archive
		if archived == nil {
			archived = []string{}
		}
		out.Decision = &bestOfNDecisionDTO{Keep: d.Keep, Archived: archived}
	}
	return out
}

// bestOfNSpawner returns the injected spawner (tests) or the live
// team.spawn seam for owner.
func (s *Server) bestOfNSpawner(owner, runID string) bestofn.Spawner {
	if s.opts.BestOfNSpawner != nil {
		return s.opts.BestOfNSpawner(owner)
	}
	return liveBestOfNSpawner{s: s, owner: owner, runID: runID}
}

// admitBestOfN gates mutating best-of-N routes like other reviewed
// mutations (read-only daemons and rate limits refuse).
func (s *Server) admitBestOfN(w http.ResponseWriter, r *http.Request, opType string) bool {
	sessionID, err := s.admitFileMutation(r, opType)
	if err == nil {
		s.auditOpOK(r, "http", opType, sessionID)
		return true
	}
	switch {
	case errors.Is(err, errOpsReadOnly):
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: err.Error(), Code: "read_only"})
	case errors.Is(err, errOpsRateLimited):
		writeJSON(w, http.StatusTooManyRequests, opErrorResponse{Error: err.Error()})
	default:
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: err.Error()})
	}
	return false
}

func (s *Server) handleBestOfNStart(w http.ResponseWriter, r *http.Request) {
	if !s.admitBestOfN(w, r, "bestofn.start") {
		return
	}
	var body bestOfNStartBody
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "validation: " + err.Error()})
		return
	}
	if body.Budget.MaxTurns < 0 || body.Budget.MaxTokens < 0 || body.Budget.TimeoutSec < 0 {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "validation: budget values must not be negative"})
		return
	}
	req := bestofn.Request{
		Prompt: body.Prompt,
		Edits:  body.Edits,
		Budget: bestofn.Budget{
			MaxTurns:  body.Budget.MaxTurns,
			MaxTokens: body.Budget.MaxTokens,
			Timeout:   time.Duration(body.Budget.TimeoutSec) * time.Second,
		},
	}
	for _, c := range body.Candidates {
		req.Candidates = append(req.Candidates, bestofn.Candidate{
			Model:  strings.TrimSpace(c.Model),
			Effort: strings.TrimSpace(c.Effort),
		})
	}
	if err := req.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "validation: " + err.Error()})
		return
	}
	owner := resolvePlanOwner(body.OwnerRoot, r)
	live := s.liveForOwner(owner)
	if owner == "" && live != nil {
		owner = live.SessionID()
	}
	if live == nil && s.opts.BestOfNSpawner == nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "owning session is not live", Code: "attach_only"})
		return
	}
	cwd := ""
	if live != nil {
		st := live.Status()
		cwd = st.CWD
		// Candidates run under the session's current mode; the engine never
		// widens it for children. Reported so the card can say so.
		req.PermissionMode = st.PermissionMode
	}
	// Subscribe before spawning so no completion is missed.
	watchCtx, cancel := context.WithTimeout(context.Background(), bestOfNWatchMax)
	var sub <-chan protocol.Event
	if live != nil {
		sub = live.Subscribe(watchCtx)
	}
	runID := newBestOfNID()
	run, err := bestofn.Start(r.Context(), s.bestOfNSpawner(owner, runID), req)
	if err != nil {
		cancel()
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "validation: " + err.Error()})
		return
	}
	e := s.registerBestOfN(runID, owner, cwd, run)
	if sub != nil {
		go s.watchBestOfN(watchCtx, cancel, e, sub, req.Budget.Timeout)
	} else {
		cancel()
	}
	writeJSON(w, http.StatusOK, s.bestOfNDTO(e))
}

func (s *Server) handleBestOfNList(w http.ResponseWriter, r *http.Request) {
	owner := rootParam(r)
	out := []bestOfNRunDTO{}
	for _, e := range s.bestOfN.list(owner) {
		s.reconcileBestOfN(e)
		out = append(out, s.bestOfNDTO(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

// bestOfNFor resolves {id} and enforces that a ?root= caller owns it.
func (s *Server) bestOfNFor(w http.ResponseWriter, r *http.Request) *bestOfNEntry {
	e := s.bestOfN.get(strings.TrimSpace(r.PathValue("id")))
	if e == nil {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "best-of-N run not found"})
		return nil
	}
	if root := rootParam(r); root != "" && root != e.owner {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "best-of-N run belongs to another session"})
		return nil
	}
	return e
}

func (s *Server) handleBestOfNGet(w http.ResponseWriter, r *http.Request) {
	e := s.bestOfNFor(w, r)
	if e == nil {
		return
	}
	s.reconcileBestOfN(e)
	writeJSON(w, http.StatusOK, s.bestOfNDTO(e))
}

func (s *Server) handleBestOfNCancel(w http.ResponseWriter, r *http.Request) {
	e := s.bestOfNFor(w, r)
	if e == nil || !s.admitBestOfN(w, r, "bestofn.cancel") {
		return
	}
	if err := e.run.CancelAll(); err != nil {
		e.mu.Lock()
		e.note = "Cancelled; some children did not confirm the interrupt: " + err.Error()
		e.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, s.bestOfNDTO(e))
}

func (s *Server) handleBestOfNAdopt(w http.ResponseWriter, r *http.Request) {
	e := s.bestOfNFor(w, r)
	if e == nil || !s.admitBestOfN(w, r, "bestofn.adopt") {
		return
	}
	var body struct {
		ChildID string `json:"childId"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "validation: " + err.Error()})
		return
	}
	if err := s.adoptBestOfN(r.Context(), e, strings.TrimSpace(body.ChildID)); err != nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.bestOfNDTO(e))
}

func (s *Server) handleBestOfNKeepNone(w http.ResponseWriter, r *http.Request) {
	e := s.bestOfNFor(w, r)
	if e == nil || !s.admitBestOfN(w, r, "bestofn.keep_none") {
		return
	}
	if err := s.keepNoneBestOfN(r.Context(), e); err != nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.bestOfNDTO(e))
}
