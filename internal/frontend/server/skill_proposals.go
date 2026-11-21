package server

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/product/project"
	"github.com/jonathanung/strike/internal/product/skillpropose"
	"github.com/jonathanung/strike/pkg/redact"
)

// Skill proposals (#1611): repeated workflows drafted as skills. The scan is
// off per workspace until the operator opts in, runs only when asked, and
// reads this workspace's own session logs. Accept installs the draft as a
// workspace skill through the same preview + apply path Customize uses.

var proposalStores sync.Map // state file path → *skillpropose.Store

func proposalStore() *skillpropose.Store {
	path := filepath.Join(config.GlobalRoot(), "skill-proposals.json")
	st, _ := proposalStores.LoadOrStore(path, skillpropose.NewStore(path))
	return st.(*skillpropose.Store)
}

type skillProposalsResponse struct {
	// Workspace is the project key the scan reads sessions for: the same
	// key sessions record in their meta (projectKey).
	Workspace  string                  `json:"workspace"`
	Enabled    bool                    `json:"enabled"`
	Threshold  int                     `json:"threshold"`
	WindowDays int                     `json:"windowDays"`
	LastScan   *time.Time              `json:"lastScan,omitempty"`
	Pending    []skillpropose.Proposal `json:"pending"`
}

type skillProposalSettings struct {
	Enabled   *bool `json:"enabled"`
	Threshold *int  `json:"threshold"`
}

type skillProposalTarget struct {
	ID string `json:"id"`
}

// proposalWorkspace is the Customize directory and its project key, the
// same key sessions record, so the scan reads only this workspace's logs.
func (s *Server) proposalWorkspace(r *http.Request) (key, dir string) {
	dir = s.customizeDir(r)
	key = dir
	if strings.TrimSpace(dir) != "" {
		if id, err := project.Resolve(r.Context(), dir); err == nil && id.Key != "" {
			key = id.Key
		}
	}
	return key, dir
}

func installedSkills(dir string) map[string]bool {
	out := map[string]bool{}
	cat, err := config.LoadCustomize(dir)
	if err != nil {
		return out
	}
	for _, sk := range cat.Skills {
		out[sk.Name] = true
	}
	return out
}

func proposalsView(key string, ws skillpropose.Workspace, installed map[string]bool) skillProposalsResponse {
	out := skillProposalsResponse{
		Workspace:  key,
		Enabled:    ws.Enabled,
		Threshold:  ws.EffectiveThreshold(),
		WindowDays: int(skillpropose.DefaultWindow / (24 * time.Hour)),
		Pending:    []skillpropose.Proposal{},
	}
	if !ws.LastScan.IsZero() {
		at := ws.LastScan
		out.LastScan = &at
	}
	// A draft whose name now exists as a skill was accepted another way
	// (edited and saved in Customize): it is no longer a proposal.
	for _, p := range ws.Pending {
		if !installed[p.Name] {
			out.Pending = append(out.Pending, p)
		}
	}
	return out
}

func proposalFail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, opErrorResponse{Error: redact.String(err.Error())})
}

func (s *Server) refuseReadOnly(w http.ResponseWriter) bool {
	if s.opts.ReadOnly {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "read-only attach mode does not allow mutations"})
		return true
	}
	return false
}

func (s *Server) handleSkillProposals(w http.ResponseWriter, r *http.Request) {
	key, dir := s.proposalWorkspace(r)
	if !s.opts.ReadOnly {
		// Remember the directory for the weekly cadence (#1913).
		_ = proposalStore().SetDir(key, dir)
	}
	ws, err := proposalStore().Get(key)
	if err != nil {
		proposalFail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, proposalsView(key, ws, installedSkills(dir)))
}

func (s *Server) handleSkillProposalSettings(w http.ResponseWriter, r *http.Request) {
	if s.refuseReadOnly(w) {
		return
	}
	var body skillProposalSettings
	if err := decodeBody(w, r, &body); err != nil {
		proposalFail(w, http.StatusBadRequest, err)
		return
	}
	key, dir := s.proposalWorkspace(r)
	_ = proposalStore().SetDir(key, dir)
	ws, err := proposalStore().Settings(key, body.Enabled, body.Threshold)
	if err != nil {
		proposalFail(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, proposalsView(key, ws, installedSkills(dir)))
}

func (s *Server) handleSkillProposalScan(w http.ResponseWriter, r *http.Request) {
	if s.refuseReadOnly(w) {
		return
	}
	key, dir := s.proposalWorkspace(r)
	_ = proposalStore().SetDir(key, dir)
	now := time.Now()
	wfs, err := skillpropose.ReadWorkflows(s.opts.SessionDir, key, now.Add(-skillpropose.DefaultWindow))
	if err != nil {
		proposalFail(w, http.StatusInternalServerError, err)
		return
	}
	installed := installedSkills(dir)
	ws, err := proposalStore().Scan(key, wfs, installed, now)
	if errors.Is(err, skillpropose.ErrDisabled) {
		proposalFail(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		proposalFail(w, http.StatusInternalServerError, err)
		return
	}
	s.mirrorProposalIdeas(ws.Pending)
	writeJSON(w, http.StatusOK, proposalsView(key, ws, installed))
}

func (s *Server) handleSkillProposalDismiss(w http.ResponseWriter, r *http.Request) {
	if s.refuseReadOnly(w) {
		return
	}
	var body skillProposalTarget
	if err := decodeBody(w, r, &body); err != nil {
		proposalFail(w, http.StatusBadRequest, err)
		return
	}
	key, dir := s.proposalWorkspace(r)
	if _, err := proposalStore().Take(key, body.ID, true); err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, skillpropose.ErrNotFound) {
			code = http.StatusNotFound
		}
		proposalFail(w, code, err)
		return
	}
	s.settleProposalIdea(body.ID, true)
	ws, err := proposalStore().Get(key)
	if err != nil {
		proposalFail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, proposalsView(key, ws, installedSkills(dir)))
}

// handleSkillProposalAccept installs a pending draft as a workspace skill.
// It previews first and applies with that preview's diff hash, exactly as
// the Customize editor does, so the same validation and write guard apply.
func (s *Server) handleSkillProposalAccept(w http.ResponseWriter, r *http.Request) {
	if s.refuseReadOnly(w) {
		return
	}
	var body skillProposalTarget
	if err := decodeBody(w, r, &body); err != nil {
		proposalFail(w, http.StatusBadRequest, err)
		return
	}
	key, dir := s.proposalWorkspace(r)
	p, err := proposalStore().Find(key, body.ID)
	if errors.Is(err, skillpropose.ErrNotFound) {
		proposalFail(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		proposalFail(w, http.StatusInternalServerError, err)
		return
	}
	ch := config.CustomizeChange{
		Kind:        "skill",
		Op:          "upsert",
		Scope:       "workspace",
		Name:        p.Name,
		Description: p.Description,
		Body:        p.Body,
		BaseHash:    "missing",
	}
	preview, err := config.PreviewCustomize(dir, ch)
	if err != nil {
		proposalFail(w, http.StatusBadRequest, err)
		return
	}
	ch.Confirm = true
	ch.DiffHash = preview.DiffHash
	res, err := config.ApplyCustomize(dir, ch)
	if err != nil {
		proposalFail(w, http.StatusBadRequest, err)
		return
	}
	if _, err := proposalStore().Take(key, p.ID, false); err != nil && !errors.Is(err, skillpropose.ErrNotFound) {
		proposalFail(w, http.StatusInternalServerError, err)
		return
	}
	s.settleProposalIdea(p.ID, false)
	writeJSON(w, http.StatusOK, res)
}
