package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/persist/plan"
	"github.com/jonathanung/strike/internal/persist/taskrun"
	"github.com/jonathanung/strike/internal/protocol"
)

// taskSpawn is an optional stand-in for team.spawn. Nil uses the live engine.
type taskSpawnFunc func(ctx context.Context, ownerRoot, name, objective string) (childID string, err error)

func (s *Server) taskRuns() *taskrun.Store {
	if s == nil {
		return nil
	}
	return s.opts.TaskRuns
}

type taskCheckDTO struct {
	Name   string `json:"name,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Passed bool   `json:"passed"`
	Error  string `json:"error,omitempty"`
}

type taskCheckpointDTO struct {
	SectionBody    string         `json:"sectionBody,omitempty"`
	ChildID        string         `json:"childId"`
	ChildStatus    string         `json:"childStatus,omitempty"`
	Summary        string         `json:"summary,omitempty"`
	FilesChanged   []string       `json:"filesChanged,omitempty"`
	HandoffText    string         `json:"handoffText,omitempty"`
	Findings       []string       `json:"findings,omitempty"`
	Blockers       []string       `json:"blockers,omitempty"`
	Incomplete     bool           `json:"incomplete,omitempty"`
	Quality        string         `json:"quality,omitempty"`
	HasVerify      bool           `json:"hasVerify,omitempty"`
	VerifyPassed   bool           `json:"verifyPassed"`
	VerifyClaimed  bool           `json:"verifyClaimed,omitempty"`
	VerifyVerified bool           `json:"verifyVerified,omitempty"`
	VerifySummary  string         `json:"verifySummary,omitempty"`
	Checks         []taskCheckDTO `json:"checks,omitempty"`
	RecordedAt     time.Time      `json:"recordedAt"`
}

type taskSectionDTO struct {
	ID         string             `json:"id"`
	Title      string             `json:"title"`
	Body       string             `json:"body,omitempty"`
	DependsOn  []string           `json:"dependsOn"`
	Status     string             `json:"status"`
	ChildID    string             `json:"childId,omitempty"`
	Detail     string             `json:"detail,omitempty"`
	Ready      bool               `json:"ready"`
	Checkpoint *taskCheckpointDTO `json:"checkpoint,omitempty"`
}

type taskRunDTO struct {
	PlanID     string           `json:"planId"`
	OwnerRoot  string           `json:"ownerRoot"`
	PlanTitle  string           `json:"planTitle"`
	PlanStatus string           `json:"planStatus"`
	Version    int              `json:"version"`
	Sections   []taskSectionDTO `json:"sections"`
	UpdatedAt  time.Time        `json:"updatedAt"`
}

type taskSpawnedDTO struct {
	SectionID      string `json:"sectionId"`
	ChildSessionID string `json:"childSessionId"`
}

type taskRunResponse struct {
	Run     taskRunDTO       `json:"run"`
	Spawned []taskSpawnedDTO `json:"spawned,omitempty"`
	Note    string           `json:"note,omitempty"`
}

func toTaskRunDTO(run taskrun.Run) taskRunDTO {
	ready := map[string]bool{}
	for _, id := range taskrun.ReadyIDs(run) {
		ready[id] = true
	}
	out := taskRunDTO{
		PlanID:     run.PlanID,
		OwnerRoot:  run.OwnerRoot,
		PlanTitle:  run.PlanTitle,
		PlanStatus: run.PlanStatus,
		Version:    run.Version,
		UpdatedAt:  run.UpdatedAt,
		Sections:   make([]taskSectionDTO, len(run.Sections)),
	}
	for i, sec := range run.Sections {
		deps := sec.DependsOn
		if deps == nil {
			deps = []string{}
		}
		row := taskSectionDTO{
			ID:        sec.ID,
			Title:     sec.Title,
			Body:      sec.Body,
			DependsOn: deps,
			Status:    sec.Status,
			ChildID:   sec.ChildID,
			Detail:    sec.Detail,
			Ready:     ready[sec.ID],
		}
		if sec.Checkpoint != nil {
			cp := sec.Checkpoint
			row.Checkpoint = &taskCheckpointDTO{
				SectionBody:    cp.SectionBody,
				ChildID:        cp.ChildID,
				ChildStatus:    cp.ChildStatus,
				Summary:        cp.Summary,
				FilesChanged:   cp.FilesChanged,
				HandoffText:    cp.HandoffText,
				Findings:       cp.Findings,
				Blockers:       cp.Blockers,
				Incomplete:     cp.Incomplete,
				Quality:        cp.Quality,
				HasVerify:      cp.HasVerify,
				VerifyPassed:   cp.VerifyPassed,
				VerifyClaimed:  cp.VerifyClaimed,
				VerifyVerified: cp.VerifyVerified,
				VerifySummary:  cp.VerifySummary,
				RecordedAt:     cp.RecordedAt,
			}
			for _, c := range cp.Checks {
				row.Checkpoint.Checks = append(row.Checkpoint.Checks, taskCheckDTO{
					Name: c.Name, Kind: c.Kind, Passed: c.Passed, Error: c.Error,
				})
			}
		}
		out.Sections[i] = row
	}
	return out
}

func (s *Server) handleTaskRunGet(w http.ResponseWriter, r *http.Request) {
	store := s.taskRuns()
	if store == nil {
		capabilityUnavailable(w, "taskRuns")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "plan id is required"})
		return
	}
	if _, ok, err := s.loadPlan(id); err != nil {
		writePlanError(w, err)
		return
	} else if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: plan.ErrNotFound.Error()})
		return
	}
	run, ok, err := store.Get(id)
	if err != nil {
		writeTaskRunError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "task run not opened"})
		return
	}
	run = s.reconcileTaskRun(r.Context(), run)
	writeJSON(w, http.StatusOK, taskRunResponse{Run: toTaskRunDTO(run)})
}

func (s *Server) handleTaskRunPost(w http.ResponseWriter, r *http.Request) {
	store := s.taskRuns()
	if store == nil {
		capabilityUnavailable(w, "taskRuns")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "plan id is required"})
		return
	}
	var body struct {
		Action          string   `json:"action"`
		OwnerRoot       string   `json:"ownerRoot"`
		SectionID       string   `json:"sectionId"`
		SectionIDs      []string `json:"sectionIds"`
		ExpectedVersion int      `json:"expectedVersion"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	p, ok, err := s.loadPlan(id)
	if err != nil {
		writePlanError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: plan.ErrNotFound.Error()})
		return
	}
	owner := resolvePlanOwner(body.OwnerRoot, r)
	if owner == "" {
		owner = p.OwnerRoot
	}
	if owner != p.OwnerRoot {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: taskrun.ErrNotOwner.Error()})
		return
	}
	action := strings.TrimSpace(body.Action)
	inputs := sectionInputs(p)
	switch action {
	case "open":
		run, err := store.Ensure(p.ID, p.OwnerRoot, p.Title, p.Status, inputs)
		if err != nil {
			writeTaskRunError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, taskRunResponse{Run: toTaskRunDTO(run)})
	case "start":
		if body.ExpectedVersion < 1 {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "expectedVersion is required"})
			return
		}
		run, spawned, note, err := s.startTaskRun(r.Context(), p, inputs, body.ExpectedVersion)
		if err != nil {
			writeTaskRunError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, taskRunResponse{Run: toTaskRunDTO(run), Spawned: spawned, Note: note})
	case "approve", "retry", "skip", "reorder":
		if body.ExpectedVersion < 1 {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "expectedVersion is required"})
			return
		}
		if _, err := store.Ensure(p.ID, p.OwnerRoot, p.Title, p.Status, inputs); err != nil {
			writeTaskRunError(w, err)
			return
		}
		var run taskrun.Run
		switch action {
		case "approve":
			run, err = store.Approve(p.ID, body.SectionID, body.ExpectedVersion)
		case "retry":
			run, err = store.Retry(p.ID, body.SectionID, body.ExpectedVersion)
		case "skip":
			run, err = store.Skip(p.ID, body.SectionID, body.ExpectedVersion)
		case "reorder":
			run, err = store.Reorder(p.ID, body.SectionIDs, body.ExpectedVersion)
		}
		if err != nil {
			writeTaskRunError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, taskRunResponse{Run: toTaskRunDTO(run)})
	default:
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "action must be open, start, approve, retry, skip, or reorder"})
	}
}

