package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/host"
)

// --- web-safe DTOs (camelCase JSON; no TUI types) ---

type goalCriterionDTO struct {
	Description string `json:"description"`
	Check       string `json:"check"`
	Satisfied   bool   `json:"satisfied"`
}

type goalDTO struct {
	ID            string             `json:"id"`
	Description   string             `json:"description"`
	Criteria      []goalCriterionDTO `json:"criteria"`
	Status        string             `json:"status"`
	MaxIterations int                `json:"maxIterations"`
	MaxCostUSD    float64            `json:"maxCostUsd"`
	AllowedTools  []string           `json:"allowedTools,omitempty"`
	CostUSD       float64            `json:"costUsd"`
	LastIteration int                `json:"lastIteration"`
	FailReason    string             `json:"failReason,omitempty"`
	CreatedAt     time.Time          `json:"createdAt"`
}

type goalIterationDTO struct {
	N         int     `json:"n"`
	Plan      string  `json:"plan,omitempty"`
	StateHash string  `json:"stateHash,omitempty"`
	CostUSD   float64 `json:"costUsd"`
	Summary   string  `json:"summary,omitempty"`
}

type goalSetRequest struct {
	Description        string   `json:"description"`
	Criteria           []string `json:"criteria"`
	MaxIterations      int      `json:"maxIterations,omitempty"`
	MaxCostUSD         float64  `json:"maxCostUsd,omitempty"`
	MaxWallClockS      int      `json:"maxWallClockS,omitempty"`
	MaxNoProgressIters int      `json:"maxNoProgressIters,omitempty"`
	AllowedTools       []string `json:"allowedTools,omitempty"`
}

func (s *Server) goals() host.Goals {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.Goals
}

func toGoalDTO(g host.Goal) goalDTO {
	out := goalDTO{
		ID:            g.ID,
		Description:   g.Description,
		Status:        g.Status,
		MaxIterations: g.MaxIterations,
		MaxCostUSD:    g.MaxCostUSD,
		CostUSD:       g.CostUSD,
		LastIteration: g.LastIteration,
		FailReason:    g.FailReason,
		CreatedAt:     g.CreatedAt,
	}
	if len(g.AllowedTools) > 0 {
		out.AllowedTools = append([]string(nil), g.AllowedTools...)
	}
	out.Criteria = make([]goalCriterionDTO, len(g.Criteria))
	for i, c := range g.Criteria {
		out.Criteria[i] = goalCriterionDTO{
			Description: c.Description,
			Check:       c.Check,
			Satisfied:   c.Satisfied,
		}
	}
	return out
}

func toGoalIterationDTOs(items []host.GoalIteration) []goalIterationDTO {
	out := make([]goalIterationDTO, len(items))
	for i, it := range items {
		out[i] = goalIterationDTO{
			N:         it.N,
			Plan:      it.Plan,
			StateHash: it.StateHash,
			CostUSD:   it.CostUSD,
			Summary:   it.Summary,
		}
	}
	return out
}

func (s *Server) handleGoalsList(w http.ResponseWriter, r *http.Request) {
	api := s.goals()
	if api == nil {
		capabilityUnavailable(w, "goals")
		return
	}
	list, err := api.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	if list == nil {
		list = []host.Goal{}
	}
	out := make([]goalDTO, len(list))
	for i, g := range list {
		out[i] = toGoalDTO(g)
	}
	writeJSON(w, http.StatusOK, map[string]any{"goals": out})
}

func (s *Server) handleGoalsSet(w http.ResponseWriter, r *http.Request) {
	api := s.goals()
	if api == nil {
		capabilityUnavailable(w, "goals")
		return
	}
	var body goalSetRequest
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	desc := strings.TrimSpace(body.Description)
	if desc == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "description is required"})
		return
	}
	if len(body.Criteria) == 0 {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "at least one criterion is required"})
		return
	}
	g, err := api.Set(desc, body.Criteria, host.GoalSetOptions{
		MaxIterations:      body.MaxIterations,
		MaxCostUSD:         body.MaxCostUSD,
		MaxWallClockS:      body.MaxWallClockS,
		MaxNoProgressIters: body.MaxNoProgressIters,
		AllowedTools:       body.AllowedTools,
	})
	if err != nil {
		writeGoalErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toGoalDTO(g))
}

func (s *Server) handleGoalGet(w http.ResponseWriter, r *http.Request) {
	api := s.goals()
	if api == nil {
		capabilityUnavailable(w, "goals")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "id is required"})
		return
	}
	g, ok, err := api.Get(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "goal not found"})
		return
	}
	writeJSON(w, http.StatusOK, toGoalDTO(g))
}

func (s *Server) handleGoalRun(w http.ResponseWriter, r *http.Request) {
	api := s.goals()
	if api == nil {
		capabilityUnavailable(w, "goals")
		return
	}
	if !s.hasLive() {
		http.Error(w, "live session unavailable", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "id is required"})
		return
	}
	// Loop respects goal wall-clock / iteration budgets and request cancel.
	g, err := api.Run(r.Context(), id)
	if err != nil {
		writeGoalErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toGoalDTO(g))
}

func (s *Server) handleGoalPause(w http.ResponseWriter, r *http.Request) {
	s.handleGoalControl(w, r, func(api host.Goals, id string) (host.Goal, error) {
		return api.Pause(id)
	})
}

func (s *Server) handleGoalResume(w http.ResponseWriter, r *http.Request) {
	s.handleGoalControl(w, r, func(api host.Goals, id string) (host.Goal, error) {
		return api.Resume(id)
	})
}

func (s *Server) handleGoalAbort(w http.ResponseWriter, r *http.Request) {
	s.handleGoalControl(w, r, func(api host.Goals, id string) (host.Goal, error) {
		return api.Abort(id)
	})
}

func (s *Server) handleGoalControl(w http.ResponseWriter, r *http.Request, fn func(host.Goals, string) (host.Goal, error)) {
	api := s.goals()
	if api == nil {
		capabilityUnavailable(w, "goals")
		return
	}
	if !s.hasLive() {
		http.Error(w, "live session unavailable", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "id is required"})
		return
	}
	g, err := fn(api, id)
	if err != nil {
		writeGoalErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toGoalDTO(g))
}

func (s *Server) handleGoalLog(w http.ResponseWriter, r *http.Request) {
	api := s.goals()
	if api == nil {
		capabilityUnavailable(w, "goals")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "id is required"})
		return
	}
	iter := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("iter")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "iter must be a non-negative integer"})
			return
		}
		iter = n
	}
	items, err := api.Log(id, iter)
	if err != nil {
		writeGoalErr(w, err)
		return
	}
	if items == nil {
		items = []host.GoalIteration{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"iterations": toGoalIterationDTOs(items)})
}

func writeGoalErr(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeJSON(w, http.StatusRequestTimeout, opErrorResponse{Error: msg})
	case strings.Contains(msg, "not found"):
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: msg})
	case strings.Contains(msg, "invalid status"), strings.Contains(msg, "cannot abort"):
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: msg})
	default:
		// Validation / bad criteria / empty description from host.
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: msg})
	}
}
