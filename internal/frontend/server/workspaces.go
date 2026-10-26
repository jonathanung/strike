package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/persist/workspace"
	"github.com/jonathanung/strike/internal/protocol"
)

type workspacesResponse struct {
	Workspaces  []workspace.Workspace `json:"workspaces"`
	ActiveID    string                `json:"activeId,omitempty"`
	Recents     []workspace.Workspace `json:"recents"`
	BrowseRoots []string              `json:"browseRoots"`
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
	recents := s.opts.Workspaces.Recents(5)
	if recents == nil {
		recents = []workspace.Workspace{}
	}
	roots := s.opts.Workspaces.BrowseRoots()
	if roots == nil {
		roots = []string{}
	}
	writeJSON(w, http.StatusOK, workspacesResponse{
		Workspaces:  list,
		ActiveID:    active,
		Recents:     recents,
		BrowseRoots: roots,
	})
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
	s.applyWorkspaceMode(r.Context(), ws)
	writeJSON(w, http.StatusOK, ws)
}

// applyWorkspaceMode pushes the workspace's stored posture onto the active
// live root. The approvals UI owns changing the mode later; this only
// applies the stored value at activation. An empty mode leaves the session
// as it is. A missing live root still leaves the registry updated.
func (s *Server) applyWorkspaceMode(ctx context.Context, ws workspace.Workspace) {
	mode := strings.TrimSpace(ws.PermissionMode)
	if mode == "" || s.opts.LiveHub == nil {
		return
	}
	parsed, ok := protocol.ParsePermissionMode(mode)
	if !ok {
		return
	}
	live := s.opts.LiveHub.Active()
	if live == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = live.Submit(ctx, protocol.SetPermissionMode{Mode: parsed})
}

func (s *Server) handleWorkspaceBrowse(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkspaces(w) {
		return
	}
	res, err := s.opts.Workspaces.Browse(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if res.Entries == nil {
		res.Entries = []workspace.BrowseEntry{}
	}
	writeJSON(w, http.StatusOK, res)
}

type workspaceMkdirBody struct {
	Parent string `json:"parent"`
	Name   string `json:"name"`
}

func (s *Server) handleWorkspaceMkdir(w http.ResponseWriter, r *http.Request) {
	if !s.requireWorkspaces(w) {
		return
	}
	var body workspaceMkdirBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid json"})
		return
	}
	path, err := s.opts.Workspaces.Mkdir(body.Parent, body.Name)
	if err != nil {
		writeWorkspaceErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"path": path})
}

type workspacePolicyBody struct {
	Rules          *string   `json:"rules"`
	PermissionMode *string   `json:"permissionMode"`
	Name           *string   `json:"name"`
	ExtraRoots     *[]string `json:"extraRoots"`
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
	if body.Rules == nil && body.PermissionMode == nil && body.Name == nil && body.ExtraRoots == nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "rules, permissionMode, name, or extraRoots is required"})
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
	var (
		ws  workspace.Workspace
		err error
	)
	if body.Name != nil {
		ws, err = s.opts.Workspaces.Rename(id, *body.Name)
		if err != nil {
			writeWorkspaceErr(w, err)
			return
		}
	}
	if body.ExtraRoots != nil {
		ws, err = s.opts.Workspaces.SetExtraRoots(id, *body.ExtraRoots)
		if err != nil {
			writeWorkspaceErr(w, err)
			return
		}
	}
	if body.Rules != nil || body.PermissionMode != nil {
		ws, err = s.opts.Workspaces.SetPolicy(id, body.Rules, body.PermissionMode)
		if err != nil {
			writeWorkspaceErr(w, err)
			return
		}
	}
	if ws.ID == "" {
		got, ok := s.opts.Workspaces.Get(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, opErrorResponse{Error: fmt.Sprintf("workspace: %q not found", id)})
			return
		}
		ws = got
	}
	writeJSON(w, http.StatusOK, ws)
}

func writeWorkspaceErr(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if strings.Contains(err.Error(), "not found") {
		status = http.StatusNotFound
	}
	writeJSON(w, status, opErrorResponse{Error: err.Error()})
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
