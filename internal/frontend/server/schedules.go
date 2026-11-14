package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/schedule"
	"github.com/jonathanung/strike/internal/trust/audit"
	"github.com/jonathanung/strike/pkg/redact"
)

type scheduleDTO struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	Kind           string `json:"kind"`
	Cron           string `json:"cron,omitempty"`
	Every          string `json:"every,omitempty"`
	At             string `json:"at,omitempty"`
	Timezone       string `json:"timezone"`
	Source         string `json:"source"`
	Prompt         string `json:"prompt,omitempty"`
	GoalID         string `json:"goalId,omitempty"`
	Workflow       string `json:"workflow,omitempty"`
	WorkspaceID    string `json:"workspaceId,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
	Overlap        string `json:"overlap"`
	Missed         string `json:"missed"`
	// Script-only (docs/design/script-schedules.md). Command is scrubbed on
	// this read-side projection; the store keeps it verbatim.
	Command       string   `json:"command,omitempty"`
	Dir           string   `json:"dir,omitempty"`
	Timeout       string   `json:"timeout,omitempty"`
	SecretRefs    []string `json:"secretRefs,omitempty"`
	ArmedAt       string   `json:"armedAt,omitempty"`
	CommandHash   string   `json:"commandHash,omitempty"`
	NextFire      string   `json:"nextFire,omitempty"`
	LastFire      string   `json:"lastFire,omitempty"`
	LastStatus    string   `json:"lastStatus,omitempty"`
	LastSessionID string   `json:"lastSessionId,omitempty"`
	LastError     string   `json:"lastError,omitempty"`
	CreatedAt     string   `json:"createdAt,omitempty"`
	UpdatedAt     string   `json:"updatedAt,omitempty"`
}

type runDTO struct {
	ID             string `json:"id"`
	ScheduleID     string `json:"scheduleId"`
	FiredAt        string `json:"firedAt"`
	ScheduledFor   string `json:"scheduledFor,omitempty"`
	Status         string `json:"status"`
	SessionID      string `json:"sessionId,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
	Error          string `json:"error,omitempty"`
	// Script-run fields; empty for session runs.
	StartedAt     string `json:"startedAt,omitempty"`
	EndedAt       string `json:"endedAt,omitempty"`
	ExitCode      *int   `json:"exitCode,omitempty"`
	OutputPreview string `json:"outputPreview,omitempty"`
	OutputRef     string `json:"outputRef,omitempty"`
	Decision      string `json:"decision,omitempty"`
}

type scheduleSpecDTO struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Cron           string `json:"cron"`
	Every          string `json:"every"`
	At             string `json:"at"`
	Timezone       string `json:"timezone"`
	Source         string `json:"source"`
	Prompt         string `json:"prompt"`
	GoalID         string `json:"goalId"`
	Workflow       string `json:"workflow"`
	WorkspaceID    string `json:"workspaceId"`
	PermissionMode string `json:"permissionMode"`
	Overlap        string `json:"overlap"`
	Missed         string `json:"missed"`
	Enabled        *bool  `json:"enabled"`
	// Script-only payload. armedAt and commandHash are never accepted from
	// the client: the server stamps them when Confirm is set.
	Command    string   `json:"command"`
	Dir        string   `json:"dir"`
	Timeout    string   `json:"timeout"`
	SecretRefs []string `json:"secretRefs"`
	// Confirm is the cockpit's explicit arming step; required for every
	// script create or replace (the same gate as adding an allow rule).
	Confirm bool `json:"confirm"`
}

