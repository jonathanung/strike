package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/jonathanung/strike-cli/internal/host"
)

func (s *Server) mcp() host.MCP {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.MCP
}

func (s *Server) handleMCPList(w http.ResponseWriter, r *http.Request) {
	m := s.mcp()
	if m == nil {
		capabilityUnavailable(w, "mcp")
		return
	}
	list := m.Statuses()
	if list == nil {
		list = []host.MCPServerStatus{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": list})
}

func (s *Server) handleMCPRetry(w http.ResponseWriter, r *http.Request) {
	m := s.mcp()
	if m == nil {
		capabilityUnavailable(w, "mcp")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	// Optional JSON body: empty or {} retries every non-up server.
	if err := decodeOptionalBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if err := m.Retry(strings.TrimSpace(body.Name)); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

// decodeOptionalBody decodes at most one JSON object. Empty bodies are OK.
func decodeOptionalBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxHTTPPayload)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}
	return ensureEOF(dec)
}

func (s *Server) handleMCPDisable(w http.ResponseWriter, r *http.Request) {
	m := s.mcp()
	if m == nil {
		capabilityUnavailable(w, "mcp")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "name is required"})
		return
	}
	if err := m.Disable(name); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}
