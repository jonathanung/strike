package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/persist/memory"
)

func (s *Server) handleKnowledgeList(w http.ResponseWriter, r *http.Request) {
	k := s.knowledgeService(w)
	if k == nil {
		return
	}
	scope, err := memoryScope(r.URL.Query().Get("scope"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	pins, err := k.List(scope)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if pins == nil {
		pins = []host.KnowledgePin{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pins":         pins,
		"scope":        scope,
		"markdownFile": k.Files(scope),
	})
}

func (s *Server) handleKnowledgePut(w http.ResponseWriter, r *http.Request) {
	k := s.knowledgeService(w)
	if k == nil {
		return
	}
	if !s.requireMutable(w) {
		return
	}
	var body struct {
		Path   string `json:"path"`
		Note   string `json:"note"`
		Author string `json:"author"`
		Scope  string `json:"scope"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	scope, err := memoryScope(body.Scope)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	pin, err := k.Put(scope, body.Path, body.Note, body.Author)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, pin)
}

func (s *Server) handleKnowledgeDelete(w http.ResponseWriter, r *http.Request) {
	k := s.knowledgeService(w)
	if k == nil {
		return
	}
	if !s.requireMutable(w) {
		return
	}
	var body struct {
		Path  string `json:"path"`
		Scope string `json:"scope"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeBody(w, r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
			return
		}
	}
	if body.Path == "" {
		body.Path = r.URL.Query().Get("path")
	}
	if body.Scope == "" {
		body.Scope = r.URL.Query().Get("scope")
	}
	scope, err := memoryScope(body.Scope)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if err := k.Delete(scope, body.Path); err != nil {
		status := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, opErrorResponse{Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) knowledgeService(w http.ResponseWriter) host.Knowledge {
	if s.opts.Services == nil || s.opts.Services.Knowledge == nil {
		capabilityUnavailable(w, "knowledge")
		return nil
	}
	return s.opts.Services.Knowledge
}

func (s *Server) handleMemoryModeGet(w http.ResponseWriter, r *http.Request) {
	live := s.resolveLive(nil, r)
	if live == nil {
		writeJSON(w, http.StatusOK, map[string]any{"mode": memory.ModeNormal, "wired": false})
		return
	}
	mode, ok := live.MemoryMode()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"mode": memory.ModeNormal, "wired": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "wired": true})
}

func (s *Server) handleMemoryModePut(w http.ResponseWriter, r *http.Request) {
	if !s.requireMutable(w) {
		return
	}
	live := s.resolveLive(nil, r)
	if live == nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "no live session"})
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid JSON"})
		return
	}
	mode, err := memory.NormalizeMode(body.Mode)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if err := live.SetMemoryMode(mode); err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not wired") {
			status = http.StatusNotImplemented
		}
		writeJSON(w, status, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": mode, "wired": true})
}
