package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/host/local"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestFilesApplyEditAndPatch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := local.NewFiles(dir)
	ops := make(chan protocol.Op, 1)
	live := NewLive("live-apply", dir, nil, ops)
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Live:       live,
		Services:   &host.Services{Files: files},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Bootstrap advertises fileApply when live + files.
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if rr.Code != 200 {
		t.Fatalf("bootstrap %d", rr.Code)
	}
	var boot bootstrapResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}
	if !boot.Capabilities.FileApply {
		t.Fatalf("fileApply want true: %+v", boot.Capabilities)
	}

	body, _ := json.Marshal(map[string]any{
		"path": "note.txt", "oldString": "hello", "newString": "hi",
	})
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/files/apply-edit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("apply-edit %d %s", rr.Code, rr.Body.String())
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "hi world") {
		t.Fatalf("file content = %q", raw)
	}

	// Escape rejected.
	body, _ = json.Marshal(map[string]any{
		"path": "../outside", "oldString": "a", "newString": "b",
	})
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/files/apply-edit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict && rr.Code != http.StatusBadRequest {
		t.Fatalf("escape want 4xx got %d %s", rr.Code, rr.Body.String())
	}

	// Attach-only rejects.
	bare, err := New(Options{Services: &host.Services{Files: files}})
	if err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/files/apply-edit", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	bare.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("attach-only want 403 got %d", rr.Code)
	}

	// Read-only rejects.
	ro, err := New(Options{
		Live: live, ReadOnly: true,
		Services: &host.Services{Files: files},
	})
	if err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/files/apply-edit", bytes.NewReader([]byte(
		`{"path":"note.txt","oldString":"hi","newString":"hey"}`,
	)))
	req.Header.Set("Content-Type", "application/json")
	ro.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("read-only want 403 got %d %s", rr.Code, rr.Body.String())
	}
}

func TestFilesApplyPatchEndpoint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := local.NewFiles(dir)
	ops := make(chan protocol.Op, 1)
	live := NewLive("live-patch", dir, nil, ops)
	srv, err := New(Options{Live: live, Services: &host.Services{Files: files}})
	if err != nil {
		t.Fatal(err)
	}
	// Minimal invalid patch → conflict, not 500.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/files/apply-patch", strings.NewReader(`{"patch":"not-a-patch"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict && rr.Code != http.StatusBadRequest {
		t.Fatalf("bad patch want 4xx got %d %s", rr.Code, rr.Body.String())
	}
}