func (s *Server) loadPlan(id string) (host.Plan, bool, error) {
	svc := s.plansService()
	if svc == nil {
		return host.Plan{}, false, fmt.Errorf("plans capability unavailable")
	}
	return svc.Get(id)
}

func sectionInputs(p host.Plan) []taskrun.SectionInput {
	out := make([]taskrun.SectionInput, len(p.Sections))
	for i, sec := range p.Sections {
		out[i] = taskrun.SectionInput{ID: sec.ID, Title: sec.Title, Body: sec.Body}
	}
	return out
}

func (s *Server) startTaskRun(ctx context.Context, p host.Plan, inputs []taskrun.SectionInput, expected int) (taskrun.Run, []taskSpawnedDTO, string, error) {
	store := s.taskRuns()
	run, err := store.Ensure(p.ID, p.OwnerRoot, p.Title, p.Status, inputs)
	if err != nil {
		return taskrun.Run{}, nil, "", err
	}
	if run.Version != expected {
		return taskrun.Run{}, nil, "", taskrun.ErrConflict
	}
	if p.Status == plan.StatusClosed {
		return taskrun.Run{}, nil, "", taskrun.ErrClosed
	}
	run = s.reconcileTaskRun(ctx, run)
	var spawned []taskSpawnedDTO
	for {
		ready := taskrun.ReadyIDs(run)
		if len(ready) == 0 {
			break
		}
		sectionID := ready[0]
		claimed, err := store.ClaimOne(p.ID, sectionID)
		if err != nil {
			if errors.Is(err, taskrun.ErrInvalid) {
				run, _, _ = store.Get(p.ID)
				break
			}
			return taskrun.Run{}, spawned, "", err
		}
		run = claimed
		sec := taskSection(run, sectionID)
		childID, err := s.spawnTaskChild(ctx, p.OwnerRoot, taskrun.SectionName(sec), taskrun.SectionObjective(p.Title, sec), fmt.Sprintf("taskrun-%s-%s-%d", p.ID, sectionID, run.Version))
		if err != nil {
			if released, rerr := store.Release(p.ID, sectionID); rerr == nil {
				run = released
			}
			return run, spawned, "", err
		}
		bound, err := store.BindChild(p.ID, sectionID, childID)
		if err != nil {
			return run, spawned, "", err
		}
		run = bound
		spawned = append(spawned, taskSpawnedDTO{SectionID: sectionID, ChildSessionID: childID})
		if s.opts.TaskSpawn == nil {
			go s.watchTaskChild(p.OwnerRoot, p.ID, sectionID, childID)
		}
	}
	note := ""
	if len(spawned) == 0 {
		note = "Nothing started. Approve or skip the checkpoint in review. A failed section blocks the ones that depend on it."
	}
	return run, spawned, note, nil
}

