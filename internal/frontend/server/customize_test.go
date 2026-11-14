package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
)

func TestCustomizeAuthAndCatalog(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".strike", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".strike", "skills", "note.md"), []byte("---\nname: note\ndescription: seeded\n---\nSay hello.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".strike", "mcp.json"), []byte(`{"servers":{"docs":{"command":"echo","env":{"TOKEN":"sk-abcdefghijklmnopqrstuvwxyz0123456789"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	locked, err := New(Options{Auth: true, Token: "sekret", SessionDir: t.TempDir(), WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	locked.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/customize", nil))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", res.Code)
	}

	fake := &fakeMCP{servers: []host.MCPServerStatus{{
		Name: "docs", State: "up", ToolCount: 2, Tools: []string{"mcp_docs_search"},
		Error: "token sk-abcdefghijklmnopqrstuvwxyz0123456789",
	}}}
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		WorkDir:    work,
		Services:   &host.Services{MCP: fake},
	})
	if err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/customize", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "sk-abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Fatalf("response leaked secret: %s", res.Body.String())
	}
	var cat struct {
		Skills []struct {
			Name     string `json:"name"`
			Origin   string `json:"origin"`
			Editable bool   `json:"editable"`
		} `json:"skills"`
		MCP []struct {
			Name  string `json:"name"`
			State string `json:"state"`
			Error string `json:"error"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &cat); err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, s := range cat.Skills {
		if s.Name == "note" {
			saw = s.Origin == "global" && s.Editable
		}
	}
	if !saw {
		t.Fatalf("skills = %+v", cat.Skills)
	}
	var mcpOK bool
	for _, row := range cat.MCP {
		if row.Name == "docs" && row.State == "up" && !strings.Contains(row.Error, "sk-") {
			mcpOK = true
		}
	}
	if !mcpOK {
		t.Fatalf("mcp = %+v", cat.MCP)
	}

	preview := httptest.NewRecorder()
	body := `{"kind":"skill","scope":"global","name":"note","description":"updated","body":"Say hello again.","baseHash":"missing"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/customize/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(preview, req)
	if preview.Code != http.StatusBadRequest {
		t.Fatalf("stale preview = %d %s", preview.Code, preview.Body.String())
	}

	docRes := httptest.NewRecorder()
	srv.Handler().ServeHTTP(docRes, httptest.NewRequest(http.MethodGet, "/v1/customize/doc?kind=skill&name=note&scope=global", nil))
	if docRes.Code != http.StatusOK {
		t.Fatalf("doc = %d %s", docRes.Code, docRes.Body.String())
	}
	var doc struct {
		BaseHash string `json:"baseHash"`
		Body     string `json:"body"`
	}
	if err := json.Unmarshal(docRes.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	applyBody := `{"confirm":true,"kind":"skill","scope":"global","name":"note","description":"updated","body":"Say hello again.","baseHash":"` + doc.BaseHash + `","diffHash":"stale"}`
	apply := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/customize/apply", strings.NewReader(applyBody))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(apply, req)
	if apply.Code != http.StatusBadRequest || strings.Contains(apply.Body.String(), "sk-") {
		t.Fatalf("apply = %d %s", apply.Code, apply.Body.String())
	}

	prevBody := strings.Replace(applyBody, `"confirm":true`, `"confirm":false`, 1)
	prevBody = strings.Replace(prevBody, `"diffHash":"stale"`, `"diffHash":""`, 1)
	preview = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/customize/preview", strings.NewReader(prevBody))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(preview, req)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview = %d %s", preview.Code, preview.Body.String())
	}
	var prev struct {
		DiffHash string `json:"diffHash"`
		Diff     string `json:"diff"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &prev); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prev.Diff, "updated") {
		t.Fatalf("diff = %s", prev.Diff)
	}
	applyBody = `{"confirm":true,"kind":"skill","scope":"global","name":"note","description":"updated","body":"Say hello again.","baseHash":"` + doc.BaseHash + `","diffHash":"` + prev.DiffHash + `"}`
	apply = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/customize/apply", strings.NewReader(applyBody))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(apply, req)
	if apply.Code != http.StatusOK {
		t.Fatalf("apply = %d %s", apply.Code, apply.Body.String())
	}
	written, err := os.ReadFile(filepath.Join(home, ".strike", "skills", "note.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "description: updated") {
		t.Fatalf("written = %s", written)
	}
}
