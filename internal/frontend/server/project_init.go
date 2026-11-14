package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jonathanung/strike/internal/frontend/host"
)

// projectInitResponse is the body of GET/POST /v1/init (#1581).
type projectInitResponse struct {
	// Path is the absolute AGENTS.md target for the resolved root.
	Path string `json:"path"`
	// Exists reports a non-empty AGENTS.md already at Path.
	Exists bool `json:"exists"`
	// Created is true on a POST that wrote a new file.
	Created bool `json:"created,omitempty"`
	// Error is set on refusal (already exists) or failure.
	Error string `json:"error,omitempty"`
}

// projectInitCapable reports whether the browser may bootstrap AGENTS.md:
// the host service is present and the daemon accepts mutations.
func (s *Server) projectInitCapable() bool {
	return s.opts.Services != nil && s.opts.Services.Init != nil && s.hasLive() && !s.opts.ReadOnly
}

// projectInitFor resolves the host ProjectInit for the request's root. With
// a live root, AGENTS.md lands in that root's working directory (the
// workspace the browser is looking at), built by ProjectInitFor so the
// server never re-implements generation. Without a live CWD it falls back to
// the daemon's own service. An explicit ?root= that names no live root is a
// 404 rather than a silent write into the daemon directory.
func (s *Server) projectInitFor(w http.ResponseWriter, r *http.Request) host.ProjectInit {
	if s.opts.Services == nil || s.opts.Services.Init == nil {
		capabilityUnavailable(w, "projectInit")
		return nil
	}
	if root := rootParam(r); root != "" && s.opts.LiveHub != nil && s.opts.LiveHub.LiveFor(root) == nil {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "root not found"})
		return nil
	}
	if s.opts.ProjectInitFor != nil {
		if cwd := strings.TrimSpace(s.currentCWD(r)); cwd != "" {
			if svc := s.opts.ProjectInitFor(cwd); svc != nil {
				return svc
			}
		}
	}
	return s.opts.Services.Init
}

// handleProjectInitGet previews the target: where AGENTS.md would be written
// and whether it already exists.
func (s *Server) handleProjectInitGet(w http.ResponseWriter, r *http.Request) {
	svc := s.projectInitFor(w, r)
	if svc == nil {
		return
	}
	exists, path, err := svc.Exists()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, projectInitResponse{Path: path, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, projectInitResponse{Path: path, Exists: exists})
}

// handleProjectInitPost writes AGENTS.md via the host service. It never
// overwrites: an existing file is a 409 (the TUI confirms before replacing;
// browser overwrite is a deliberate follow-up).
func (s *Server) handleProjectInitPost(w http.ResponseWriter, r *http.Request) {
	svc := s.projectInitFor(w, r)
	if svc == nil {
		return
	}
	if !s.requireMutable(w) {
		return
	}
	if s.opts.ReadOnly {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "daemon is --read-only"})
		return
	}
	path, created, err := svc.Write(false)
	switch {
	case errors.Is(err, host.ErrInitExists):
		writeJSON(w, http.StatusConflict, projectInitResponse{Path: path, Exists: true, Error: err.Error()})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, projectInitResponse{Path: path, Error: err.Error()})
	default:
		writeJSON(w, http.StatusCreated, projectInitResponse{Path: path, Exists: true, Created: created})
	}
}
