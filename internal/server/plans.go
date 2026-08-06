package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/plan"
)

// plansService returns host.Plans when configured.
func (s *Server) plansService() host.Plans {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.Plans
}

func (s *Server) handlePlansList(w http.ResponseWriter, r *http.Request) {
	svc := s.plansService()
	if svc == nil {
		capabilityUnavailable(w, "plans")
		return
	}
	items, err := svc.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	if items == nil {
		items = []host.PlanMeta{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"plans": items})
}

func (s *Server) handlePlanGet(w http.ResponseWriter, r *http.Request) {
	svc := s.plansService()
	if svc == nil {
		capabilityUnavailable(w, "plans")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "plan id is required"})
		return
	}
	p, ok, err := svc.Get(id)
	if err != nil {
		writePlanError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: plan.ErrNotFound.Error()})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handlePlanCreate(w http.ResponseWriter, r *http.Request) {
	svc := s.plansService()
	if svc == nil {
		capabilityUnavailable(w, "plans")
		return
	}
	var body struct {
		OwnerRoot string `json:"ownerRoot"`
		Title     string `json:"title"`
		Sections  []struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		} `json:"sections"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	owner := resolvePlanOwner(body.OwnerRoot, r)
	if owner == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "ownerRoot is required"})
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "title is required"})
		return
	}
	var sections []host.PlanSection
	for _, sec := range body.Sections {
		sections = append(sections, host.PlanSection{Title: sec.Title, Body: sec.Body})
	}
	p, err := svc.Create(owner, title, sections)
	if err != nil {
		writePlanError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handlePlanUpdateTitle(w http.ResponseWriter, r *http.Request) {
	svc := s.plansService()
	if svc == nil {
		capabilityUnavailable(w, "plans")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "plan id is required"})
		return
	}
	var body struct {
		OwnerRoot       string `json:"ownerRoot"`
		Title           string `json:"title"`
		ExpectedVersion int    `json:"expectedVersion"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	owner := resolvePlanOwner(body.OwnerRoot, r)
	if owner == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "ownerRoot is required"})
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "title is required"})
		return
	}
	p, err := svc.UpdateTitle(id, owner, title, body.ExpectedVersion)
	if err != nil {
		writePlanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handlePlanUpdateSection(w http.ResponseWriter, r *http.Request) {
	svc := s.plansService()
	if svc == nil {
		capabilityUnavailable(w, "plans")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	sectionID := strings.TrimSpace(r.PathValue("sectionID"))
	if id == "" || sectionID == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "plan id and section id are required"})
		return
	}
	var body struct {
		OwnerRoot       string  `json:"ownerRoot"`
		Title           *string `json:"title"`
		Body            *string `json:"body"`
		ExpectedVersion int     `json:"expectedVersion"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	owner := resolvePlanOwner(body.OwnerRoot, r)
	if owner == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "ownerRoot is required"})
		return
	}
	if body.Title == nil && body.Body == nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "title or body is required"})
		return
	}
	p, err := svc.UpdateSection(id, owner, sectionID, body.Title, body.Body, body.ExpectedVersion)
	if err != nil {
		writePlanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handlePlanAddSection(w http.ResponseWriter, r *http.Request) {
	svc := s.plansService()
	if svc == nil {
		capabilityUnavailable(w, "plans")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "plan id is required"})
		return
	}
	var body struct {
		OwnerRoot       string `json:"ownerRoot"`
		Title           string `json:"title"`
		Body            string `json:"body"`
		ExpectedVersion int    `json:"expectedVersion"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	owner := resolvePlanOwner(body.OwnerRoot, r)
	if owner == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "ownerRoot is required"})
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "title is required"})
		return
	}
	p, err := svc.AddSection(id, owner, title, body.Body, body.ExpectedVersion)
	if err != nil {
		writePlanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handlePlanSetStatus(w http.ResponseWriter, r *http.Request) {
	svc := s.plansService()
	if svc == nil {
		capabilityUnavailable(w, "plans")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "plan id is required"})
		return
	}
	var body struct {
		OwnerRoot       string `json:"ownerRoot"`
		Status          string `json:"status"`
		ExpectedVersion int    `json:"expectedVersion"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	owner := resolvePlanOwner(body.OwnerRoot, r)
	if owner == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "ownerRoot is required"})
		return
	}
	status := strings.TrimSpace(body.Status)
	if status == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "status is required"})
		return
	}
	p, err := svc.SetStatus(id, owner, status, body.ExpectedVersion)
	if err != nil {
		writePlanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handlePlanReopen(w http.ResponseWriter, r *http.Request) {
	svc := s.plansService()
	if svc == nil {
		capabilityUnavailable(w, "plans")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "plan id is required"})
		return
	}
	var body struct {
		OwnerRoot       string `json:"ownerRoot"`
		ExpectedVersion int    `json:"expectedVersion"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	owner := resolvePlanOwner(body.OwnerRoot, r)
	if owner == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "ownerRoot is required"})
		return
	}
	p, err := svc.Reopen(id, owner, body.ExpectedVersion)
	if err != nil {
		writePlanError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// resolvePlanOwner prefers an explicit body ownerRoot, then ?root=.
func resolvePlanOwner(bodyOwner string, r *http.Request) string {
	if o := strings.TrimSpace(bodyOwner); o != "" {
		return o
	}
	return rootParam(r)
}

// writePlanError maps plan store sentinel errors to HTTP status codes.
// Ownership and CAS are never relaxed — ErrNotOwner and ErrConflict stay hard.
func writePlanError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, plan.ErrNotFound):
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: err.Error()})
	case errors.Is(err, plan.ErrNotOwner):
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: err.Error()})
	case errors.Is(err, plan.ErrConflict):
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
	case errors.Is(err, plan.ErrInvalidStatus),
		errors.Is(err, plan.ErrClosedPlan),
		errors.Is(err, plan.ErrInFlight),
		errors.Is(err, plan.ErrDelegateMismatch):
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
	default:
		// Validation errors from the store (empty title, etc.) are 400.
		msg := err.Error()
		if strings.HasPrefix(msg, "plan:") {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: msg})
			return
		}
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: msg})
	}
}
