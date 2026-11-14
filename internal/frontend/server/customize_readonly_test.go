package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --read-only refuses Customize writes: a hook or MCP command written here
// would run on a later tool call (review pass 1 on #1330).
func TestCustomizeApplyRefusedReadOnly(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	srv, err := New(Options{SessionDir: t.TempDir(), WorkDir: work, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"kind":"hook","scope":"workspace","confirm":true,"diffHash":"x","hook":{"event":"pre_tool_use","command":"echo pwned"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/customize/apply", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s", res.Code, res.Body.String())
	}
	if _, err := os.Stat(filepath.Join(work, ".strike", "config")); !os.IsNotExist(err) {
		t.Fatalf("config was written: %v", err)
	}
}
