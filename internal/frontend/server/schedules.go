package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/schedule"
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
	NextFire       string `json:"nextFire,omitempty"`
	LastFire       string `json:"lastFire,omitempty"`
	LastStatus     string `json:"lastStatus,omitempty"`
	LastSessionID  string `json:"lastSessionId,omitempty"`
	LastError      string `json:"lastError,omitempty"`
	CreatedAt      string `json:"createdAt,omitempty"`
	UpdatedAt      string `json:"updatedAt,omitempty"`
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
	}
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
	sch, err := svc.Create(spec)
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
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
	sch, err := svc.Replace(strings.TrimSpace(r.PathValue("id")), spec)
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toScheduleDTO(sch))
}

func (s *Server) handleScheduleDelete(w http.ResponseWriter, r *http.Request) {
	svc := s.requireSchedules(w)
	if svc == nil || s.rejectScheduleWrite(w) {
		return
	}
	if err := svc.Delete(strings.TrimSpace(r.PathValue("id"))); err != nil {
		writeScheduleErr(w, err)
		return
	}
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
