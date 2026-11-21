package server

import (
	"net/http"

	"github.com/jonathanung/strike/internal/persist/skillusage"
	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/pkg/redact"
)

// handleSkillUsage serves per-skill run counts for Customize → Skills
// (#1442). The skill list is the same catalog GET /v1/customize returns for
// this workspace. Counts come from the recorded session logs in SessionDir,
// computed on request, and never leave the gateway.
func (s *Server) handleSkillUsage(w http.ResponseWriter, r *http.Request) {
	cat, err := config.LoadCustomize(s.customizeDir(r))
	if err != nil {
		customizeFail(w, err)
		return
	}
	names := make([]string, 0, len(cat.Skills))
	for _, sk := range cat.Skills {
		names = append(names, sk.Name)
	}
	report, err := skillusage.Aggregate(s.opts.SessionDir, names)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: redact.String(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, report)
}
