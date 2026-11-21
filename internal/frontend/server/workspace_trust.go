package server

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/product/project"
	"github.com/jonathanung/strike/internal/trust/workspacetrust"
)

// workspaceTrustResponse is the body of GET/POST /v1/workspace/trust (#1781).
// Surfaces is the load-free enumeration of what the repo would load, the same
// list `strike trust status` prints: names and command lines, never env or
// headers.
type workspaceTrustResponse struct {
	// Root is the canonical workspace root the decision is keyed on (git top
	// level when known, else the live root's working directory).
	Root    string `json:"root"`
	Trusted bool   `json:"trusted"`
	// TrustedAt is RFC3339 UTC when Trusted.
	TrustedAt string `json:"trustedAt,omitempty"`
	// Untrustable is true for the home directory and filesystem root, which
	// never accept a grant.
	Untrustable bool                   `json:"untrustable,omitempty"`
	Summary     string                 `json:"summary"`
	Surfaces    config.ProjectSurfaces `json:"surfaces"`
	// Mutable reports whether POST is accepted (live root, not --read-only).
	Mutable bool `json:"mutable"`
	// RestartRequired is true when the decision differs from what the running
	// sessions were assembled with: trust takes effect on the next strike
	// start (live re-assembly is a follow-up).
	RestartRequired bool   `json:"restartRequired,omitempty"`
	Error           string `json:"error,omitempty"`
}

// WorkspaceTrustAssembly records the workspace a daemon assembled its
// sessions for (#1781): Dir is the launch directory, Root the canonical
// workspace root the trust store is keyed on, Trusted the decision applied.
type WorkspaceTrustAssembly struct {
	Dir     string
	Root    string
	Trusted bool
}

// workspaceTrustMu serializes this daemon's open-mutate-save cycles on the
// trust file so two concurrent POSTs cannot drop each other's grant.
var workspaceTrustMu sync.Mutex

// workspaceTrustRequest is the POST body. The workspace is never taken from
// the request: it is always the live root's own working directory, so a
// browser cannot trust an arbitrary path.
type workspaceTrustRequest struct {
	Action string `json:"action"`
}

// workspaceTrustCapable reports whether the trust surface is wired.
func (s *Server) workspaceTrustCapable() bool {
	return strings.TrimSpace(s.opts.WorkspaceTrustPath) != ""
}

// workspaceTrustMutable reports whether POST /v1/workspace/trust is allowed.
func (s *Server) workspaceTrustMutable() bool {
	return s.workspaceTrustCapable() && s.hasLive() && !s.opts.ReadOnly
}

// workspaceTrustRoot resolves the workspace the request is about: the
// daemon's assembled workspace when known (all roots share it), else the
// request's live root CWD, else the daemon WorkDir. It writes the error
// response and returns ok=false when nothing resolves.
func (s *Server) workspaceTrustRoot(w http.ResponseWriter, r *http.Request) (workDir, root string, ok bool) {
	if !s.workspaceTrustCapable() {
		capabilityUnavailable(w, "workspaceTrust")
		return "", "", false
	}
	if id := rootParam(r); id != "" && s.opts.LiveHub != nil && s.opts.LiveHub.LiveFor(id) == nil {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "root not found"})
		return "", "", false
	}
	if a := s.opts.WorkspaceTrustAssembled; a != nil && strings.TrimSpace(a.Root) != "" {
		dir := strings.TrimSpace(a.Dir)
		if dir == "" {
			dir = a.Root
		}
		return dir, a.Root, true
	}
	workDir = strings.TrimSpace(s.currentCWD(r))
	if workDir == "" {
		workDir = strings.TrimSpace(s.opts.WorkDir)
	}
	if workDir == "" {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "no workspace directory is known for this root"})
		return "", "", false
	}
	if info, err := os.Stat(workDir); err != nil || !info.IsDir() {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "workspace directory is not available"})
		return "", "", false
	}
	id, err := project.Resolve(r.Context(), workDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return "", "", false
	}
	return workDir, id.Root, true
}

// workspaceTrustStatus builds the response from a freshly opened store so a
// grant made by `strike trust` in another terminal is visible immediately.
func (s *Server) workspaceTrustStatus(store *workspacetrust.Store, workDir, root string) workspaceTrustResponse {
	surfaces := config.EnumerateProjectSurfaces(workDir, root)
	resp := workspaceTrustResponse{
		Root:     root,
		Summary:  surfaces.Summary(),
		Surfaces: surfaces,
		Mutable:  s.workspaceTrustMutable(),
	}
	if rec, ok := store.Record(root); ok {
		resp.Trusted = true
		resp.TrustedAt = rec.TrustedAt
	}
	resp.Untrustable = workspacetrust.Untrustable(root)
	if a := s.opts.WorkspaceTrustAssembled; a != nil && a.Root == root && a.Trusted != resp.Trusted {
		resp.RestartRequired = true
	}
	return resp
}

// handleWorkspaceTrustGet reports trust state and the repo config it gates.
func (s *Server) handleWorkspaceTrustGet(w http.ResponseWriter, r *http.Request) {
	workDir, root, ok := s.workspaceTrustRoot(w, r)
	if !ok {
		return
	}
	store, err := workspacetrust.Open(s.opts.WorkspaceTrustPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.workspaceTrustStatus(store, workDir, root))
}

// handleWorkspaceTrustPost grants or revokes trust for the live root's
// workspace. Trust only lets repo config load on the next start; it never
// widens a permission.
func (s *Server) handleWorkspaceTrustPost(w http.ResponseWriter, r *http.Request) {
	if !s.workspaceTrustCapable() {
		capabilityUnavailable(w, "workspaceTrust")
		return
	}
	if !s.requireMutable(w) {
		return
	}
	if s.opts.ReadOnly {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "daemon is --read-only"})
		return
	}
	var body workspaceTrustRequest
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	action := strings.TrimSpace(body.Action)
	if action != "grant" && action != "revoke" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: `action must be "grant" or "revoke"`})
		return
	}
	workDir, root, ok := s.workspaceTrustRoot(w, r)
	if !ok {
		return
	}
	workspaceTrustMu.Lock()
	defer workspaceTrustMu.Unlock()
	store, err := workspacetrust.Open(s.opts.WorkspaceTrustPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	if action == "grant" {
		err = store.Trust(root)
	} else {
		err = store.Revoke(root)
	}
	if err != nil {
		resp := s.workspaceTrustStatus(store, workDir, root)
		resp.Error = err.Error()
		code := http.StatusInternalServerError
		if errors.Is(err, workspacetrust.ErrUntrustable) {
			code = http.StatusUnprocessableEntity
		}
		writeJSON(w, code, resp)
		return
	}
	writeJSON(w, http.StatusOK, s.workspaceTrustStatus(store, workDir, root))
}
