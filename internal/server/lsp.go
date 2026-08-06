package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/jonathanung/strike-cli/internal/host"
)

// --- LSP / diagnostics DTOs (web-safe camelCase) ---

type lspServerItem struct {
	Name       string   `json:"name"`
	Command    string   `json:"command,omitempty"`
	State      string   `json:"state"`
	Extensions []string `json:"extensions,omitempty"`
	Error      string   `json:"error,omitempty"`
	OpenDocs   int      `json:"openDocs,omitempty"`
}

type lspStatusResponse struct {
	Servers []lspServerItem `json:"servers"`
	Note    string          `json:"note,omitempty"`
}

type lspRetryRequest struct {
	// Name selects one server; empty retries every non-up server.
	Name string `json:"name"`
}

type diagnosticItem struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
	Severity  string `json:"severity"`
	Source    string `json:"source,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message"`
}

type diagnosticsResponse struct {
	Diagnostics []diagnosticItem `json:"diagnostics"`
	Count       int              `json:"count"`
	Note        string           `json:"note,omitempty"`
}

func (s *Server) lsp() host.LSP {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.LSP
}

func (s *Server) handleLSP(w http.ResponseWriter, r *http.Request) {
	svc := s.lsp()
	if svc == nil {
		capabilityUnavailable(w, "lsp")
		return
	}
	writeJSON(w, http.StatusOK, lspStatusPayload(svc))
}

func (s *Server) handleLSPRetry(w http.ResponseWriter, r *http.Request) {
	svc := s.lsp()
	if svc == nil {
		capabilityUnavailable(w, "lsp")
		return
	}
	var body lspRetryRequest
	// Empty body is allowed (retry all non-up servers).
	r.Body = http.MaxBytesReader(w, r.Body, maxHTTPPayload)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil && err != io.EOF {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if err := svc.Retry(strings.TrimSpace(body.Name)); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleLSPDisable(w http.ResponseWriter, r *http.Request) {
	svc := s.lsp()
	if svc == nil {
		capabilityUnavailable(w, "lsp")
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "name is required"})
		return
	}
	if err := svc.Disable(name); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	svc := s.lsp()
	if svc == nil {
		capabilityUnavailable(w, "lsp")
		return
	}
	writeJSON(w, http.StatusOK, diagnosticsPayload(svc))
}

func lspStatusPayload(svc host.LSP) lspStatusResponse {
	raw := svc.Statuses()
	out := make([]lspServerItem, 0, len(raw))
	live := 0
	for _, st := range raw {
		item := lspServerItem{
			Name:       st.Name,
			Command:    st.Command,
			State:      st.State,
			Extensions: append([]string(nil), st.Extensions...),
			Error:      st.Error,
			OpenDocs:   st.OpenDocs,
		}
		if item.Extensions == nil {
			item.Extensions = []string{}
		}
		out = append(out, item)
		if st.State == "up" {
			live++
		}
	}
	resp := lspStatusResponse{Servers: out}
	if len(out) == 0 {
		resp.Note = "no language servers configured (add lsp.servers in config)"
	} else if live == 0 {
		resp.Note = "no live language servers (see servers status; try retry)"
	}
	return resp
}

func diagnosticsPayload(svc host.LSP) diagnosticsResponse {
	raw := svc.Diagnostics()
	out := make([]diagnosticItem, 0, len(raw))
	for _, d := range raw {
		sev := strings.TrimSpace(d.Severity)
		if sev == "" {
			sev = "error"
		}
		out = append(out, diagnosticItem{
			Path:      d.Path,
			Line:      d.Line,
			Character: d.Character,
			Severity:  sev,
			Source:    d.Source,
			Code:      d.Code,
			Message:   d.Message,
		})
	}
	resp := diagnosticsResponse{Diagnostics: out, Count: len(out)}
	// Soft empty notes match the diagnostics tool / LSP status surface.
	statuses := svc.Statuses()
	live := 0
	for _, st := range statuses {
		if st.State == "up" {
			live++
		}
	}
	if len(statuses) == 0 {
		resp.Note = "no language servers configured (add lsp.servers in config)"
	} else if live == 0 {
		resp.Note = "no live language servers (see servers status; try retry)"
	} else if len(out) == 0 {
		resp.Note = "no diagnostics"
	}
	return resp
}
