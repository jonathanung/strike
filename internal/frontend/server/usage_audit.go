package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/internal/product/models"
	"github.com/jonathanung/strike/internal/trust/usageaudit"
)

// usageResponse is GET /v1/usage. Costs are omitted (costKnown false) when
// the model has no published price.
type usageResponse struct {
	usageaudit.Report
	BudgetSet   bool    `json:"budgetSet"`
	DailyUSD    float64 `json:"dailyUsd,omitempty"`
	PricingNote string  `json:"pricingNote,omitempty"`
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	price, note := s.usagePricer()
	report, err := usageaudit.Aggregate(s.opts.SessionDir, price)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	resp := usageResponse{Report: report, PricingNote: note}
	if log := s.usageLog(); log != nil {
		usd, set, err := log.DailyBudget()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
			return
		}
		resp.BudgetSet = set
		resp.DailyUSD = usd
		today := usageaudit.DayUsage{}
		day := time.Now().UTC().Format("2006-01-02")
		for _, d := range report.Days {
			if d.Day == day {
				today = d
				break
			}
		}
		resp.Alerts = append(resp.Alerts, usageaudit.DailyAlerts(today, usd, set, time.Now())...)
	}
	if resp.Sessions == nil {
		resp.Sessions = []usageaudit.SessionUsage{}
	}
	if resp.Days == nil {
		resp.Days = []usageaudit.DayUsage{}
	}
	if resp.Models == nil {
		resp.Models = []usageaudit.ModelUsage{}
	}
	if resp.Alerts == nil {
		resp.Alerts = []usageaudit.Alert{}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleUsageBudget(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "read-only"})
		return
	}
	log := s.usageLog()
	if log == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "usage audit log is not configured"})
		return
	}
	var body struct {
		DailyUSD float64 `json:"dailyUsd"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid budget: " + err.Error()})
		return
	}
	if math.IsNaN(body.DailyUSD) || math.IsInf(body.DailyUSD, 0) || body.DailyUSD < 0 {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "dailyUsd must be zero or a positive number"})
		return
	}
	if err := log.SetDailyBudget(body.DailyUSD); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	summary := "daily usage budget cleared"
	if body.DailyUSD > 0 {
		summary = "daily usage budget set"
	}
	if _, err := log.Append(usageaudit.Input{
		Action:  usageaudit.ActionConfig,
		Summary: summary,
		Detail:  map[string]any{"dailyUsd": body.DailyUSD},
	}); err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "budget saved but audit append failed: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dailyUsd": body.DailyUSD, "budgetSet": body.DailyUSD > 0})
}

func (s *Server) handleAuditList(w http.ResponseWriter, r *http.Request) {
	log := s.usageLog()
	if log == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"configured": false,
			"verified":   true,
			"entries":    []usageaudit.Entry{},
			"note":       "audit log is not configured",
		})
		return
	}
	entries, entErr := log.Entries()
	verErr := log.Verify()
	if entErr != nil && !chainDamage(entErr) {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: entErr.Error()})
		return
	}
	if verErr != nil && entErr == nil && !chainDamage(verErr) {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: verErr.Error()})
		return
	}
	if entries == nil {
		entries = []usageaudit.Entry{}
	}
	reason := ""
	switch {
	case entErr != nil:
		reason = entErr.Error()
	case verErr != nil:
		reason = verErr.Error()
	}
	// 200 so the page can show "Chain broken". /v1/audit/verify stays 409.
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": true,
		"verified":   entErr == nil && verErr == nil,
		"error":      reason,
		"entries":    entries,
	})
}

func chainDamage(err error) bool {
	var ve *usageaudit.VerifyError
	return errors.As(err, &ve)
}

func (s *Server) handleAuditVerify(w http.ResponseWriter, r *http.Request) {
	log := s.usageLog()
	if log == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "audit log is not configured"})
		return
	}
	entries, err := log.Entries()
	if err != nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
		return
	}
	if err := log.Verify(); err != nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "entries": len(entries)})
}

func (s *Server) handleAuditExport(w http.ResponseWriter, r *http.Request) {
	log := s.usageLog()
	if log == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "audit log is not configured"})
		return
	}
	exp, err := log.Export()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="strike-audit.json"`)
	writeJSON(w, http.StatusOK, exp)
}

