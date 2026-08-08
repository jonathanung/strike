package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/host/local"
)

func TestFilesSearchAndHistoryEnqueue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "alpha.go"), []byte("package a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta.md"), []byte("# b"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := local.NewFiles(dir)
	srv, err := New(Options{Services: &host.Services{
		Files:   files,
		History: testHistory{entries: []string{"old"}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/files/search?q=alpha&limit=10", nil))
	if rr.Code != 200 {
		t.Fatalf("search %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Paths) == 0 {
		t.Fatalf("expected paths: %s", rr.Body.String())
	}
	found := false
	for _, p := range body.Paths {
		if strings.Contains(p, "alpha") {
			found = true
		}
	}
	if !found {
		t.Fatalf("alpha not in %v", body.Paths)
	}

	bare, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	bare.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/files/search?q=x", nil))
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("want 501 got %d", rr.Code)
	}

	// Without a live engine, history POST is attach-only blocked (mutations).
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/history", strings.NewReader(`{"prompt":"hello world"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("history post attach-only want 403 got %d %s", rr.Code, rr.Body.String())
	}
}