func taskSection(run taskrun.Run, id string) taskrun.Section {
	for _, sec := range run.Sections {
		if sec.ID == id {
			return sec
		}
	}
	return taskrun.Section{ID: id}
}

func (s *Server) spawnTaskChild(ctx context.Context, owner, name, objective, idem string) (string, error) {
	if s.opts.TaskSpawn != nil {
		return s.opts.TaskSpawn(ctx, owner, name, objective)
	}
	live := s.liveForOwner(owner)
	if live == nil {
		return "", fmt.Errorf("owning session is not live")
	}
	reply := make(chan protocol.TeamOpOutcome, 1)
	op := protocol.TeamSpawn{
		Objective: objective,
		Name:      name,
		Reply:     reply,
	}
	// IdempotencyKey and RootSessionID are promoted from an unexported
	// embedded struct, so they are set by selector rather than a literal.
	op.IdempotencyKey = idem
	op.RootSessionID = owner
	submitCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := live.Submit(submitCtx, op); err != nil {
		return "", err
	}
	select {
	case out := <-reply:
		if !out.OK {
			msg := out.Error
			if msg == "" {
				msg = out.Code
			}
			return "", errors.New(msg)
		}
		if strings.TrimSpace(out.ChildSessionID) == "" {
			return "", errors.New("team.spawn returned no child")
		}
		return out.ChildSessionID, nil
	case <-submitCtx.Done():
		return "", fmt.Errorf("team.spawn timed out")
	}
}