func (s *Server) schedules() *schedule.Service {
	if s == nil {
		return nil
	}
	return s.opts.Schedules
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func toScheduleDTO(sch schedule.Schedule) scheduleDTO {
	return scheduleDTO{
		ID:             sch.ID,
		Name:           sch.Name,
		Enabled:        sch.Enabled,
		Kind:           sch.Kind,
		Cron:           sch.Cron,
		Every:          sch.Every,
		At:             rfc3339(sch.At),
		Timezone:       sch.Timezone,
		Source:         sch.Source,
		Prompt:         sch.Prompt,
		GoalID:         sch.GoalID,
		Workflow:       sch.Workflow,
		WorkspaceID:    sch.WorkspaceID,
		PermissionMode: sch.PermissionMode,
		Overlap:        sch.Overlap,
		Missed:         sch.Missed,
		Command:        scrubCommand(sch.Command),
		Dir:            sch.Dir,
		Timeout:        sch.Timeout,
		SecretRefs:     append([]string(nil), sch.SecretRefs...),
		ArmedAt:        rfc3339(sch.ArmedAt),
		CommandHash:    sch.CommandHash,
		NextFire:       rfc3339(sch.NextFire),
		LastFire:       rfc3339(sch.LastFire),
		LastStatus:     sch.LastStatus,
		LastSessionID:  sch.LastSessionID,
		LastError:      sch.LastError,
		CreatedAt:      rfc3339(sch.CreatedAt),
		UpdatedAt:      rfc3339(sch.UpdatedAt),
	}
}

func toRunDTO(run schedule.Run) runDTO {
	return runDTO{
		ID:             run.ID,
		ScheduleID:     run.ScheduleID,
		FiredAt:        rfc3339(run.FiredAt),
		ScheduledFor:   rfc3339(run.ScheduledFor),
		Status:         run.Status,
		SessionID:      run.SessionID,
		PermissionMode: run.PermissionMode,
		Error:          run.Error,
		StartedAt:      rfc3339(run.StartedAt),
		EndedAt:        rfc3339(run.EndedAt),
		ExitCode:       run.ExitCode,
		OutputPreview:  run.OutputPreview,
		OutputRef:      run.OutputRef,
		Decision:       run.Decision,
	}
}

func scrubCommand(cmd string) string {
	if cmd == "" {
		return ""
	}
	return redact.ScrubToolOutput(cmd)
}

func isScriptSource(source string) bool {
	return strings.EqualFold(strings.TrimSpace(source), schedule.SourceScript)
}

// errConfirmRequired is the 400 body for a script write without confirm.
const errConfirmRequired = "confirm required"

// Script lifecycle statuses written to the script_run audit family.
const (
	scriptLifecycleArmed   = "armed"
	scriptLifecycleRearmed = "rearmed"
	scriptLifecyclePaused  = "paused"
	scriptLifecycleResumed = "resumed"
	scriptLifecycleDeleted = "deleted"
)

// auditScriptLifecycle writes one script_run lifecycle record for a script
// schedule; other sources write nothing. A failed audit write does not undo
// the mutation that already happened.
func (s *Server) auditScriptLifecycle(r *http.Request, sch schedule.Schedule, status string) {
	if sch.Source != schedule.SourceScript || s.opts.Audit == nil {
		return
	}
	_ = s.opts.Audit.Record(audit.FamilyScriptRun, "", "", "", "", audit.ScriptRunPayload{
		ScheduleID:  sch.ID,
		Status:      status,
		Mode:        sch.PermissionMode,
		Command:     sch.Command,
		CommandHash: sch.CommandHash,
		Dir:         sch.Dir,
		Timeout:     sch.Timeout,
		ClientIP:    s.clientIPString(r),
	})
}

func (s *Server) requireSchedules(w http.ResponseWriter) *schedule.Service {
	svc := s.schedules()
	if svc == nil {
		capabilityUnavailable(w, "schedules")
		return nil
	}
	return svc
}

func (s *Server) rejectScheduleWrite(w http.ResponseWriter) bool {
	if s.opts.ReadOnly {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "server is read-only"})
		return true
	}
	return false
}

func writeScheduleErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, schedule.ErrNotFound):
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: err.Error()})
	case errors.Is(err, schedule.ErrBusy):
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
	case errors.Is(err, schedule.ErrUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: err.Error()})
	case errors.Is(err, schedule.ErrInvalid), errors.Is(err, schedule.ErrLimit):
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
	default:
		writeJSON(w, http.StatusBadGateway, opErrorResponse{Error: err.Error()})
	}
}

func specFromDTO(body scheduleSpecDTO) (schedule.Spec, error) {
	spec := schedule.Spec{
		Name:           body.Name,
		Kind:           body.Kind,
		Cron:           body.Cron,
		Every:          body.Every,
		Timezone:       body.Timezone,
		Source:         body.Source,
		Prompt:         body.Prompt,
		GoalID:         body.GoalID,
		Workflow:       body.Workflow,
		WorkspaceID:    body.WorkspaceID,
		PermissionMode: body.PermissionMode,
		Overlap:        body.Overlap,
		Missed:         body.Missed,
		Enabled:        body.Enabled,
		Command:        body.Command,
		Dir:            body.Dir,
		Timeout:        body.Timeout,
		SecretRefs:     append([]string(nil), body.SecretRefs...),
	}
	if strings.TrimSpace(body.At) != "" {
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(body.At))
		if err != nil {
			return schedule.Spec{}, err
		}
		spec.At = at
	}
	return spec, nil
}

