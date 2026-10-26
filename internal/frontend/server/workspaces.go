package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jonathanung/strike/internal/persist/workspace"
	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/trust/usageaudit"
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

type workspacePolicyBody struct {
	Rules          *string `json:"rules"`
	PermissionMode *string `json:"permissionMode"`
}

func (s *Server) handleWorkspacePolicy(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkspaces(w) {
		return
	}
	var body workspacePolicyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid json"})
		return
	}
	if body.Rules == nil && body.PermissionMode == nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "rules or permissionMode is required"})
		return
	}
	if body.PermissionMode != nil && strings.TrimSpace(*body.PermissionMode) != "" {
		mode, ok := protocol.ParsePermissionMode(*body.PermissionMode)
		if !ok {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "unknown permissionMode"})
			return
		}
		canonical := string(mode)
		body.PermissionMode = &canonical
	}
	id := strings.TrimSpace(r.PathValue("id"))
	ws, err := s.opts.Workspaces.SetPolicy(id, body.Rules, body.PermissionMode)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, opErrorResponse{Error: err.Error()})
		return
	}
	action := usageaudit.ActionConfig
	summary := "workspace policy updated"
	if body.PermissionMode != nil && strings.TrimSpace(*body.PermissionMode) != "" {
		action = usageaudit.ActionPermission
		summary = "workspace permission mode " + strings.TrimSpace(*body.PermissionMode)
	}
	detail := map[string]any{"workspaceId": id}
	if body.PermissionMode != nil {
		detail["permissionMode"] = strings.TrimSpace(*body.PermissionMode)
	}
	if body.Rules != nil {
		detail["rulesSet"] = strings.TrimSpace(*body.Rules) != ""
	}
	s.noteAudit(action, "", summary, detail)
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
