package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/persist/artifact"
	"github.com/jonathanung/strike/internal/persist/history"
	"github.com/jonathanung/strike/internal/persist/issue"
	"github.com/jonathanung/strike/internal/persist/ledger"
	"github.com/jonathanung/strike/internal/persist/memory"
	"github.com/jonathanung/strike/internal/persist/plan"
	"github.com/jonathanung/strike/internal/persist/workspace"
	"github.com/jonathanung/strike/internal/product/auth"
	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/product/goal"
	"github.com/jonathanung/strike/internal/product/project"
	"github.com/jonathanung/strike/internal/trust/audit"
)

func TestProjectStateFilesMatchStores(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "project")
	if err := os.MkdirAll(key, 0o755); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, st := range projectStateFiles(root, key) {
		got[st.path] = st.detail
	}

	mem, err := memory.Open(root, key)
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Close()
	if _, ok := got[mem.Path()]; !ok {
		t.Fatalf("memory path %s not listed", mem.Path())
	}

	hist, err := history.Open(root, key)
	if err != nil {
		t.Fatal(err)
	}
	defer hist.Close()
	entries, err := os.ReadDir(filepath.Join(root, "history"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("history entries %#v", entries)
	}
	histPath := filepath.Join(root, "history", entries[0].Name())
	if _, ok := got[histPath]; !ok {
		t.Fatalf("history path %s not listed in %#v", histPath, keys(got))
	}

	issues, err := issue.Open(root, key)
	if err != nil {
		t.Fatal(err)
	}
	defer issues.Close()
	if _, ok := got[issues.Path()]; !ok {
		t.Fatalf("issues path %s not listed", issues.Path())
	}

	plans, err := plan.Open(root, key)
	if err != nil {
		t.Fatal(err)
	}
	defer plans.Close()
	if _, ok := got[plans.Path()]; !ok {
		t.Fatalf("plans path %s not listed", plans.Path())
	}

	arts, err := artifact.Open(root, key)
	if err != nil {
		t.Fatal(err)
	}
	defer arts.Close()
	if _, ok := got[arts.Path()]; !ok {
		t.Fatalf("artifacts path %s not listed", arts.Path())
	}

	led, err := ledger.Open(root, key)
	if err != nil {
		t.Fatal(err)
	}
	defer led.Close()
	if _, ok := got[led.Path()]; !ok {
		t.Fatalf("ledger path %s not listed", led.Path())
	}

	goals, err := goal.Open(root, key)
	if err != nil {
		t.Fatal(err)
	}
	defer goals.Close()
	if _, ok := got[goals.Dir()]; !ok {
		t.Fatalf("goals dir %s not listed", goals.Dir())
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestLocalFilesHandler(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	secret := "local-files-credential-must-not-leak"
	authPath := auth.DefaultPath()
	if err := os.MkdirAll(filepath.Dir(authPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(t.TempDir(), "daemon.token")
	tokenSecret := "local-files-token-secret"
	if err := os.WriteFile(tokenPath, []byte(tokenSecret), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionDir := t.TempDir()
	ws, err := workspace.Open(filepath.Join(t.TempDir(), "ws"))
	if err != nil {
		t.Fatal(err)
	}
	aud, err := audit.Open(audit.Options{Dir: filepath.Join(t.TempDir(), "audit")})
	if err != nil {
		t.Fatal(err)
	}
	defer aud.Close()

	srv, err := New(Options{
		SessionDir: sessionDir,
		Workspaces: ws,
		Audit:      aud,
		Auth:       true,
		Token:      "bootstrap-token",
		TokenFile:  tokenPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	denied := httptest.NewRecorder()
	srv.Handler().ServeHTTP(denied, httptest.NewRequest(http.MethodGet, "/v1/local-files", nil))
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status %d", denied.Code)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/local-files", nil)
	req.Header.Set("Authorization", "Bearer "+tokenSecret)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, secret) || strings.Contains(body, tokenSecret) || strings.Contains(body, "bootstrap-token") {
		t.Fatalf("response leaked a secret: %s", body)
	}
	var doc localFilesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Telemetry {
		t.Fatal("telemetry must be false")
	}
	if doc.Auth != "file" {
		t.Fatalf("auth %q", doc.Auth)
	}
	if !strings.Contains(doc.Note, "No telemetry") || !strings.Contains(doc.Note, "onboarding.json") {
		t.Fatalf("note %q", doc.Note)
	}
	need := map[string]bool{
		sessionDir: false,
		aud.Dir():  false,
		ws.Path():  false,
		tokenPath:  false,
	}
	for _, f := range doc.Files {
		if strings.HasSuffix(f.Path, "onboarding.json") {
			t.Fatalf("listed onboarding.json %s", f.Path)
		}
		if f.Path == authPath || strings.Contains(f.Path, "attachments") || strings.Contains(f.Path, "/history/") {
			t.Fatalf("attach-only server listed an assemble path %s", f.Path)
		}
		if _, ok := need[f.Path]; ok {
			need[f.Path] = true
		}
		if f.Path == authPath && !f.Exists {
			t.Fatal("auth.json should exist")
		}
		if f.Group == "credentials" && f.Path == tokenPath && f.Access != "read-each-request" {
			t.Fatalf("token access %q", f.Access)
		}
	}
	for path, ok := range need {
		if !ok {
			t.Errorf("missing %s", path)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if doc.WorkDir != cwd {
		t.Fatalf("workDir %q want %q", doc.WorkDir, cwd)
	}
}

func TestLocalFilesAssembledListsStores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srv, err := New(Options{SessionDir: t.TempDir(), Assembled: true})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/local-files", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var doc localFilesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	root := config.GlobalRoot()
	abs := func(p string) string {
		if a, err := filepath.Abs(p); err == nil {
			return a
		}
		return p
	}
	want := map[string]bool{
		abs(auth.DefaultPath()):                 false,
		abs(filepath.Join(root, "attachments")): false,
	}
	if id, err := project.Resolve(t.Context(), mustGetwd(t)); err == nil && id.Key != "" {
		for _, st := range projectStateFiles(root, id.Key) {
			want[abs(st.path)] = false
		}
	}
	for _, f := range doc.Files {
		if _, ok := want[f.Path]; ok {
			want[f.Path] = true
		}
		if strings.HasSuffix(f.Path, "onboarding.json") {
			t.Fatalf("listed onboarding.json %s", f.Path)
		}
	}
	for path, ok := range want {
		if !ok {
			t.Errorf("missing %s", path)
		}
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

func TestLocalFilesAuthOff(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/local-files", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var doc localFilesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Auth != "off" || doc.Telemetry {
		t.Fatalf("auth %q telemetry %v", doc.Auth, doc.Telemetry)
	}
}
