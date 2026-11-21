package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/persist/memory"
	"github.com/jonathanung/strike/internal/product/project"
	"github.com/jonathanung/strike/internal/trust/workspacetrust"
)

// Lessons loop routes (#1376 / #1874): pending lesson proposals and the
// per-session "lesson used" provenance. Proposals never reach a prompt until
// the operator approves them into a lesson-tagged memory entry.

func (s *Server) lessonsService(w http.ResponseWriter) host.MemoryLessons {
	mem := s.memoryService(w)
	if mem == nil {
		return nil
	}
	lessons, ok := mem.(host.MemoryLessons)
	if !ok {
		capabilityUnavailable(w, "lessons")
		return nil
	}
	return lessons
}

func (s *Server) handleLessonProposals(w http.ResponseWriter, r *http.Request) {
	lessons := s.lessonsService(w)
	if lessons == nil {
		return
	}
	list, err := lessons.Proposals()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	if list == nil {
		list = []host.MemoryProposal{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"proposals": list})
}

// handleLessonPropose records a pending proposal for the live session.
// Incognito and temporary sessions, and untrusted workspaces, are refused.
func (s *Server) handleLessonPropose(w http.ResponseWriter, r *http.Request) {
	lessons := s.lessonsService(w)
	if lessons == nil {
		return
	}
	if !s.requireMutable(w) {
		return
	}
	var body struct {
		Key     string `json:"key"`
		Value   string `json:"value"`
		Scope   string `json:"scope"`
		Session string `json:"session"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	session := strings.TrimSpace(body.Session)
	if live := s.resolveLive(nil, r); live != nil {
		// The proposing session is the live root's, never a body claim.
		session = live.SessionID()
		if mode, wired := live.MemoryMode(); wired && !memory.AllowsWrite(mode) {
			writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "memory mode " + mode + ": this session cannot propose lessons"})
			return
		}
	}
	if msg := s.lessonTrustRefusal(r); msg != "" {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: msg})
		return
	}
	p, err := lessons.Propose(host.MemoryProposal{Key: body.Key, Value: body.Value, Scope: body.Scope, Session: session})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// lessonTrustRefusal returns a refusal message when the workspace trust gate
// (#1352) is wired and this workspace is not trusted. The store is read
// fresh so a grant from `strike trust` counts immediately.
func (s *Server) lessonTrustRefusal(r *http.Request) string {
	if !s.workspaceTrustCapable() {
		return ""
	}
	const refused = "untrusted workspace: lessons cannot be proposed until the workspace is trusted"
	root := ""
	if a := s.opts.WorkspaceTrustAssembled; a != nil {
		root = strings.TrimSpace(a.Root)
	}
	if root == "" {
		workDir := strings.TrimSpace(s.currentCWD(r))
		if workDir == "" {
			workDir = strings.TrimSpace(s.opts.WorkDir)
		}
		if workDir == "" {
			return refused
		}
		id, err := project.Resolve(r.Context(), workDir)
		if err != nil {
			return refused
		}
		root = id.Root
	}
	store, err := workspacetrust.Open(s.opts.WorkspaceTrustPath)
	if err != nil || !store.Trusted(root) {
		return refused
	}
	return ""
}

func (s *Server) handleLessonProposalEdit(w http.ResponseWriter, r *http.Request) {
	lessons := s.lessonsService(w)
	if lessons == nil {
		return
	}
	if !s.requireMutable(w) {
		return
	}
	var body struct {
		Key   string `json:"key"`
		Value string `json:"value"`
		Scope string `json:"scope"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	p, err := lessons.EditProposal(r.PathValue("id"), body.Key, body.Value, body.Scope)
	if err != nil {
		writeLessonError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleLessonProposalApprove(w http.ResponseWriter, r *http.Request) {
	lessons := s.lessonsService(w)
	if lessons == nil {
		return
	}
	if !s.requireMutable(w) {
		return
	}
	var body struct {
		Replace bool `json:"replace"`
	}
	if r.ContentLength != 0 {
		if err := decodeBody(w, r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
			return
		}
	}
	entry, err := lessons.ApproveProposal(r.PathValue("id"), body.Replace)
	if err != nil {
		writeLessonError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) handleLessonProposalDismiss(w http.ResponseWriter, r *http.Request) {
	lessons := s.lessonsService(w)
	if lessons == nil {
		return
	}
	if !s.requireMutable(w) {
		return
	}
	if err := lessons.DismissProposal(r.PathValue("id")); err != nil {
		writeLessonError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSessionLessonsUsed lists the memory entries recorded as auto-loaded
// into a session: the "N lessons used" marker. Display-only.
func (s *Server) handleSessionLessonsUsed(w http.ResponseWriter, r *http.Request) {
	lessons := s.lessonsService(w)
	if lessons == nil {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "session id is required"})
		return
	}
	list, err := lessons.Injected(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	if list == nil {
		list = []host.MemoryInjection{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessionId": id, "entries": list})
}

func writeLessonError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, memory.ErrProposalNotFound):
		status = http.StatusNotFound
	case errors.Is(err, host.ErrMemoryKeyExists):
		status = http.StatusConflict
	}
	writeJSON(w, status, opErrorResponse{Error: err.Error()})
}