func (s *Server) liveForOwner(owner string) *Live {
	owner = strings.TrimSpace(owner)
	if s.opts.LiveHub != nil {
		return s.opts.LiveHub.LiveFor(owner)
	}
	if s.opts.Live != nil && (owner == "" || s.opts.Live.SessionID() == owner) {
		return s.opts.Live
	}
	return nil
}

func (s *Server) watchTaskChild(owner, planID, sectionID, childID string) {
	live := s.liveForOwner(owner)
	if live == nil || s.taskRuns() == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	sub := live.Subscribe(ctx)
	for ev := range sub {
		cc, ok := ev.(protocol.ChildCompleted)
		if !ok || cc.SessionID != childID {
			continue
		}
		_, _ = s.taskRuns().ApplyChild(planID, sectionID, outcomeFromChild(cc))
		return
	}
}

func (s *Server) reconcileTaskRun(ctx context.Context, run taskrun.Run) taskrun.Run {
	if ctx != nil && ctx.Err() != nil {
		return run
	}
	if s.opts.Services == nil || s.opts.Services.Sessions == nil || s.taskRuns() == nil {
		return run
	}
	raw, err := s.opts.Services.Sessions.ReplayJSONL(run.OwnerRoot)
	if err != nil || len(raw) == 0 {
		return run
	}
	evs, err := decodeSessionJSONL(raw)
	if err != nil {
		return run
	}
	latest := map[string]protocol.ChildCompleted{}
	for _, ev := range evs {
		cc, ok := ev.(protocol.ChildCompleted)
		if !ok || strings.TrimSpace(cc.SessionID) == "" {
			continue
		}
		latest[cc.SessionID] = cc
	}
	for _, sec := range run.Sections {
		if sec.Status != taskrun.StatusRunning || sec.ChildID == "" {
			continue
		}
		cc, ok := latest[sec.ChildID]
		if !ok {
			continue
		}
		updated, err := s.taskRuns().ApplyChild(run.PlanID, sec.ID, outcomeFromChild(cc))
		if err != nil {
			continue
		}
		run = updated
	}
	return run
}

func outcomeFromChild(cc protocol.ChildCompleted) taskrun.Outcome {
	o := taskrun.Outcome{
		ChildID:      cc.SessionID,
		ChildStatus:  string(cc.Status),
		Summary:      cc.Handoff.Summary,
		FilesChanged: cc.Handoff.FilesChanged,
		HandoffText:  cc.Handoff.Verification,
		Findings:     cc.Handoff.Findings,
		Blockers:     cc.Handoff.Blockers,
		Incomplete:   cc.Handoff.Incomplete,
		Quality:      cc.Handoff.Quality,
	}
	if strings.TrimSpace(o.Summary) == "" {
		o.Summary = cc.Summary
	}
	if cc.Verification != nil {
		o.HasVerify = true
		o.VerifyPassed = cc.Verification.Passed
		o.VerifyClaimed = cc.Verification.Claimed
		o.VerifyVerified = cc.Verification.Verified
		o.VerifySummary = cc.Verification.Summary
		for _, c := range cc.Verification.Checks {
			o.Checks = append(o.Checks, taskrun.Check{
				Name: c.Name, Kind: c.Kind, Passed: c.Passed, Error: c.Error,
			})
		}
	}
	return o
}

func writeTaskRunError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, taskrun.ErrNotFound), errors.Is(err, plan.ErrNotFound):
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: err.Error()})
	case errors.Is(err, taskrun.ErrNotOwner), errors.Is(err, plan.ErrNotOwner):
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: err.Error()})
	case errors.Is(err, taskrun.ErrConflict):
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
	case errors.Is(err, taskrun.ErrClosed), errors.Is(err, taskrun.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
	default:
		msg := err.Error()
		if strings.HasPrefix(msg, "taskrun:") || strings.HasPrefix(msg, "plan:") {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: msg})
			return
		}
		if strings.Contains(msg, "not live") || strings.Contains(msg, "team") {
			writeJSON(w, http.StatusConflict, opErrorResponse{Error: msg})
			return
		}
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: msg})
	}
}
