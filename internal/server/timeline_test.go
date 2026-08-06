package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/session"
	"github.com/jonathanung/strike-cli/pkg/timeline"
)

func writeTimelineSession(t *testing.T, dir, id, secret string) {
	t.Helper()
	store, err := session.Open(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	corr := protocol.Correlation{SessionID: id, TurnID: "turn-1"}
	for _, ev := range []protocol.Event{
		protocol.TurnStarted{Correlation: corr},
		protocol.ToolCallBegin{
			Correlation: corr,
			CallID:      "c1",
			Name:        "bash",
			Args:        json.RawMessage(`{"command":"echo ` + secret + `"}`),
		},
		protocol.ToolCallEnd{
			Correlation: corr,
			CallID:      "c1",
			Output:      "OPENAI_API_KEY=sk-proj-nested-web-timeline-99\n" + secret,
		},
		protocol.TurnCompleted{Correlation: corr, StopReason: "end_turn"},
	} {
		if err := store.Append(ev); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionTimelineSnapshotAndRedaction(t *testing.T) {
	dir := t.TempDir()
	secret := "sk-ant-api03-WEBTIMELINELEAKVALUE99"
	writeTimelineSession(t, dir, "sess-tl", secret)

	srv, err := New(Options{SessionDir: dir, Auth: true, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	// Unauthorized
	unauth := httptest.NewRecorder()
	srv.Handler().ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/v1/sessions/sess-tl/timeline", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d", unauth.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/sess-tl/timeline", nil)
	req.Header.Set("Authorization", "Bearer secret")
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, banned := range []string{secret, "sk-proj-nested-web-timeline-99"} {
		if strings.Contains(body, banned) {
			t.Errorf("timeline leaked %q", banned)
		}
	}
	var tr timeline.Trace
	if err := json.Unmarshal(res.Body.Bytes(), &tr); err != nil {
		t.Fatal(err)
	}
	if !tr.Redacted {
		t.Fatal("expected redacted=true")
	}
	if tr.SessionID != "sess-tl" {
		t.Fatalf("sessionId = %q", tr.SessionID)
	}
	if tr.Summary.Turns != 1 || tr.Summary.Tools != 1 {
		t.Fatalf("summary = %+v", tr.Summary)
	}
	if len(tr.Entries) == 0 {
		t.Fatal("expected entries")
	}
	// Collapsed fields present when known.
	var sawTool bool
	for _, e := range tr.Entries {
		if e.Kind == timeline.KindTool {
			sawTool = true
			if e.State == "" {
				t.Fatalf("tool missing state: %+v", e)
			}
			if e.Name != "bash" {
				t.Fatalf("tool name = %q", e.Name)
			}
		}
	}
	if !sawTool {
		t.Fatal("expected tool entry")
	}
}

func TestSessionTimelineExportJSONAndJSONL(t *testing.T) {
	dir := t.TempDir()
	secret := "sk-ant-api03-EXPORTBOUNDARYVALUE88"
	writeTimelineSession(t, dir, "export-me", secret)

	srv, err := New(Options{SessionDir: dir, Auth: true, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}

	// JSON export
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/export-me/timeline/export?format=json", nil)
	req.Header.Set("Authorization", "Bearer t")
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("json export status = %d body=%s", res.Code, res.Body.String())
	}
	if cd := res.Header().Get("Content-Disposition"); !strings.Contains(cd, "strike-timeline-export-me.json") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	if strings.Contains(res.Body.String(), secret) {
		t.Fatal("json export leaked secret")
	}
	var tr timeline.Trace
	if err := json.Unmarshal(res.Body.Bytes(), &tr); err != nil {
		t.Fatal(err)
	}
	if !tr.Redacted || tr.Summary.Tools != 1 {
		t.Fatalf("json export trace = %+v", tr)
	}

	// JSONL export
	req2 := httptest.NewRequest(http.MethodGet, "/v1/sessions/export-me/timeline/export?format=jsonl", nil)
	req2.Header.Set("Authorization", "Bearer t")
	res2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res2, req2)
	if res2.Code != http.StatusOK {
		t.Fatalf("jsonl export status = %d body=%s", res2.Code, res2.Body.String())
	}
	if cd := res2.Header().Get("Content-Disposition"); !strings.Contains(cd, ".jsonl") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	body := res2.Body.String()
	if strings.Contains(body, secret) {
		t.Fatal("jsonl export leaked secret")
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) < 2 {
		t.Fatalf("jsonl lines = %d", len(lines))
	}
	var header map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header["type"] != "timeline.header" || header["redacted"] != true {
		t.Fatalf("header = %+v", header)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["type"] != "timeline.entry" {
		t.Fatalf("entry type = %v", entry["type"])
	}

	// Bad format
	bad := httptest.NewRequest(http.MethodGet, "/v1/sessions/export-me/timeline/export?format=xml", nil)
	bad.Header.Set("Authorization", "Bearer t")
	badRes := httptest.NewRecorder()
	srv.Handler().ServeHTTP(badRes, bad)
	if badRes.Code != http.StatusBadRequest {
		t.Fatalf("bad format status = %d", badRes.Code)
	}
}

func TestSessionTimelineNotFoundAndInvalidID(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	missing := httptest.NewRecorder()
	srv.Handler().ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/sessions/nope/timeline", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing = %d", missing.Code)
	}

	// Path separators / empty ids are rejected before filesystem access.
	for _, id := range []string{"", "foo/bar", `foo\bar`, "..", "a/../b"} {
		if _, err := srv.sessionTrace(id); err == nil {
			t.Fatalf("expected invalid session id error for %q", id)
		}
	}
}

func TestBootstrapDeclaresTimelineCapability(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), `"timeline":true`) {
		t.Fatalf("bootstrap missing timeline capability: %s", res.Body.String())
	}
}
