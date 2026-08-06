package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// --- request/response DTOs (web-safe; no TUI types) ---

type workflowSaveRequest struct {
	Document host.WorkflowDocument `json:"document"`
	Scope    string                `json:"scope"`
	Force    bool                  `json:"force"`
}

type workflowSaveResponse struct {
	Path      string `json:"path"`
	Activated bool   `json:"activated"`
}

type workflowValidateRequest struct {
	Document host.WorkflowDocument `json:"document"`
}

type workflowValidateResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type workflowFormatRequest struct {
	Document host.WorkflowDocument `json:"document"`
}

type workflowFormatResponse struct {
	JSON string `json:"json"`
}

type workflowPhaseGrantsRequest struct {
	Document   host.WorkflowDocument `json:"document"`
	PhaseIndex int                   `json:"phaseIndex"`
}

type workflowScaffoldRequest struct {
	Name string `json:"name"`
}

type workflowStartRequest struct {
	// Confirm must be true after the client has shown grant review.
	Confirm bool `json:"confirm"`
}

type workflowDraftReviewRequest struct {
	JSON string `json:"json"`
}

type workflowDraftSaveRequest struct {
	JSON    string `json:"json"`
	Scope   string `json:"scope"`
	Confirm bool   `json:"confirm"`
	Force   bool   `json:"force"`
}

func (s *Server) workflows() host.Workflows {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.Workflows
}

func (s *Server) workflowDrafts() host.WorkflowDrafts {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.WorkflowDrafts
}

func (s *Server) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	wf := s.workflows()
	if wf == nil {
		capabilityUnavailable(w, "workflows")
		return
	}
	list := wf.List()
	if list == nil {
		list = []host.WorkflowSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": list})
}

func (s *Server) handleWorkflowGet(w http.ResponseWriter, r *http.Request) {
	wf := s.workflows()
	if wf == nil {
		capabilityUnavailable(w, "workflows")
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	sum, ok := wf.Get(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "workflow not found"})
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) handleWorkflowDocument(w http.ResponseWriter, r *http.Request) {
	wf := s.workflows()
	if wf == nil {
		capabilityUnavailable(w, "workflows")
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	doc, ok := wf.Document(name)
	if !ok {
		// Fall back to summary→document shape when only catalog entry exists.
		if sum, sok := wf.Get(name); sok {
			writeJSON(w, http.StatusOK, summaryToWorkflowDocument(sum))
			return
		}
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "workflow not found"})
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func summaryToWorkflowDocument(sum host.WorkflowSummary) host.WorkflowDocument {
	doc := host.WorkflowDocument{
		SchemaVersion: 1,
		Name:          sum.Name,
		Description:   sum.Description,
	}
	doc.Phases = make([]host.WorkflowPhaseDocument, 0, len(sum.Phases))
	for _, p := range sum.Phases {
		pd := host.WorkflowPhaseDocument{
			Name:        p.Name,
			Description: p.Description,
			Agent:       p.Agent,
			Gate:        p.Gate,
			GateCommand: p.GateCommand,
		}
		if len(p.Permissions) > 0 {
			pd.Permissions = append([]host.WorkflowPermission(nil), p.Permissions...)
		}
		doc.Phases = append(doc.Phases, pd)
	}
	return doc
}

func (s *Server) handleWorkflowScaffold(w http.ResponseWriter, r *http.Request) {
	wf := s.workflows()
	if wf == nil {
		capabilityUnavailable(w, "workflows")
		return
	}
	var body workflowScaffoldRequest
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	doc, err := wf.Scaffold(strings.TrimSpace(body.Name))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) handleWorkflowValidate(w http.ResponseWriter, r *http.Request) {
	wf := s.workflows()
	if wf == nil {
		capabilityUnavailable(w, "workflows")
		return
	}
	var body workflowValidateRequest
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if err := wf.Validate(body.Document); err != nil {
		writeJSON(w, http.StatusOK, workflowValidateResponse{OK: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, workflowValidateResponse{OK: true})
}

func (s *Server) handleWorkflowFormat(w http.ResponseWriter, r *http.Request) {
	wf := s.workflows()
	if wf == nil {
		capabilityUnavailable(w, "workflows")
		return
	}
	var body workflowFormatRequest
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	raw, err := wf.Format(body.Document)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, workflowFormatResponse{JSON: raw})
}

func (s *Server) handleWorkflowPhaseGrants(w http.ResponseWriter, r *http.Request) {
	wf := s.workflows()
	if wf == nil {
		capabilityUnavailable(w, "workflows")
		return
	}
	var body workflowPhaseGrantsRequest
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	grants := wf.PhaseGrants(body.Document, body.PhaseIndex)
	if grants == nil {
		grants = []host.WorkflowPermission{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"grants": grants})
}

func (s *Server) handleWorkflowSave(w http.ResponseWriter, r *http.Request) {
	wf := s.workflows()
	if wf == nil {
		capabilityUnavailable(w, "workflows")
		return
	}
	var body workflowSaveRequest
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	path, err := wf.Save(body.Document, body.Scope, body.Force)
	if err != nil {
		writeWorkflowSaveError(w, err)
		return
	}
	// Saves never activate — surface that contract explicitly.
	writeJSON(w, http.StatusOK, workflowSaveResponse{Path: path, Activated: false})
}

func writeWorkflowSaveError(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case errors.Is(err, host.ErrWorkflowInvalid), strings.Contains(msg, "draft is invalid"), strings.Contains(msg, "workflow is invalid"):
		writeJSON(w, http.StatusUnprocessableEntity, opErrorResponse{Error: msg})
	case errors.Is(err, host.ErrWorkflowExists), strings.Contains(msg, "already exists"):
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: msg})
	case strings.Contains(msg, "requires explicit confirmation"):
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: msg})
	default:
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: msg})
	}
}

