package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
)

type testLSP struct {
	statuses    []host.LSPServerStatus
	diagnostics []host.Diagnostic
	retryName   string
	disableName string
	retryErr    error
	disableErr  error
}

func (t *testLSP) Statuses() []host.LSPServerStatus {
	return append([]host.LSPServerStatus(nil), t.statuses...)
}

func (t *testLSP) Retry(name string) error {
	t.retryName = name
	return t.retryErr
}

func (t *testLSP) Disable(name string) error {
	t.disableName = name
	return t.disableErr
}

func (t *testLSP) Diagnostics() []host.Diagnostic {
	return append([]host.Diagnostic(nil), t.diagnostics...)
}

func TestLSPAPIsUnavailableWithoutHost(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/lsp", "/v1/diagnostics"} {
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code != http.StatusNotImplemented || !strings.Contains(res.Body.String(), "capability unavailable") {
			t.Errorf("GET %s = %d %q", path, res.Code, res.Body.String())
		}
	}
	for _, p := range []struct {
		method, path string
	}{
		{http.MethodPost, "/v1/lsp/retry"},
		{http.MethodPost, "/v1/lsp/gopls/disable"},
	} {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(p.method, p.path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		srv.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d %q", p.method, p.path, res.Code, res.Body.String())
		}
	}
}

func TestBootstrapLSPCapability(t *testing.T) {
	// Nil LSP → capability false
	srv, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{}})
	if err != nil {
		t.Fatal(err)
	}
	boot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if boot.Code != http.StatusOK {
		t.Fatalf("bootstrap = %d", boot.Code)
	}
	if !strings.Contains(boot.Body.String(), `"lsp":false`) {
		t.Fatalf("want lsp:false, got %s", boot.Body.String())
	}

	// Non-nil LSP → capability true
	srv2, err := New(Options{
		SessionDir: t.TempDir(),
		Services:   &host.Services{LSP: &testLSP{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	boot2 := httptest.NewRecorder()
	srv2.Handler().ServeHTTP(boot2, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if !strings.Contains(boot2.Body.String(), `"lsp":true`) {
		t.Fatalf("want lsp:true, got %s", boot2.Body.String())
	}
}

func TestLSPStatusAndControl(t *testing.T) {
	fake := &testLSP{
		statuses: []host.LSPServerStatus{
			{Name: "gopls", Command: "gopls", State: "up", Extensions: []string{".go"}, OpenDocs: 2},
			{Name: "tsserver", Command: "typescript-language-server", State: "error", Error: "exit 1", Extensions: []string{".ts"}},
		},
	}
	srv, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{LSP: fake}})
	if err != nil {
		t.Fatal(err)
	}

	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/lsp", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d %s", res.Code, res.Body.String())
	}
	var payload lspStatusResponse
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Servers) != 2 {
		t.Fatalf("servers = %#v", payload.Servers)
	}
	if payload.Servers[0].Name != "gopls" || payload.Servers[0].State != "up" || payload.Servers[0].OpenDocs != 2 {
		t.Fatalf("gopls = %#v", payload.Servers[0])
	}
	if payload.Servers[1].Name != "tsserver" || payload.Servers[1].Error != "exit 1" {
		t.Fatalf("tsserver = %#v", payload.Servers[1])
	}
	if payload.Note != "" {
		t.Fatalf("unexpected note with live server: %q", payload.Note)
	}

	// Soft note when no servers configured.
	empty := &testLSP{}
	srvEmpty, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{LSP: empty}})
	if err != nil {
		t.Fatal(err)
	}
	emptyRes := httptest.NewRecorder()
	srvEmpty.Handler().ServeHTTP(emptyRes, httptest.NewRequest(http.MethodGet, "/v1/lsp", nil))
	if !strings.Contains(emptyRes.Body.String(), "no language servers configured") {
		t.Fatalf("empty note = %s", emptyRes.Body.String())
	}

	// Soft note when all down.
	down := &testLSP{statuses: []host.LSPServerStatus{{Name: "gopls", State: "down"}}}
	srvDown, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{LSP: down}})
	if err != nil {
		t.Fatal(err)
	}
	downRes := httptest.NewRecorder()
	srvDown.Handler().ServeHTTP(downRes, httptest.NewRequest(http.MethodGet, "/v1/lsp", nil))
	if !strings.Contains(downRes.Body.String(), "no live language servers") {
		t.Fatalf("down note = %s", downRes.Body.String())
	}

	// Retry named
	retry := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/lsp/retry", strings.NewReader(`{"name":"tsserver"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(retry, req)
	if retry.Code != http.StatusOK || fake.retryName != "tsserver" {
		t.Fatalf("retry = %d name=%q body=%s", retry.Code, fake.retryName, retry.Body.String())
	}

	// Retry all (empty body)
	fake.retryName = "unset"
	retryAll := httptest.NewRecorder()
	reqAll := httptest.NewRequest(http.MethodPost, "/v1/lsp/retry", nil)
	srv.Handler().ServeHTTP(retryAll, reqAll)
	if retryAll.Code != http.StatusOK || fake.retryName != "" {
		t.Fatalf("retry all = %d name=%q body=%s", retryAll.Code, fake.retryName, retryAll.Body.String())
	}

	// Disable
	dis := httptest.NewRecorder()
	reqDis := httptest.NewRequest(http.MethodPost, "/v1/lsp/tsserver/disable", nil)
	srv.Handler().ServeHTTP(dis, reqDis)
	if dis.Code != http.StatusOK || fake.disableName != "tsserver" {
		t.Fatalf("disable = %d name=%q body=%s", dis.Code, fake.disableName, dis.Body.String())
	}
}

