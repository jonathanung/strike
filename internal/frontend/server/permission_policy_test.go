package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/trust/audit"
	"github.com/jonathanung/strike/pkg/telemetry"
)

func TestPermissionRulesRequireConfirm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	ops := make(chan protocol.Op, 4)
	live := NewLive("s1", work, nil, ops)
	sink, err := audit.Open(audit.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	if err := sink.Record(audit.FamilyPermission, "s1", "", "", "", telemetry.PermissionEvent{
		Permission: "bash",
		Action:     "deny",
		Decision:   "reject",
		Patterns:   []string{"rm -rf tmp"},
		Layer:      "config",
		RuleAction: "deny",
		SessionID:  "s1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Record(audit.FamilyServeOp, "", "", "", "", map[string]string{"type": "interrupt"}); err != nil {
		t.Fatal(err)
	}

	srv, err := New(Options{
		SessionDir:   t.TempDir(),
		PollInterval: 20 * time.Millisecond,
		Live:         live,
		Audit:        sink,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/permissions/rules", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"globalMode":"default"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Deny rules still apply") || !strings.Contains(rec.Body.String(), `"bash"`) {
		t.Fatalf("list body = %s", rec.Body.String())
	}

	post := func(path string, body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		h.ServeHTTP(rec, req)
		return rec
	}

	rec = post("/v1/permissions/rules", map[string]any{
		"scope": "global", "permission": "bash", "pattern": "rm *", "action": "deny",
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"action":"deny"`) {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body.String())
	}
	rec = post("/v1/permissions/rules", map[string]any{
		"scope": "global", "permission": "bash", "pattern": "*", "action": "allow",
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "confirmation required") {
		t.Fatalf("allow: %d %s", rec.Code, rec.Body.String())
	}
	rec = post("/v1/permissions/rules", map[string]any{
		"scope": "project", "permission": "bash", "pattern": "git status", "action": "allow", "confirm": true,
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"scope":"project"`) {
		t.Fatalf("project allow: %d %s", rec.Code, rec.Body.String())
	}
	rec = post("/v1/permissions/rules", map[string]any{
		"scope": "global", "permission": "nope", "pattern": "*", "action": "deny", "confirm": true,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown permission: %d %s", rec.Code, rec.Body.String())
	}
	rec = post("/v1/permissions/rules/remove", map[string]any{"scope": "global", "index": 0})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "confirmation required") {
		t.Fatalf("remove deny: %d %s", rec.Code, rec.Body.String())
	}
	rec = post("/v1/permissions/rules/remove", map[string]any{"scope": "global", "index": 0, "confirm": true})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"pattern":"rm *"`) {
		t.Fatalf("removed deny still present: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/permissions/audit?limit=10", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"available":true`) {
		t.Fatalf("audit: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"action":"deny"`) || strings.Contains(rec.Body.String(), "interrupt") {
		t.Fatalf("audit body = %s", rec.Body.String())
	}

	ro, err := New(Options{SessionDir: t.TempDir(), PollInterval: time.Millisecond, Live: live, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	raw, _ := json.Marshal(map[string]any{"scope": "global", "permission": "bash", "pattern": "rm *", "action": "deny"})
	ro.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/permissions/rules", bytes.NewReader(raw)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("readonly: %d %s", rec.Code, rec.Body.String())
	}
}
