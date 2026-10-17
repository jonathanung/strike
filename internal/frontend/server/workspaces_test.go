package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jonathanung/strike/internal/persist/workspace"
)

func TestWorkspacesAPI(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "proj")
	store, err := workspace.Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{
		SessionDir:   dir,
		PollInterval: 20 * time.Millisecond,
		Workspaces:   store,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	// Empty list.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"workspaces":[]`) {
		t.Fatalf("empty list: %d %s", rec.Code, rec.Body.String())
	}

	// Add (dir must exist).
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"path": wsDir})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/workspaces", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	var added workspace.Workspace
	if err := json.NewDecoder(rec.Body).Decode(&added); err != nil {
		t.Fatal(err)
	}
	if added.ID == "" {
		t.Fatalf("added: %+v", added)
	}

	// Activate + remove.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/workspaces/"+added.ID+"/activate", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("activate: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/workspaces/"+added.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/workspaces/"+added.ID, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("remove missing: %d, want 404", rec.Code)
	}
}

func TestWorkspacesAPIUnavailable(t *testing.T) {
	srv := testServer(t, t.TempDir(), "")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil registry: %d, want 503", rec.Code)
	}
}

func TestCSPAllowlistsInlineLoaderByHash(t *testing.T) {
	loader := "\nimport init from '/assets/app.js';\nawait init();\n"
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Static: fstest.MapFS{
			"index.html": {Data: []byte(`<script type="module">` + loader + `</script><script src="/assets/register.js"></script>`)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	sum := sha256.Sum256([]byte(loader))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, want) {
		t.Fatalf("CSP missing %s:\n%s", want, csp)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("CSP must not use unsafe-inline: %s", csp)
	}
}

func TestResolveDaemonBindAddr(t *testing.T) {
	cases := []struct {
		addr    string
		allow   bool
		wantErr bool
	}{
		{"127.0.0.1:8787", false, false},
		{"100.64.0.5:8787", false, true},
		{"100.64.0.5:8787", true, false},
		{"192.168.1.10:8787", true, false},
		{"8.8.8.8:8787", true, true},
		{"0.0.0.0:8787", true, true},
	}
	for _, c := range cases {
		_, err := ResolveDaemonBindAddr(c.addr, c.allow)
		if (err != nil) != c.wantErr {
			t.Errorf("ResolveDaemonBindAddr(%q, %v) err=%v, wantErr=%v", c.addr, c.allow, err, c.wantErr)
		}
	}
}