func TestDiagnosticsAPIOrderingStability(t *testing.T) {
	// Host adapter already sorts; API must preserve order and emit stable JSON keys.
	fake := &testLSP{
		statuses: []host.LSPServerStatus{{Name: "gopls", State: "up"}},
		diagnostics: []host.Diagnostic{
			{Path: "a.go", Line: 1, Character: 2, Severity: "error", Source: "compiler", Code: "E1", Message: "first"},
			{Path: "a.go", Line: 3, Character: 1, Severity: "warning", Message: "second"},
			{Path: "b.go", Line: 10, Character: 4, Severity: "info", Source: "linter", Message: "third"},
		},
	}
	srv, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{LSP: fake}})
	if err != nil {
		t.Fatal(err)
	}

	var bodies []string
	for i := 0; i < 3; i++ {
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/diagnostics", nil))
		if res.Code != http.StatusOK {
			t.Fatalf("pass %d status = %d %s", i, res.Code, res.Body.String())
		}
		bodies = append(bodies, res.Body.String())
	}
	for i := 1; i < len(bodies); i++ {
		if bodies[i] != bodies[0] {
			t.Fatalf("unstable response:\n%s\nvs\n%s", bodies[0], bodies[i])
		}
	}

	var payload diagnosticsResponse
	if err := json.Unmarshal([]byte(bodies[0]), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Count != 3 || len(payload.Diagnostics) != 3 {
		t.Fatalf("count = %d diags=%d", payload.Count, len(payload.Diagnostics))
	}
	wantOrder := []string{"a.go:1:2:first", "a.go:3:1:second", "b.go:10:4:third"}
	for i, d := range payload.Diagnostics {
		key := fmt.Sprintf("%s:%d:%d:%s", d.Path, d.Line, d.Character, d.Message)
		if key != wantOrder[i] {
			t.Errorf("order[%d] = %q want %q", i, key, wantOrder[i])
		}
	}
	if payload.Diagnostics[0].Severity != "error" || payload.Diagnostics[0].Source != "compiler" || payload.Diagnostics[0].Code != "E1" {
		t.Fatalf("first = %#v", payload.Diagnostics[0])
	}
	if payload.Note != "" {
		t.Fatalf("unexpected note with findings: %q", payload.Note)
	}

	// Soft empty when live but no findings.
	emptyLive := &testLSP{statuses: []host.LSPServerStatus{{Name: "gopls", State: "up"}}}
	srv2, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{LSP: emptyLive}})
	if err != nil {
		t.Fatal(err)
	}
	res2 := httptest.NewRecorder()
	srv2.Handler().ServeHTTP(res2, httptest.NewRequest(http.MethodGet, "/v1/diagnostics", nil))
	if !strings.Contains(res2.Body.String(), `"note":"no diagnostics"`) {
		t.Fatalf("empty live = %s", res2.Body.String())
	}
	// Empty diagnostics array (not null) for stable clients.
	if !strings.Contains(res2.Body.String(), `"diagnostics":[]`) {
		t.Fatalf("want empty array: %s", res2.Body.String())
	}
}
