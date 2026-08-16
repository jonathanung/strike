package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
)

// fakeMCP is a host.MCP for API tests.
type fakeMCP struct {
	mu       sync.Mutex
	servers  []host.MCPServerStatus
	retryErr error
	disErr   error
	retries  []string
	disabled []string
}

func (f *fakeMCP) Statuses() []host.MCPServerStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]host.MCPServerStatus, len(f.servers))
	copy(out, f.servers)
	return out
}

func (f *fakeMCP) Retry(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.retryErr != nil {
		return f.retryErr
	}
	f.retries = append(f.retries, name)
	for i := range f.servers {
		if name != "" && f.servers[i].Name != name {
			continue
		}
		if f.servers[i].State != "up" && f.servers[i].State != "disabled" {
			f.servers[i].State = "up"
			f.servers[i].Error = ""
		}
	}
	return nil
}

func (f *fakeMCP) Disable(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.disErr != nil {
		return f.disErr
	}
	if name == "" {
		return errors.New("name required")
	}
	found := false
	for i := range f.servers {
		if f.servers[i].Name == name {
			f.servers[i].State = "disabled"
			f.servers[i].ToolCount = 0
			f.servers[i].Tools = nil
			found = true
			break
		}
	}
	if !found {
		return errors.New("unknown MCP server: " + name)
	}
	f.disabled = append(f.disabled, name)
	return nil
}

func TestMCPCapabilityUnavailable(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/mcp"},
		{http.MethodPost, "/v1/mcp/retry"},
		{http.MethodPost, "/v1/mcp/disable"},
	} {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(p.method, p.path, strings.NewReader(`{}`))
		if p.method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
		}
		srv.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusNotImplemented || !strings.Contains(res.Body.String(), "capability unavailable") {
			t.Errorf("%s %s = %d %q", p.method, p.path, res.Code, res.Body.String())
		}
	}
}

func TestMCPListRetryDisable(t *testing.T) {
	fake := &fakeMCP{
		servers: []host.MCPServerStatus{
			{
				Name: "docs", Command: "npx docs-mcp", Transport: "stdio",
				State: "up", ToolCount: 2, Tools: []string{"mcp_docs_search", "mcp_docs_get"},
			},
			{
				Name: "remote", Command: "https://mcp.example/mcp", Transport: "http",
				State: "error", ToolCount: 0, Error: "connection refused",
			},
		},
	}
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Services:   &host.Services{MCP: fake},
	})
	if err != nil {
		t.Fatal(err)
	}

	// bootstrap capability
	boot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if boot.Code != http.StatusOK {
		t.Fatalf("bootstrap = %d", boot.Code)
	}
	if !strings.Contains(boot.Body.String(), `"mcp":true`) {
		t.Errorf("bootstrap missing mcp true: %s", boot.Body.String())
	}

	// list
	list := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/mcp", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	var payload struct {
		Servers []host.MCPServerStatus `json:"servers"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(payload.Servers) != 2 {
		t.Fatalf("servers = %d, want 2: %s", len(payload.Servers), list.Body.String())
	}
	if payload.Servers[0].Name != "docs" || payload.Servers[0].ToolCount != 2 {
		t.Errorf("first server = %+v", payload.Servers[0])
	}
	if payload.Servers[1].State != "error" || payload.Servers[1].Error != "connection refused" {
		t.Errorf("second server = %+v", payload.Servers[1])
	}
	// camelCase wire fields
	if !strings.Contains(list.Body.String(), `"toolCount":2`) {
		t.Errorf("want camelCase toolCount: %s", list.Body.String())
	}

	// retry named
	retry := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp/retry", strings.NewReader(`{"name":"remote"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(retry, req)
	if retry.Code != http.StatusOK || !strings.Contains(retry.Body.String(), `"ok":true`) {
		t.Fatalf("retry = %d %s", retry.Code, retry.Body.String())
	}
	if len(fake.retries) != 1 || fake.retries[0] != "remote" {
		t.Errorf("retries = %v", fake.retries)
	}

	// retry all (empty body)
	retryAll := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/mcp/retry", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(retryAll, req)
	if retryAll.Code != http.StatusOK {
		t.Fatalf("retry all = %d %s", retryAll.Code, retryAll.Body.String())
	}
	if len(fake.retries) != 2 || fake.retries[1] != "" {
		t.Errorf("retries after all = %v", fake.retries)
	}

	// disable
	dis := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/mcp/disable", strings.NewReader(`{"name":"docs"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(dis, req)
	if dis.Code != http.StatusOK {
		t.Fatalf("disable = %d %s", dis.Code, dis.Body.String())
	}
	if len(fake.disabled) != 1 || fake.disabled[0] != "docs" {
		t.Errorf("disabled = %v", fake.disabled)
	}

	// list reflects disable
	list2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list2, httptest.NewRequest(http.MethodGet, "/v1/mcp", nil))
	if !strings.Contains(list2.Body.String(), `"state":"disabled"`) {
		t.Errorf("list after disable: %s", list2.Body.String())
	}

	// disable missing name
	bad := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/mcp/disable", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(bad, req)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "name is required") {
		t.Errorf("disable empty = %d %s", bad.Code, bad.Body.String())
	}

	// disable unknown
	unk := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/mcp/disable", strings.NewReader(`{"name":"nope"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(unk, req)
	if unk.Code != http.StatusBadRequest || !strings.Contains(unk.Body.String(), "unknown") {
		t.Errorf("disable unknown = %d %s", unk.Code, unk.Body.String())
	}
}

func TestMCPBootstrapFalseWithoutService(t *testing.T) {
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Services:   &host.Services{},
	})
	if err != nil {
		t.Fatal(err)
	}
	boot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if !strings.Contains(boot.Body.String(), `"mcp":false`) {
		t.Errorf("want mcp false: %s", boot.Body.String())
	}
}

func TestMCPEmptyList(t *testing.T) {
	fake := &fakeMCP{servers: nil}
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Services:   &host.Services{MCP: fake},
	})
	if err != nil {
		t.Fatal(err)
	}
	list := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/mcp", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	if !strings.Contains(list.Body.String(), `"servers":[]`) {
		t.Errorf("want empty servers array: %s", list.Body.String())
	}
}
