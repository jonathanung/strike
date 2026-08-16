package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/persist/session"
	"github.com/jonathanung/strike-cli/pkg/timeline"
)

// handleSessionTimeline returns a redacted structured run timeline for a session.
// Derived from durable JSONL (complement to the full transcript, not a second log).
func (s *Server) handleSessionTimeline(w http.ResponseWriter, r *http.Request) {
	tr, err := s.sessionTrace(r.PathValue("id"))
	if err != nil {
		writeTimelineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tr)
}

// handleSessionTimelineExport streams a redacted timeline download (JSON or JSONL).
// Query: format=json|jsonl (default json).
func (s *Server) handleSessionTimelineExport(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	tr, err := s.sessionTrace(id)
	if err != nil {
		writeTimelineError(w, err)
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}
	switch format {
	case "json":
		name := fmt.Sprintf("strike-timeline-%s.json", sanitizeFilename(id))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(true)
		enc.SetIndent("", "  ")
		_ = enc.Encode(tr)
	case "jsonl":
		name := fmt.Sprintf("strike-timeline-%s.jsonl", sanitizeFilename(id))
		w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		enc := json.NewEncoder(w)
		header := map[string]any{
			"type":          "timeline.header",
			"schemaVersion": tr.SchemaVersion,
			"sessionId":     tr.SessionID,
			"exportedAt":    tr.ExportedAt,
			"redacted":      true,
			"summary":       tr.Summary,
			"note":          tr.Note,
		}
		if err := enc.Encode(header); err != nil {
			return
		}
		for _, ent := range tr.Entries {
			row := struct {
				Type string `json:"type"`
				timeline.Entry
			}{Type: "timeline.entry", Entry: ent}
			if err := enc.Encode(row); err != nil {
				return
			}
		}
	default:
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "format must be json or jsonl"})
	}
}

func (s *Server) sessionTrace(id string) (timeline.Trace, error) {
	id = strings.TrimSpace(id)
	if err := validateSessionID(id); err != nil {
		return timeline.Trace{}, err
	}
	path := session.LogPath(s.opts.SessionDir, id)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return timeline.Trace{}, errSessionNotFound
		}
		return timeline.Trace{}, fmt.Errorf("session unavailable: %w", err)
	}
	tr, err := session.BuildTrace(path, timeline.Options{SessionID: id})
	if err != nil {
		return timeline.Trace{}, err
	}
	// Defensive: Trace() already redacts field-level strings; keep flag honest.
	tr.Redacted = true
	if tr.ExportedAt.IsZero() {
		tr.ExportedAt = time.Now().UTC()
	}
	return tr, nil
}

var errSessionNotFound = errors.New("session not found")

func writeTimelineError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errSessionNotFound) || os.IsNotExist(err):
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "session not found"})
	case err != nil && (strings.Contains(err.Error(), "session id") || strings.Contains(err.Error(), "invalid session")):
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "timeline unavailable"})
	}
}

func sanitizeFilename(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "session"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "session"
	}
	return out
}