// handleWorkflowStart activates a catalog entry only after explicit confirm and
// only when the entry is valid. Grant review is a client responsibility before
// confirm; invalid drafts cannot be activated through this path.
func (s *Server) handleWorkflowStart(w http.ResponseWriter, r *http.Request) {
	wf := s.workflows()
	if wf == nil {
		capabilityUnavailable(w, "workflows")
		return
	}
	live := s.resolveLive(w, r)
	if live == nil {
		http.Error(w, "live session unavailable", http.StatusServiceUnavailable)
		return
	}
	var body workflowStartRequest
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if !body.Confirm {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "start requires confirm=true after grant review"})
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	sum, ok := wf.Get(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "workflow not found"})
		return
	}
	if !sum.Valid {
		msg := "workflow is invalid and cannot be activated"
		if sum.ValidationError != "" {
			msg = msg + ": " + sum.ValidationError
		}
		writeJSON(w, http.StatusUnprocessableEntity, opErrorResponse{Error: msg})
		return
	}
	if len(sum.Phases) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, opErrorResponse{Error: "workflow has no phases and cannot be activated"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := live.Submit(ctx, protocol.StartWorkflow{Name: sum.Name}); err != nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleWorkflowStop(w http.ResponseWriter, r *http.Request) {
	if s.workflows() == nil {
		capabilityUnavailable(w, "workflows")
		return
	}
	live := s.resolveLive(w, r)
	if live == nil {
		http.Error(w, "live session unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := live.Submit(ctx, protocol.StopWorkflow{}); err != nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleWorkflowDraftReview(w http.ResponseWriter, r *http.Request) {
	drafts := s.workflowDrafts()
	if drafts == nil {
		capabilityUnavailable(w, "workflowDrafts")
		return
	}
	var body workflowDraftReviewRequest
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, drafts.Review(body.JSON))
}

func (s *Server) handleWorkflowDraftSave(w http.ResponseWriter, r *http.Request) {
	drafts := s.workflowDrafts()
	if drafts == nil {
		capabilityUnavailable(w, "workflowDrafts")
		return
	}
	var body workflowDraftSaveRequest
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	result, err := drafts.Save(body.JSON, body.Scope, body.Confirm, body.Force)
	if err != nil {
		writeWorkflowSaveError(w, err)
		return
	}
	// Keep the in-memory catalog in sync so List/Get/start see the new file
	// without requiring a process restart. Force rewrite is idempotent.
	if wf := s.workflows(); wf != nil {
		if rev := drafts.Review(body.JSON); rev.Valid && rev.CanonicalJSON != "" {
			if doc, ok := documentFromCanonicalJSON(wf, rev.CanonicalJSON); ok {
				_, _ = wf.Save(doc, body.Scope, true)
			}
		}
	}
	result.Activated = false
	writeJSON(w, http.StatusOK, result)
}

// documentFromCanonicalJSON decodes canonical workflow JSON into a host document.
func documentFromCanonicalJSON(wf host.Workflows, canonical string) (host.WorkflowDocument, bool) {
	var doc host.WorkflowDocument
	if err := json.Unmarshal([]byte(canonical), &doc); err != nil {
		return host.WorkflowDocument{}, false
	}
	if err := wf.Validate(doc); err != nil {
		return host.WorkflowDocument{}, false
	}
	return doc, true
}
