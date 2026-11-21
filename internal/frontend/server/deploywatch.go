package server

import (
	"errors"
	"net/http"

	"github.com/jonathanung/strike/internal/daemon/deploywatch"
)

// Deploy watch REST (#1782, slice 1 of #1602). Every route needs the bearer;
// mutating routes are refused on a read-only server.

func (s *Server) deployWatches() *deploywatch.Service {
	if s == nil {
		return nil
	}
	return s.opts.DeployWatches
}

func (s *Server) requireDeployWatches(w http.ResponseWriter) *deploywatch.Service {
	svc := s.deployWatches()
	if svc == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "deploy watches unavailable"})
	}
	return svc
}

func (s *Server) handleDeployWatchesList(w http.ResponseWriter, r *http.Request) {
	svc := s.requireDeployWatches(w)
	if svc == nil {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Watches []deploywatch.Watch `json:"watches"`
	}{Watches: svc.List()})
}

func (s *Server) handleDeployWatchCreate(w http.ResponseWriter, r *http.Request) {
	svc := s.requireDeployWatches(w)
	if svc == nil || !s.allowTriggerWrite(w) {
		return
	}
	var in deploywatch.Input
	if err := decodeBody(w, r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	watch, err := svc.Create(in)
	if err != nil {
		writeJSON(w, deployWatchStatus(err), opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, watch)
}

func (s *Server) handleDeployWatchGet(w http.ResponseWriter, r *http.Request) {
	svc := s.requireDeployWatches(w)
	if svc == nil {
		return
	}
	watch, err := svc.Get(r.PathValue("id"))
	if err != nil {
		writeJSON(w, deployWatchStatus(err), opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, watch)
}

func (s *Server) handleDeployWatchPoll(w http.ResponseWriter, r *http.Request) {
	svc := s.requireDeployWatches(w)
	if svc == nil || !s.allowTriggerWrite(w) {
		return
	}
	watch, err := svc.Poll(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSON(w, deployWatchStatus(err), opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, watch)
}

func (s *Server) handleDeployWatchCancel(w http.ResponseWriter, r *http.Request) {
	svc := s.requireDeployWatches(w)
	if svc == nil || !s.allowTriggerWrite(w) {
		return
	}
	watch, err := svc.Cancel(r.PathValue("id"))
	if err != nil {
		writeJSON(w, deployWatchStatus(err), opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, watch)
}

// handoffConfirm is the body of a regression handoff (#1784). Confirm must
// be true: the cockpit sends it only after the user confirms the action.
type handoffConfirm struct {
	Confirm        bool   `json:"confirm"`
	PermissionMode string `json:"permissionMode,omitempty"`
}

func (s *Server) decodeHandoff(w http.ResponseWriter, r *http.Request) (handoffConfirm, bool) {
	var body handoffConfirm
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return body, false
	}
	if !body.Confirm {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "confirmation required: send {\"confirm\": true}"})
		return body, false
	}
	return body, true
}

func (s *Server) handleDeployWatchFixSession(w http.ResponseWriter, r *http.Request) {
	svc := s.requireDeployWatches(w)
	if svc == nil || !s.allowTriggerWrite(w) {
		return
	}
	body, ok := s.decodeHandoff(w, r)
	if !ok {
		return
	}
	watch, err := svc.OpenFixSession(r.Context(), r.PathValue("id"), body.PermissionMode)
	if err != nil {
		writeJSON(w, deployWatchStatus(err), opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, watch)
}

func (s *Server) handleDeployWatchRevertPR(w http.ResponseWriter, r *http.Request) {
	svc := s.requireDeployWatches(w)
	if svc == nil || !s.allowTriggerWrite(w) {
		return
	}
	body, ok := s.decodeHandoff(w, r)
	if !ok {
		return
	}
	if body.PermissionMode != "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "permissionMode applies to fix-session only"})
		return
	}
	watch, err := svc.DraftRevertPR(r.Context(), r.PathValue("id"))
	if err != nil {
		writeJSON(w, deployWatchStatus(err), opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, watch)
}

func deployWatchStatus(err error) int {
	switch {
	case errors.Is(err, deploywatch.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, deploywatch.ErrNotRegression):
		return http.StatusConflict
	case errors.Is(err, deploywatch.ErrUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, deploywatch.ErrInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
