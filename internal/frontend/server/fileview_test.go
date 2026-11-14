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

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
	"github.com/jonathanung/strike/pkg/protocol"
)

type scriptedPerms struct {
	action  string
	summary string
}

func (p scriptedPerms) Explain(perm, pattern string) host.PermissionExplanation {
	return host.PermissionExplanation{
		Permission: perm,
		Pattern:    pattern,
		Action:     p.action,
		Summary:    p.summary,
	}
}

func (p scriptedPerms) ExplainPreset(perm, pattern, _ string) host.PermissionExplanation {
	return p.Explain(perm, pattern)
}

func (scriptedPerms) DiffPresets(string, string) (host.PermissionDiff, error) {
	return host.PermissionDiff{}, nil
}

func (scriptedPerms) Presets() []host.PermissionPresetInfo { return nil }

func (scriptedPerms) SessionFolderGrants() []host.PermissionFolderGrant { return nil }

func (scriptedPerms) RevokeSessionFolderGrant(int) bool { return false }

type staticRoots struct {
	roots []string
}

func (s staticRoots) AllowedRoots() []string { return s.roots }

func TestAllowedRootsFrom(t *testing.T) {
	if _, ok := allowedRootsFrom(nil); ok {
		t.Fatal("nil source")
	}
	if _, ok := allowedRootsFrom(staticRoots{}); ok {
		t.Fatal("empty list should not engage the ceiling")
	}
	roots, ok := allowedRootsFrom(staticRoots{roots: []string{" /tmp/ws ", ""}})
	if !ok || len(roots) != 1 || roots[0] != "/tmp/ws" {
		t.Fatalf("roots = %#v ok %v", roots, ok)
	}
}

func TestApplyDocumentRootsCeiling(t *testing.T) {
	work := t.TempDir()
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "note.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := local.NewFiles(work)
	ApplyDocumentRoots(files, staticRoots{roots: []string{other}})
	if _, err := files.ReadDocument("note.txt"); err == nil || !strings.Contains(err.Error(), "allowed roots") {
		t.Fatalf("ceiling err = %v", err)
	}
	ApplyDocumentRoots(files, nil)
	if _, err := files.ReadDocument("note.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestFileViewAndPut(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := local.NewFiles(dir)
	ops := make(chan protocol.Op, 1)
	live := NewLive("live-doc", dir, nil, ops)
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Live:       live,
		Services:   &host.Services{Files: files},
	})
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/file/view?path=note.txt", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("view %d %s", rr.Code, rr.Body.String())
	}
	var view documentView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Content != "hello\n" || !view.Writable || view.SHA256 == "" || view.MtimeUnixNano == "" {
		t.Fatalf("view = %+v", view)
	}

	body, _ := json.Marshal(map[string]any{
		"path": "note.txt", "content": "saved\n", "sha256": view.SHA256, "mtimeUnixNano": view.MtimeUnixNano,
	})
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/file", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("put %d %s", rr.Code, rr.Body.String())
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "saved\n" {
		t.Fatalf("file = %q", raw)
	}

	// Stale hash.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v1/file", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("conflict %d %s", rr.Code, rr.Body.String())
	}
	var conflict documentError
	if err := json.Unmarshal(rr.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Code != "conflict" || conflict.SHA256 == "" || strings.Contains(rr.Body.String(), "saved") && strings.Contains(conflict.Error, "saved") {
		t.Fatalf("conflict body = %s", rr.Body.String())
	}
	raw, _ = os.ReadFile(target)
	if string(raw) != "saved\n" {
		t.Fatalf("conflict wrote %q", raw)
	}

	// Escape, symlink-shaped dotdot, and missing content.
	for _, payload := range []string{
		`{"path":"../outside.txt","content":"x","sha256":"` + view.SHA256 + `","mtimeUnixNano":"1"}`,
		`{"path":"note.txt","sha256":"` + view.SHA256 + `","mtimeUnixNano":"1"}`,
	} {
		rr = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPut, "/v1/file", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		srv.Handler().ServeHTTP(rr, req)
		if rr.Code < 400 || rr.Code == http.StatusConflict && strings.Contains(payload, "outside") {
			t.Fatalf("payload %s → %d %s", payload, rr.Code, rr.Body.String())
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "outside.txt")); !os.IsNotExist(err) {
		t.Fatalf("escape created file: %v", err)
	}

	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/file/view?path=../outside.txt", nil))
	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusNotFound {
		t.Fatalf("escape read %d %s", rr.Code, rr.Body.String())
	}

	// Attach-only and read-only.
	bare, err := New(Options{Services: &host.Services{Files: files}})
	if err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v1/file", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	bare.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("attach-only %d %s", rr.Code, rr.Body.String())
	}

	ro, err := New(Options{Live: live, ReadOnly: true, Services: &host.Services{Files: files}})
	if err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v1/file", bytes.NewReader([]byte(
		`{"path":"note.txt","content":"x","sha256":"`+view.SHA256+`","mtimeUnixNano":"1"}`,
	)))
	req.Header.Set("Content-Type", "application/json")
	ro.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("read-only %d %s", rr.Code, rr.Body.String())
	}
}

func TestFilePutPlanAndDeny(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := local.NewFiles(dir)
	ops := make(chan protocol.Op, 1)
	live := NewLive("live-plan", dir, nil, ops)
	live.Publish(protocol.PermissionModeSelected{Mode: protocol.PermissionModePlan})
	srv, err := New(Options{Live: live, Services: &host.Services{Files: files}})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/file/view?path=note.txt", nil))
	var view documentView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Writable || !strings.Contains(view.WriteBlock, "plan") {
		t.Fatalf("plan view = %+v", view)
	}
	body := `{"path":"note.txt","content":"nope\n","sha256":"` + view.SHA256 + `","mtimeUnixNano":"` + view.MtimeUnixNano + `"}`
	rr = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/file", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("plan put %d %s", rr.Code, rr.Body.String())
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "note.txt"))
	if string(raw) != "hello\n" {
		t.Fatalf("plan wrote %q", raw)
	}

	live.Publish(protocol.PermissionModeSelected{Mode: protocol.PermissionModeDefault})
	denied, err := New(Options{
		Live: live,
		Services: &host.Services{
			Files:       files,
			Permissions: scriptedPerms{action: "deny", summary: "edit denied by rule"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v1/file", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	denied.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "edit denied") {
		t.Fatalf("deny put %d %s", rr.Code, rr.Body.String())
	}
	raw, _ = os.ReadFile(filepath.Join(dir, "note.txt"))
	if string(raw) != "hello\n" {
		t.Fatalf("deny wrote %q", raw)
	}

	// Ask is the user's own Save, so the write proceeds.
	ask, err := New(Options{
		Live: live,
		Services: &host.Services{
			Files:       files,
			Permissions: scriptedPerms{action: "ask"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v1/file", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ask.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("ask put %d %s", rr.Code, rr.Body.String())
	}
}

func TestFileViewBinary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bin.dat"), []byte("a\x00b"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := local.NewFiles(dir)
	ops := make(chan protocol.Op, 1)
	live := NewLive("live-bin", dir, nil, ops)
	srv, err := New(Options{Live: live, Services: &host.Services{Files: files}})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/file/view?path=bin.dat", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("binary view %d %s", rr.Code, rr.Body.String())
	}
	var view documentView
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.Binary || view.Writable || view.Content != "" {
		t.Fatalf("binary = %+v", view)
	}
}
