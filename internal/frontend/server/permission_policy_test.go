package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
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

	// Invalid glob syntax is rejected with 400 and nothing is persisted:
	// evaluation would treat the pattern as never-matching, so storing it
	// would leave an intended deny silently inert (fail-open under yolo).
	rec = post("/v1/permissions/rules", map[string]any{
		"scope": "global", "permission": "bash", "pattern": "[r*", "action": "deny", "confirm": true,
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid glob syntax") || !strings.Contains(rec.Body.String(), "[r*") {
		t.Fatalf("invalid glob: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/permissions/rules", nil))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "[r*") {
		t.Fatalf("invalid glob persisted: %d %s", rec.Code, rec.Body.String())
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

func TestPermissionRulesSessionFolderGrants(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	perms := &testPermissions{
		folderGrants: []host.PermissionFolderGrant{
			{Index: 0, Permission: "write", Folder: "docs", Pattern: "docs/**"},
			{Index: 1, Permission: "edit", Folder: "src/api", Pattern: "src/api/**"},
		},
	}
	ops := make(chan protocol.Op, 4)
	live := NewLive("s1", t.TempDir(), nil, ops)
	srv, err := New(Options{
		SessionDir:   t.TempDir(),
		PollInterval: 20 * time.Millisecond,
		Live:         live,
		Services:     &host.Services{Permissions: perms},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/permissions/rules", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"scope":"session folder"`, `"pattern":"docs/**"`, `"pattern":"src/api/**"`, `"permission":"write"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("list body missing %s: %s", want, body)
		}
	}

	post := func(body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/permissions/rules/remove", bytes.NewReader(raw)))
		return rec
	}

	// Out-of-range index is a 400 and removes nothing.
	rec = post(map[string]any{"scope": "session folder", "index": 7})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("remove out of range: %d %s", rec.Code, rec.Body.String())
	}
	if len(perms.folderGrants) != 2 {
		t.Fatalf("grants after bad remove = %d, want 2", len(perms.folderGrants))
	}

	// Removing index 0 revokes that grant; the response lists the rest.
	rec = post(map[string]any{"scope": "session folder", "index": 0})
	if rec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	if len(perms.folderGrants) != 1 || perms.folderGrants[0].Folder != "src/api" {
		t.Fatalf("grants after remove = %+v", perms.folderGrants)
	}
	if strings.Contains(rec.Body.String(), `"pattern":"docs/**"`) {
		t.Fatalf("removed grant still listed: %s", rec.Body.String())
	}
}
