package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jonathanung/strike-cli/internal/persist/workspace"
)

type workspacesResponse struct {
	Workspaces []workspace.Workspace `json:"workspaces"`
	ActiveID   string                `json:"activeId,omitempty"`
}

func (s *Server) requireWorkspaces(w http.ResponseWriter) bool {
	if s.opts.Workspaces != nil {
		return true
	}
	writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "workspace registry unavailable"})
	return false
}

func (s *Server) handleWorkspacesList(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkspaces(w) {
		return
	}
	list, active := s.opts.Workspaces.List()
	writeJSON(w, http.StatusOK, workspacesResponse{Workspaces: list, ActiveID: active})
}

type workspaceAddBody struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

func (s *Server) handleWorkspaceAdd(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkspaces(w) {
		return
	}
	var body workspaceAddBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Path) == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "path is required"})
		return
	}
	ws, err := s.opts.Workspaces.Add(body.Path, body.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, ws)
}

func (s *Server) handleWorkspaceActivate(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkspaces(w) {
		return
	}
	ws, err := s.opts.Workspaces.Activate(strings.TrimSpace(r.PathValue("id")))
	if err != nil {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (s *Server) handleWorkspaceRemove(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkspaces(w) {
		return
	}
	if err := s.opts.Workspaces.Remove(strings.TrimSpace(r.PathValue("id"))); err != nil {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}
