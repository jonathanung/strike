package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/trust/audit"
	"github.com/jonathanung/strike/pkg/telemetry"
)

const workspaceModeNote = "Stored on the workspace only. Activating a workspace does not change the live session. Apply it here if this session should use it."

type permissionRuleBody struct {
	Scope      string `json:"scope"`
	Permission string `json:"permission"`
	Pattern    string `json:"pattern"`
	Action     string `json:"action"`
	Index      int    `json:"index"`
	Confirm    bool   `json:"confirm"`
}

func (s *Server) handlePermissionRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.writePermissionRules(w, r)
	case http.MethodPost:
		s.appendPermissionRule(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, opErrorResponse{Error: "method not allowed"})
	}
}

func (s *Server) handlePermissionRuleRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, opErrorResponse{Error: "method not allowed"})
		return
	}
	if !s.allowPermissionWrite(w) {
		return
	}
	var body permissionRuleBody
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	err := config.DeletePermissionRule(s.currentCWD(r), strings.TrimSpace(body.Scope), body.Index, body.Confirm)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	s.writePermissionRules(w, r)
}

func (s *Server) appendPermissionRule(w http.ResponseWriter, r *http.Request) {
	if !s.allowPermissionWrite(w) {
		return
	}
	var body permissionRuleBody
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	rule := permission.Rule{
		Permission: body.Permission,
		Pattern:    body.Pattern,
		Action:     permission.Action(body.Action),
	}
	err := config.AppendPermissionRule(s.currentCWD(r), strings.TrimSpace(body.Scope), rule, body.Confirm)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	s.writePermissionRules(w, r)
}

func (s *Server) allowPermissionWrite(w http.ResponseWriter) bool {
	if s.opts.ReadOnly {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "read-only attach mode does not allow mutations"})
		return false
	}
	return s.requireMutable(w)
}

func (s *Server) writePermissionRules(w http.ResponseWriter, r *http.Request) {
	view, err := config.ReadPermissionRules(s.currentCWD(r))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	modes := make([]string, 0, len(protocol.PermissionModes()))
	for _, mode := range protocol.PermissionModes() {
		modes = append(modes, string(mode))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workDir":          view.WorkDir,
		"globalMode":       view.GlobalMode,
		"projectMode":      view.ProjectMode,
		"effectiveMode":    view.EffectiveMode,
		"rules":            view.Rules,
		"knownPermissions": permission.KnownNames(),
		"modes":            modes,
		"note":             view.Note,
		"workspaceNote":    workspaceModeNote,
	})
}

type auditRow struct {
	Time       string   `json:"time"`
	SessionID  string   `json:"sessionId,omitempty"`
	Permission string   `json:"permission,omitempty"`
	Action     string   `json:"action,omitempty"`
	Decision   string   `json:"decision,omitempty"`
	Patterns   []string `json:"patterns,omitempty"`
	Layer      string   `json:"layer,omitempty"`
	RuleAction string   `json:"ruleAction,omitempty"`
}

func (s *Server) handlePermissionAudit(w http.ResponseWriter, r *http.Request) {
	dir := ""
	if src, ok := s.opts.Audit.(interface{ Dir() string }); ok && src != nil {
		dir = src.Dir()
	}
	if dir == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"records":   []auditRow{},
			"note":      "No audit sink is attached to this daemon.",
		})
		return
	}
	limit := 40
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := parseAuditLimit(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "limit must be a number"})
			return
		}
		limit = n
	}
	recs, err := audit.ReadFamily(dir, audit.FamilyPermission, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	rows := make([]auditRow, 0, len(recs))
	for _, rec := range recs {
		rows = append(rows, auditRowFrom(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"records":   rows,
		"note":      "Past permission evaluations. Patterns are stored redacted.",
	})
}

func parseAuditLimit(raw string) (int, error) {
	n := 0
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, errors.New("limit")
		}
		n = n*10 + int(c-'0')
		if n > 200 {
			return 200, nil
		}
	}
	if n == 0 {
		return 0, errors.New("limit")
	}
	return n, nil
}

func auditRowFrom(rec audit.Record) auditRow {
	row := auditRow{Time: rec.Time.UTC().Format(time.RFC3339)}
	if rec.SessionID != "" {
		row.SessionID = rec.SessionID
	}
	var payload telemetry.PermissionEvent
	if err := json.Unmarshal(rec.Payload, &payload); err != nil {
		row.Action = "recorded"
		return row
	}
	row.Permission = payload.Permission
	row.Action = payload.Action
	row.Decision = payload.Decision
	row.Patterns = payload.Patterns
	row.Layer = payload.Layer
	row.RuleAction = payload.RuleAction
	if row.SessionID == "" {
		row.SessionID = payload.SessionID
	}
	return row
}
