package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/pkg/diag"
)

// handleDiag exports a redacted prompt/config diagnostic bundle for the live
// root (GET or POST). Capability-gated: 503 when no live engine is available.
// Response body matches TUI /diag JSON (pkg/diag.Bundle) with Content-Disposition
// for browser download.
func (s *Server) handleDiag(w http.ResponseWriter, r *http.Request) {
	if !s.hasLive() {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "diag capability unavailable on this host"})
		return
	}
	live := s.resolveLive(w, r)
	if live == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "diag capability unavailable on this host"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ev, err := live.RequestDiagnostic(ctx)
	if err != nil {
		if ctx.Err() != nil {
			writeJSON(w, http.StatusGatewayTimeout, opErrorResponse{Error: "diagnostic bundle timed out"})
			return
		}
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
		return
	}

	// Defense in depth: re-run pkg/diag.Build so secrets are scrubbed even if a
	// non-redacted event slipped through (same posture as TUI export).
	bundle := diag.FromProtocol(ev)
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "encode diagnostic bundle: " + err.Error()})
		return
	}
	data = append(data, '\n')

	name := diagDownloadName(live.SessionID(), bundle.ExportedAt)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func diagDownloadName(sessionID string, exportedAt time.Time) string {
	if exportedAt.IsZero() {
		exportedAt = time.Now().UTC()
	}
	stamp := exportedAt.UTC().Format("20060102-150405")
	short := shortSessionID(sessionID)
	if short == "" {
		short = "session"
	}
	return fmt.Sprintf("strike-diag-%s-%s.json", short, stamp)
}

func shortSessionID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	// Prefer trailing segment after last non-alnum separator.
	for i := len(id) - 1; i >= 0; i-- {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		if i+1 < len(id) {
			id = id[i+1:]
		}
		break
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
