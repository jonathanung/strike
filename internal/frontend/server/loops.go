package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jonathanung/strike/internal/schedule/sessionloop"
	"github.com/jonathanung/strike/pkg/loopspec"
)

// Session-scoped /loop routes (#1588). A loop re-injects a prompt into one
// live session on an interval until stopped or the session ends; it is not a
// Schedule (persisted, starts new sessions) and not a Goal.

type sessionLoopsResponse struct {
	Loops         []sessionloop.Loop `json:"loops"`
	Max           int                `json:"max"`
	MinIntervalMs int64              `json:"minIntervalMs"`
	MaxIntervalMs int64              `json:"maxIntervalMs"`
}

type sessionLoopStartRequest struct {
	// Interval uses the TUI /loop grammar: Go duration or bare minutes.
	Interval string `json:"interval"`
	Job      string `json:"job"`
}

type sessionLoopStartResponse struct {
	Loop sessionloop.Loop `json:"loop"`
}

type sessionLoopStopResponse struct {
	OK      bool `json:"ok"`
	Stopped int  `json:"stopped"`
}

const (
	opLoopStart = "loop.start"
	opLoopStop  = "loop.stop"
)

// sessionLoops resolves {id} to its live registry, writing 400/404 and
// returning nil when the session id is bad or the session is not live.
func (s *Server) sessionLoops(w http.ResponseWriter, r *http.Request) (*sessionloop.Registry, string) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := validateSessionID(id); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return nil, ""
	}
	var live *Live
	if s.opts.LiveHub != nil {
		live = s.opts.LiveHub.LiveForSession(id)
	} else if s.opts.Live != nil && s.opts.Live.SessionID() == id {
		live = s.opts.Live
	}
	reg := live.Loops()
	if reg == nil {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "session not live; loops exist only on a running session", Code: "session_not_live"})
		return nil, ""
	}
	return reg, id
}

func (s *Server) handleSessionLoopsList(w http.ResponseWriter, r *http.Request) {
	reg, _ := s.sessionLoops(w, r)
	if reg == nil {
		return
	}
	writeJSON(w, http.StatusOK, sessionLoopsResponse{
		Loops:         reg.List(),
		Max:           loopspec.MaxPerSession,
		MinIntervalMs: loopspec.MinInterval.Milliseconds(),
		MaxIntervalMs: loopspec.MaxInterval.Milliseconds(),
	})
}

func (s *Server) handleSessionLoopStart(w http.ResponseWriter, r *http.Request) {
	reg, id := s.sessionLoops(w, r)
	if reg == nil {
		return
	}
	if !s.admitLoopOp(w, r, opLoopStart, id) {
		return
	}
	var body sessionLoopStartRequest
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid loop request: " + err.Error()})
		return
	}
	loop, err := reg.Start(body.Interval, body.Job)
	if err != nil {
		s.recordServeOpError(r, "http", opLoopStart, id)
		writeJSON(w, loopErrorStatus(err), opErrorResponse{Error: err.Error()})
		return
	}
	s.auditOpOK(r, "http", opLoopStart, id)
	writeJSON(w, http.StatusCreated, sessionLoopStartResponse{Loop: loop})
}

func (s *Server) handleSessionLoopStop(w http.ResponseWriter, r *http.Request) {
	reg, id := s.sessionLoops(w, r)
	if reg == nil {
		return
	}
	if !s.admitLoopOp(w, r, opLoopStop, id) {
		return
	}
	if err := reg.Stop(strings.TrimSpace(r.PathValue("loopID"))); err != nil {
		s.recordServeOpError(r, "http", opLoopStop, id)
		writeJSON(w, loopErrorStatus(err), opErrorResponse{Error: err.Error()})
		return
	}
	s.auditOpOK(r, "http", opLoopStop, id)
	writeJSON(w, http.StatusOK, sessionLoopStopResponse{OK: true, Stopped: 1})
}

func (s *Server) handleSessionLoopsStopAll(w http.ResponseWriter, r *http.Request) {
	reg, id := s.sessionLoops(w, r)
	if reg == nil {
		return
	}
	if !s.admitLoopOp(w, r, opLoopStop, id) {
		return
	}
	n := reg.StopAll()
	s.auditOpOK(r, "http", opLoopStop, id)
	writeJSON(w, http.StatusOK, sessionLoopStopResponse{OK: true, Stopped: n})
}

// admitLoopOp applies the same --read-only and rate-limit gate as POST
// /v1/ops: a loop start is a deferred user.input.
func (s *Server) admitLoopOp(w http.ResponseWriter, r *http.Request, opType, sessionID string) bool {
	err := s.admitOp(r, "http", opType, sessionID)
	switch {
	case err == nil:
		return true
	case errors.Is(err, errOpsReadOnly):
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: err.Error()})
	case errors.Is(err, errOpsRateLimited):
		writeJSON(w, http.StatusTooManyRequests, opErrorResponse{Error: err.Error()})
	default:
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: err.Error()})
	}
	return false
}

func loopErrorStatus(err error) int {
	switch {
	case errors.Is(err, sessionloop.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, sessionloop.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, sessionloop.ErrLimit):
		return http.StatusConflict
	case errors.Is(err, sessionloop.ErrClosed):
		return http.StatusGone
	default:
		return http.StatusInternalServerError
	}
}