func (s *Server) handleSchedulesList(w http.ResponseWriter, r *http.Request) {
	svc := s.requireSchedules(w)
	if svc == nil {
		return
	}
	list := svc.List()
	out := make([]scheduleDTO, len(list))
	for i, sch := range list {
		out[i] = toScheduleDTO(sch)
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": out})
}

func (s *Server) handleScheduleCreate(w http.ResponseWriter, r *http.Request) {
	svc := s.requireSchedules(w)
	if svc == nil || s.rejectScheduleWrite(w) {
		return
	}
	var body scheduleSpecDTO
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	spec, err := specFromDTO(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "at must be RFC3339"})
		return
	}
	if isScriptSource(body.Source) {
		if !body.Confirm {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: errConfirmRequired})
			return
		}
		spec.ArmedAt = time.Now().UTC()
	}
	sch, err := svc.Create(spec)
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	s.auditScriptLifecycle(r, sch, scriptLifecycleArmed)
	writeJSON(w, http.StatusCreated, toScheduleDTO(sch))
}

func (s *Server) handleScheduleGet(w http.ResponseWriter, r *http.Request) {
	svc := s.requireSchedules(w)
	if svc == nil {
		return
	}
	sch, ok := svc.Get(strings.TrimSpace(r.PathValue("id")))
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "schedule not found"})
		return
	}
	writeJSON(w, http.StatusOK, toScheduleDTO(sch))
}

func (s *Server) handleScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	svc := s.requireSchedules(w)
	if svc == nil || s.rejectScheduleWrite(w) {
		return
	}
	var body scheduleSpecDTO
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	spec, err := specFromDTO(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "at must be RFC3339"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	cur, ok := svc.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "schedule not found"})
		return
	}
	wantScript, isScript := isScriptSource(body.Source), cur.Source == schedule.SourceScript
	if wantScript != isScript {
		// Never a silent conversion: delete and recreate instead.
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "source cannot change to or from script; delete and recreate the schedule"})
		return
	}
	if isScript {
		if !body.Confirm {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: errConfirmRequired})
			return
		}
		spec.ArmedAt = time.Now().UTC()
	}
	sch, err := svc.Replace(id, spec)
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	s.auditScriptLifecycle(r, sch, scriptLifecycleRearmed)
	writeJSON(w, http.StatusOK, toScheduleDTO(sch))
}

func (s *Server) handleScheduleDelete(w http.ResponseWriter, r *http.Request) {
	svc := s.requireSchedules(w)
	if svc == nil || s.rejectScheduleWrite(w) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	// Snapshot before delete so the lifecycle record carries the command
	// and hash of what was removed.
	prev, _ := svc.Get(id)
	if err := svc.Delete(id); err != nil {
		writeScheduleErr(w, err)
		return
	}
	s.auditScriptLifecycle(r, prev, scriptLifecycleDeleted)
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleSchedulePause(w http.ResponseWriter, r *http.Request) {
	s.handleScheduleEnabled(w, r, false)
}

func (s *Server) handleScheduleResume(w http.ResponseWriter, r *http.Request) {
	s.handleScheduleEnabled(w, r, true)
}

func (s *Server) handleScheduleEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	svc := s.requireSchedules(w)
	if svc == nil || s.rejectScheduleWrite(w) {
		return
	}
	sch, err := svc.SetEnabled(strings.TrimSpace(r.PathValue("id")), enabled)
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	status := scriptLifecyclePaused
	if enabled {
		status = scriptLifecycleResumed
	}
	s.auditScriptLifecycle(r, sch, status)
	writeJSON(w, http.StatusOK, toScheduleDTO(sch))
}

func (s *Server) handleScheduleRun(w http.ResponseWriter, r *http.Request) {
	svc := s.requireSchedules(w)
	if svc == nil || s.rejectScheduleWrite(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	run, err := svc.RunNow(ctx, strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRunDTO(run))
}

func (s *Server) handleScheduleHistory(w http.ResponseWriter, r *http.Request) {
	svc := s.requireSchedules(w)
	if svc == nil {
		return
	}
	runs, err := svc.History(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	out := make([]runDTO, len(runs))
	for i, run := range runs {
		out[i] = toRunDTO(run)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

// handleScheduleRunOutput serves a script run's scrubbed sidecar tail as
// text/plain. It is history, like /history: read-only and attach-only
// servers may read it. 404 when the schedule, the run, or the sidecar is
// missing.
func (s *Server) handleScheduleRunOutput(w http.ResponseWriter, r *http.Request) {
	svc := s.requireSchedules(w)
	if svc == nil {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	runID := strings.TrimSpace(r.PathValue("runId"))
	runs, err := svc.History(id)
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	// Only ids the store generated reach the sidecar path.
	known := false
	for _, run := range runs {
		if run.ID == runID {
			known = true
			break
		}
	}
	if !known {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "run not found"})
		return
	}
	data, err := svc.RunOutput(id, runID)
	switch {
	case errors.Is(err, schedule.ErrNotFound), errors.Is(err, schedule.ErrNoOutput):
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "no run output"})
		return
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "read run output failed"})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
