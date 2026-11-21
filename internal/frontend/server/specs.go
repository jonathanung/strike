package server

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/persist/spec"
)

// Spec authoring (#1348): phase-gated requirements (EARS) → design → tasks,
// stored as plain files under <WorkDir>/.strike/specs/<name>/. Hand-off
// creates a plan through the same host.Plans service as POST /v1/plans.

// specView is a spec plus the daemon-computed checks the UI renders.
type specView struct {
	spec.Spec
	Coverage spec.Coverage       `json:"coverage"`
	Lint     map[string][]string `json:"lint"`
	Path     string              `json:"path"`
}

func (s *Server) specStore() (*spec.Store, string) {
	dir := strings.TrimSpace(s.opts.WorkDir)
	if dir == "" {
		return nil, ""
	}
	root := filepath.Join(dir, ".strike", "specs")
	st, err := spec.Open(root)
	if err != nil {
		return nil, ""
	}
	return st, root
}

func (s *Server) writeSpec(w http.ResponseWriter, status int, sp spec.Spec, root string) {
	writeJSON(w, status, specView{
		Spec:     sp,
		Coverage: spec.ComputeCoverage(sp),
		Lint:     spec.Lint(sp),
		Path:     filepath.Join(root, sp.Name),
	})
}

func writeSpecError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, spec.ErrNotFound):
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: err.Error()})
	case errors.Is(err, spec.ErrExists), errors.Is(err, spec.ErrConflict), errors.Is(err, spec.ErrHandedOff):
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
	case errors.Is(err, spec.ErrGate), errors.Is(err, spec.ErrInvalid):
		writeJSON(w, http.StatusUnprocessableEntity, opErrorResponse{Error: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
	}
}

func (s *Server) handleSpecsList(w http.ResponseWriter, r *http.Request) {
	st, _ := s.specStore()
	if st == nil {
		capabilityUnavailable(w, "specs")
		return
	}
	list, err := st.List()
	if err != nil {
		writeSpecError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"specs": list})
}

func (s *Server) handleSpecCreate(w http.ResponseWriter, r *http.Request) {
	st, root := s.specStore()
	if st == nil {
		capabilityUnavailable(w, "specs")
		return
	}
	var body struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	sp, err := st.Create(body.Name, body.Title, body.Description)
	if err != nil {
		writeSpecError(w, err)
		return
	}
	s.writeSpec(w, http.StatusCreated, sp, root)
}

func (s *Server) handleSpecGet(w http.ResponseWriter, r *http.Request) {
	st, root := s.specStore()
	if st == nil {
		capabilityUnavailable(w, "specs")
		return
	}
	sp, err := st.Get(strings.TrimSpace(r.PathValue("name")))
	if err != nil {
		writeSpecError(w, err)
		return
	}
	s.writeSpec(w, http.StatusOK, sp, root)
}

// handleSpecPhaseSave saves one phase's content: requirements, design or tasks.
func (s *Server) handleSpecPhaseSave(w http.ResponseWriter, r *http.Request) {
	st, root := s.specStore()
	if st == nil {
		capabilityUnavailable(w, "specs")
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	var body struct {
		Requirements    []spec.Requirement `json:"requirements"`
		Design          string             `json:"design"`
		Tasks           []spec.Task        `json:"tasks"`
		ExpectedVersion int                `json:"expectedVersion"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	var (
		sp  spec.Spec
		err error
	)
	switch r.PathValue("phase") {
	case spec.PhaseRequirements:
		sp, err = st.SaveRequirements(name, body.Requirements, body.ExpectedVersion)
	case spec.PhaseDesign:
		sp, err = st.SaveDesign(name, body.Design, body.ExpectedVersion)
	case spec.PhaseTasks:
		sp, err = st.SaveTasks(name, body.Tasks, body.ExpectedVersion)
	default:
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "unknown phase"})
		return
	}
	if err != nil {
		writeSpecError(w, err)
		return
	}
	s.writeSpec(w, http.StatusOK, sp, root)
}

// handleSpecPhaseStatus moves one phase to draft / review / approved.
// Approval is only ever this explicit request — never a model side effect.
func (s *Server) handleSpecPhaseStatus(w http.ResponseWriter, r *http.Request) {
	st, root := s.specStore()
	if st == nil {
		capabilityUnavailable(w, "specs")
		return
	}
	var body struct {
		Status          string `json:"status"`
		ExpectedVersion int    `json:"expectedVersion"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	sp, err := st.SetPhaseStatus(strings.TrimSpace(r.PathValue("name")), r.PathValue("phase"), strings.TrimSpace(body.Status), body.ExpectedVersion)
	if err != nil {
		writeSpecError(w, err)
		return
	}
	s.writeSpec(w, http.StatusOK, sp, root)
}

// handleSpecHandOff turns the approved, fully covered tasks phase into a
// plan (sections = tasks, bodies cite their requirements).
func (s *Server) handleSpecHandOff(w http.ResponseWriter, r *http.Request) {
	st, root := s.specStore()
	if st == nil {
		capabilityUnavailable(w, "specs")
		return
	}
	plans := s.plansService()
	if plans == nil {
		capabilityUnavailable(w, "plans")
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
	sp, err := st.HandOff(strings.TrimSpace(r.PathValue("name")), body.ExpectedVersion, func(title string, secs []spec.PlanSection) (string, error) {
		sections := make([]host.PlanSection, 0, len(secs))
		for _, sec := range secs {
			sections = append(sections, host.PlanSection{Title: sec.Title, Body: sec.Body})
		}
		p, err := plans.Create(owner, title, sections)
		if err != nil {
			return "", err
		}
		return p.ID, nil
	})
	if err != nil {
		writeSpecError(w, err)
		return
	}
	s.writeSpec(w, http.StatusOK, sp, root)
}