func (s *Server) handleAuditAppend(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "read-only"})
		return
	}
	log := s.usageLog()
	if log == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "audit log is not configured"})
		return
	}
	var body struct {
		Action    string          `json:"action"`
		SessionID string          `json:"sessionId"`
		Summary   string          `json:"summary"`
		Detail    json.RawMessage `json:"detail"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	var detail any
	if len(body.Detail) > 0 && string(body.Detail) != "null" {
		if err := json.Unmarshal(body.Detail, &detail); err != nil {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "detail must be a JSON value"})
			return
		}
	}
	entry, err := log.Append(usageaudit.Input{
		Action:    body.Action,
		SessionID: body.SessionID,
		Summary:   body.Summary,
		Detail:    detail,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) usageLog() *usageaudit.Log {
	dir := strings.TrimSpace(s.opts.UsageAuditDir)
	if dir == "" {
		return nil
	}
	log, err := usageaudit.Open(dir)
	if err != nil {
		return nil
	}
	return log
}

// noteAudit records a control-plane action. It is best-effort when the log
// is not configured. The summary is redacted inside the chain.
func (s *Server) noteAudit(action, sessionID, summary string, detail any) {
	log := s.usageLog()
	if log == nil {
		return
	}
	_, _ = log.Append(usageaudit.Input{
		Action:    action,
		SessionID: sessionID,
		Summary:   summary,
		Detail:    detail,
	})
}

func (s *Server) noteTokenFile(tok string) {
	sum := tokenHash(tok)
	s.tokenMu.Lock()
	prev := s.tokenHash
	if prev == sum {
		s.tokenMu.Unlock()
		return
	}
	s.tokenHash = sum
	s.tokenMu.Unlock()
	if prev == "" {
		return
	}
	s.noteAudit(usageaudit.ActionToken, "", "serve observed a new daemon token file", map[string]string{"source": "token-file"})
}

func settingsPatchFields(body settingsPatchBody) []string {
	var out []string
	add := func(name, value string) {
		if strings.TrimSpace(value) != "" {
			out = append(out, name)
		}
	}
	add("provider", body.Provider)
	add("model", body.Model)
	add("agent", body.Agent)
	add("effort", body.Effort)
	add("mode", body.Mode)
	add("theme", body.Theme)
	add("notify", body.Notify)
	add("leanCode", body.LeanCode)
	add("deferTools", body.DeferTools)
	add("sessionWorktree", body.SessionWorktree)
	add("autoupdate", body.Autoupdate)
	add("permissionAutoApproveSeconds", body.PermissionAutoApproveSeconds)
	add("maxChildDepth", body.MaxChildDepth)
	add("compactionStrategy", body.CompactionStrategy)
	add("compactionModel", body.CompactionModel)
	add("compactionThreshold", body.CompactionThreshold)
	add("compactionBuffer", body.CompactionBuffer)
	add("keepUserTurns", body.KeepUserTurns)
	add("pruneProtectTokens", body.PruneProtectTokens)
	add("pruneMinimumTokens", body.PruneMinimumTokens)
	add("pruneKeepUserTurns", body.PruneKeepUserTurns)
	add("pruneProtectTools", body.PruneProtectTools)
	if body.PermissionAutoApproveExclude != nil {
		out = append(out, "permissionAutoApproveExclude")
	}
	return out
}

func tokenHash(tok string) string {
	// Hash only. The token itself never enters the audit log.
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

var (
	catalogMu   sync.Mutex
	catalogAt   time.Time
	catalogFn   usageaudit.Pricer
	catalogNote string
)

func (s *Server) usagePricer() (usageaudit.Pricer, string) {
	if s.opts.UsagePricer != nil {
		return s.opts.UsagePricer, ""
	}
	catalogMu.Lock()
	defer catalogMu.Unlock()
	if catalogFn != nil && time.Since(catalogAt) < 5*time.Minute {
		return catalogFn, catalogNote
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cat, err := models.Load(ctx)
	if err != nil {
		catalogFn = func(string, string) (usageaudit.Rates, bool) { return usageaudit.Rates{}, false }
		catalogNote = "Model catalog unavailable. Token counts are from usage events. Costs are omitted."
		catalogAt = time.Now()
		return catalogFn, catalogNote
	}
	catalogFn = func(provider, model string) (usageaudit.Rates, bool) {
		cost, ok := cat.Cost(provider, model)
		if !ok {
			return usageaudit.Rates{}, false
		}
		return usageaudit.Rates{
			Input:           cost.Input,
			Output:          cost.Output,
			CacheRead:       cost.CacheRead,
			CacheWrite:      cost.CacheWrite,
			CacheReadKnown:  cost.CacheRead > 0,
			CacheWriteKnown: cost.CacheWrite > 0,
		}, true
	}
	catalogNote = ""
	catalogAt = time.Now()
	return catalogFn, catalogNote
}
