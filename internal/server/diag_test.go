package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/pkg/diag"
)

func TestDiagUnavailableWithoutLive(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/v1/diag", nil)
		srv.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusServiceUnavailable {
			t.Errorf("%s /v1/diag = %d, want 503", method, res.Code)
		}
		if !strings.Contains(res.Body.String(), "diag capability unavailable") {
			t.Errorf("%s body = %q", method, res.Body.String())
		}
	}
	// Bootstrap advertises diag=false in attach-only.
	boot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if !strings.Contains(boot.Body.String(), `"diag":false`) {
		t.Errorf("bootstrap missing diag:false: %s", boot.Body.String())
	}
}

func TestDiagExportHappyPathAndRedaction(t *testing.T) {
	const secret = "sk-ant-api03-SUPERSECRETWEBDIAGKEY99"
	ops := make(chan protocol.Op, 1)
	live := NewLive("sess-web-diag-abcdef", "/tmp/work", nil, ops)
	defer live.Close()

	// Engine stub: answer inspect.diagnostic with a bundle that still contains
	// a credential-shaped preview (server must scrub via diag.FromProtocol).
	go func() {
		op, ok := <-ops
		if !ok {
			return
		}
		if _, ok := op.(protocol.InspectDiagnosticBundle); !ok {
			t.Errorf("op = %#v, want InspectDiagnosticBundle", op)
			return
		}
		live.Publish(protocol.DiagnosticBundle{
			SchemaVersion: diag.SchemaVersion,
			ExportedAt:    time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
			Redacted:      false, // force server-side scrub path
			Session: protocol.DiagnosticSession{
				SessionID: "sess-web-diag-abcdef",
			},
			Prompt: protocol.DiagnosticPrompt{
				Layers: []protocol.PromptLayerInfo{
					{
						Kind:    protocol.PromptLayerPersona,
						Source:  "agent:build",
						Mode:    protocol.PromptLayerReplace,
						Chars:   40,
						Preview: "persona key=" + secret,
					},
				},
				LayerCount:  1,
				SystemChars: 40,
			},
			Config: protocol.DiagnosticConfig{
				Provider: "echo",
				Model:    "echo",
				Agent:    "build",
			},
		})
	}()

	srv, err := New(Options{SessionDir: t.TempDir(), Live: live})
	if err != nil {
		t.Fatal(err)
	}

	// Capability advertised when live is present.
	boot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if !strings.Contains(boot.Body.String(), `"diag":true`) {
		t.Fatalf("bootstrap missing diag:true: %s", boot.Body.String())
	}

	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/diag", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("GET /v1/diag = %d %s", res.Code, res.Body.String())
	}
	ct := res.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	cd := res.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "strike-diag-") || !strings.HasSuffix(strings.Trim(cd, `"`), ".json") && !strings.Contains(cd, ".json") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	body := res.Body.String()
	if strings.Contains(body, secret) || strings.Contains(body, "sk-ant-api03-") {
		t.Fatalf("response leaked secret: %s", body)
	}
	if !strings.Contains(body, "REDACTED") {
		t.Fatalf("expected redaction placeholder in body: %s", body)
	}
	if !strings.Contains(body, `"redacted": true`) && !strings.Contains(body, `"redacted":true`) {
		t.Fatalf("expected redacted=true: %s", body)
	}
	if !strings.Contains(body, `"schemaVersion"`) || !strings.Contains(body, `"layers"`) {
		t.Fatalf("bundle shape missing fields: %s", body)
	}
	var parsed diag.Bundle
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("decode bundle: %v\n%s", err, body)
	}
	if !parsed.Redacted || parsed.Prompt.LayerCount != 1 {
		t.Fatalf("parsed = %+v", parsed)
	}
	if len(parsed.Prompt.Layers) != 1 || strings.Contains(parsed.Prompt.Layers[0].Preview, secret) {
		t.Fatalf("layer preview not scrubbed: %+v", parsed.Prompt.Layers)
	}
}

func TestDiagPOSTAndRootScope(t *testing.T) {
	ops1 := make(chan protocol.Op, 1)
	ops2 := make(chan protocol.Op, 1)
	live1 := NewLive("r1", "/a", nil, ops1)
	live2 := NewLive("r2", "/b", nil, ops2)
	defer live1.Close()
	defer live2.Close()

	hub := NewLiveHub(nil, nil)
	hub.Add("r1", live1)
	hub.Add("r2", live2)

	answer := func(ops <-chan protocol.Op, live *Live, sid string) {
		op := <-ops
		if _, ok := op.(protocol.InspectDiagnosticBundle); !ok {
			t.Errorf("unexpected op %#v", op)
			return
		}
		live.Publish(protocol.DiagnosticBundle{
			SchemaVersion: diag.SchemaVersion,
			ExportedAt:    time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			Redacted:      true,
			Session:       protocol.DiagnosticSession{SessionID: sid},
			Prompt:        protocol.DiagnosticPrompt{LayerCount: 0},
			Config:        protocol.DiagnosticConfig{Provider: "echo"},
		})
	}
	go answer(ops2, live2, "r2")

	srv, err := New(Options{SessionDir: t.TempDir(), LiveHub: hub})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/diag?root=r2", nil)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("POST /v1/diag?root=r2 = %d %s", res.Code, res.Body.String())
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), `"sessionId": "r2"`) && !strings.Contains(string(body), `"sessionId":"r2"`) {
		t.Fatalf("expected r2 session in body: %s", body)
	}
	// Unknown root → 503
	miss := httptest.NewRecorder()
	srv.Handler().ServeHTTP(miss, httptest.NewRequest(http.MethodGet, "/v1/diag?root=missing", nil))
	if miss.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing root = %d", miss.Code)
	}
}

func TestDiagDownloadName(t *testing.T) {
	got := diagDownloadName("sess-abcdef0123456789", time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC))
	if !strings.HasPrefix(got, "strike-diag-") || !strings.HasSuffix(got, ".json") {
		t.Fatalf("name = %q", got)
	}
	if !strings.Contains(got, "20260806-120000") {
		t.Fatalf("stamp missing: %q", got)
	}
}
