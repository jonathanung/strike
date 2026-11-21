package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
)

// filesGrepTimeout bounds one content search; a search cut short by it
// reports capped instead of failing.
const filesGrepTimeout = 5 * time.Second

// filesGrepResponse is the GET /v1/files/grep body.
type filesGrepResponse struct {
	Query        string           `json:"query"`
	Limit        int              `json:"limit"`
	Matches      []host.GrepMatch `json:"matches"`
	Capped       bool             `json:"capped"`
	FilesScanned int              `json:"filesScanned"`
}

// handleFilesGrep is GET /v1/files/grep?q=&limit= (#1406): plain-text,
// case-insensitive substring search over the work root's files. Root
// scoping, the allowed-roots ceiling, binary/VCS skipping and walk caps are
// the files service's (host.ContentSearcher); this handler only parses and
// bounds the request.
func (s *Server) handleFilesGrep(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Files == nil {
		capabilityUnavailable(w, "files")
		return
	}
	searcher, ok := s.opts.Services.Files.(host.ContentSearcher)
	if !ok {
		capabilityUnavailable(w, "file content search")
		return
	}
	q := r.URL.Query()
	query := q.Get("q")
	if strings.TrimSpace(query) == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "q is required"})
		return
	}
	limit := 0
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), filesGrepTimeout)
	defer cancel()
	res, err := searcher.GrepFiles(ctx, query, limit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	matches := res.Matches
	if matches == nil {
		matches = []host.GrepMatch{}
	}
	writeJSON(w, http.StatusOK, filesGrepResponse{
		Query:        query,
		Limit:        limit,
		Matches:      matches,
		Capped:       res.Capped,
		FilesScanned: res.FilesScanned,
	})
}
