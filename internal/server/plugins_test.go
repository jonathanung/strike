package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host"
)

type fakePlugins struct {
	mu      sync.Mutex
	list    []host.PluginInfo
	enabled map[string]bool
	trusted map[string]bool
}

func newFakePlugins(items ...host.PluginInfo) *fakePlugins {
	f := &fakePlugins{
		list:    append([]host.PluginInfo(nil), items...),
		enabled: map[string]bool{},
		trusted: map[string]bool{},
	}
	for _, p := range items {
		f.enabled[p.ID] = p.Enabled
		f.trusted[p.ID] = p.TrustState == host.PluginTrustTrusted
	}
	return f
}

func (f *fakePlugins) List() ([]host.PluginInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]host.PluginInfo, len(f.list))
	copy(out, f.list)
	for i := range out {
		out[i].Enabled = f.enabled[out[i].ID]
		if f.trusted[out[i].ID] {
			out[i].TrustState = host.PluginTrustTrusted
		} else if out[i].HasExecutable {
			out[i].TrustState = host.PluginTrustNone
		}
	}
	return out, nil
}

func (f *fakePlugins) Inspect(id, scope string) (host.PluginInfo, error) {
	list, _ := f.List()
	for _, p := range list {
		if p.ID == id {
			return p, nil
		}
	}
	return host.PluginInfo{}, errors.New("not found")
}

func (f *fakePlugins) Enable(id, scope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.enabled[id]; !ok {
		return errors.New("not found")
	}
	f.enabled[id] = true
	return nil
}

func (f *fakePlugins) Disable(id, scope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.enabled[id]; !ok {
		return errors.New("not found")
	}
	f.enabled[id] = false
	return nil
}

func (f *fakePlugins) Remove(id, scope string, confirm bool) error {
	if !confirm {
		return errors.New("confirm required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	next := f.list[:0]
	for _, p := range f.list {
		if p.ID != id {
			next = append(next, p)
		}
	}
	f.list = next
	delete(f.enabled, id)
	return nil
}

func (f *fakePlugins) TrustPreview(id, scope string) (host.PluginTrustPreview, error) {
	p, err := f.Inspect(id, scope)
	if err != nil {
		return host.PluginTrustPreview{}, err
	}
	return host.PluginTrustPreview{
		ID: p.ID, Scope: p.Scope, Digest: p.Digest,
		Capabilities: p.Capabilities, ReviewLines: []string{"mcp: bin/x"},
	}, nil
}

func (f *fakePlugins) Trust(id, scope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.enabled[id]; !ok {
		return errors.New("not found")
	}
	f.trusted[id] = true
	return nil
}

func (f *fakePlugins) Untrust(id, scope string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trusted[id] = false
	return nil
}

func (f *fakePlugins) Search(ctx context.Context, registry, query string) ([]host.PluginCatalogHit, error) {
	return []host.PluginCatalogHit{{ID: "cat.demo", Name: "Demo", Version: "1.0.0", Description: "d"}}, nil
}

func (f *fakePlugins) Install(ctx context.Context, source, scope, registry string) (host.PluginInstallResult, error) {
	return host.PluginInstallResult{ID: "new.plug", Version: "1.0.0", Scope: "global", Enabled: true}, nil
}

func (f *fakePlugins) CheckOutdated(ctx context.Context, registry string) ([]host.PluginInfo, error) {
	return nil, nil
}

func (f *fakePlugins) PreviewUpdate(ctx context.Context, id, scope, registry string) (host.PluginUpdateReview, error) {
	return host.PluginUpdateReview{ID: id, OldVersion: "1.0.0", NewVersion: "1.1.0", Summary: "bump"}, nil
}

func (f *fakePlugins) Update(ctx context.Context, id, scope, registry string, confirm bool) (host.PluginInstallResult, error) {
	if !confirm {
		return host.PluginInstallResult{}, errors.New("confirm required")
	}
	return host.PluginInstallResult{ID: id, Version: "1.1.0", Scope: scope, Enabled: true}, nil
}

type fakePanes struct {
	list []host.PaneInfo
}

func (f *fakePanes) List() ([]host.PaneInfo, error) {
	out := make([]host.PaneInfo, len(f.list))
	copy(out, f.list)
	return out, nil
}

func postJSON(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(res, req)
	return res
}

func TestPluginsCapabilityUnavailable(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/plugins", nil))
	if res.Code != http.StatusNotImplemented {
		t.Fatalf("code=%d body=%s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "plugins") {
		t.Fatalf("body=%s", res.Body.String())
	}
}

func TestPluginsListEnableTrust(t *testing.T) {
	fake := newFakePlugins(host.PluginInfo{
		ID: "acme.pack", Version: "1.0.0", Name: "Acme", Scope: host.PluginScopeGlobal,
		Enabled: true, Status: "enabled", TrustState: host.PluginTrustNone,
		HasExecutable: true, Panes: 1, Capabilities: []string{"panes", "panes.process"},
		MCP: []host.PluginMCP{{Name: "lint", Command: "bin/lint", EnvKeys: []string{"TOK"}}},
	})
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Live:       &Live{},
		Services:   &host.Services{Plugins: fake},
	})
	if err != nil {
		t.Fatal(err)
	}

	boot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if !strings.Contains(boot.Body.String(), `"plugins":true`) {
		t.Fatalf("bootstrap: %s", boot.Body.String())
	}

	list := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/plugins", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list=%d %s", list.Code, list.Body.String())
	}
	body := list.Body.String()
	if !strings.Contains(body, `"id":"acme.pack"`) {
		t.Fatalf("list body: %s", body)
	}
	if strings.Contains(body, "supersecret") {
		t.Fatalf("leaked secret: %s", body)
	}
	if !strings.Contains(body, `"envKeys"`) {
		t.Fatalf("want envKeys: %s", body)
	}

	dis := postJSON(t, srv, "/v1/plugins/disable", `{"id":"acme.pack"}`)
	if dis.Code != http.StatusOK {
		t.Fatalf("disable=%d %s", dis.Code, dis.Body.String())
	}

	en := postJSON(t, srv, "/v1/plugins/enable", `{"id":"acme.pack"}`)
	if en.Code != http.StatusOK {
		t.Fatalf("enable=%d %s", en.Code, en.Body.String())
	}

	prev := httptest.NewRecorder()
	srv.Handler().ServeHTTP(prev, httptest.NewRequest(http.MethodGet, "/v1/plugins/acme.pack/trust-preview", nil))
	if prev.Code != http.StatusOK || !strings.Contains(prev.Body.String(), "reviewLines") {
		t.Fatalf("trust-preview=%d %s", prev.Code, prev.Body.String())
	}

	tr := postJSON(t, srv, "/v1/plugins/trust", `{"id":"acme.pack"}`)
	if tr.Code != http.StatusOK {
		t.Fatalf("trust=%d %s", tr.Code, tr.Body.String())
	}

	rm := postJSON(t, srv, "/v1/plugins/remove", `{"id":"acme.pack"}`)
	if rm.Code != http.StatusBadRequest && rm.Code != http.StatusUnprocessableEntity {
		t.Fatalf("remove no confirm=%d %s", rm.Code, rm.Body.String())
	}

	rm2 := postJSON(t, srv, "/v1/plugins/remove", `{"id":"acme.pack","confirm":true}`)
	if rm2.Code != http.StatusOK {
		t.Fatalf("remove=%d %s", rm2.Code, rm2.Body.String())
	}
}

func TestPluginsAttachOnlyAllowsRead(t *testing.T) {
	// Attach-only still allows list/inspect; mutations may be allowed at host
	// layer (plugins are project config). Ensure list works without Live.
	fake := newFakePlugins(host.PluginInfo{ID: "x", Version: "1", Name: "X", Enabled: true})
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Services:   &host.Services{Plugins: fake},
	})
	if err != nil {
		t.Fatal(err)
	}
	list := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/plugins", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list=%d %s", list.Code, list.Body.String())
	}
}

func TestPanesListStaticNoPluginRoot(t *testing.T) {
	def := json.RawMessage(`{"schemaVersion":1,"id":"acme.status","title":"Status","mode":"static","permissions":{"host":[],"fs":"none","network":"none","command":"none"},"view":{"type":"text","text":"hi","style":"title"}}`)
	fake := &fakePanes{list: []host.PaneInfo{{
		ID: "acme.status", PluginID: "acme", PluginVersion: "1.0.0",
		Title: "Status", Mode: host.PaneModeStatic, Trusted: true,
		PluginRoot: "/secret/path/plugins/acme", DefinitionJSON: def,
	}}}
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Services:   &host.Services{Panes: fake},
	})
	if err != nil {
		t.Fatal(err)
	}
	boot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if !strings.Contains(boot.Body.String(), `"panes":true`) {
		t.Fatalf("bootstrap: %s", boot.Body.String())
	}

	list := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/panes", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list=%d %s", list.Code, list.Body.String())
	}
	body := list.Body.String()
	if strings.Contains(body, "/secret/path") || strings.Contains(body, "pluginRoot") {
		t.Fatalf("plugin root leaked: %s", body)
	}
	if !strings.Contains(body, `"id":"acme.status"`) {
		t.Fatalf("body: %s", body)
	}
	if strings.Contains(body, "tea.Msg") || strings.Contains(body, "lipgloss") {
		t.Fatalf("tui leak: %s", body)
	}

	mount := postJSON(t, srv, "/v1/panes/acme.status/mount", `{"width":40,"height":12}`)
	if mount.Code != http.StatusOK {
		t.Fatalf("mount=%d %s", mount.Code, mount.Body.String())
	}
	if !strings.Contains(mount.Body.String(), `"view"`) {
		t.Fatalf("mount body: %s", mount.Body.String())
	}

	snap := httptest.NewRecorder()
	srv.Handler().ServeHTTP(snap, httptest.NewRequest(http.MethodGet, "/v1/panes/acme.status/snapshot", nil))
	if snap.Code != http.StatusOK {
		t.Fatalf("snapshot=%d %s", snap.Code, snap.Body.String())
	}
}

func TestPaneProcessUntrustedConflict(t *testing.T) {
	def := json.RawMessage(`{"schemaVersion":1,"id":"acme.board","title":"Board","mode":"process","command":"x","permissions":{"host":[],"fs":"none","network":"none","command":"none"}}`)
	fake := &fakePanes{list: []host.PaneInfo{{
		ID: "acme.board", PluginID: "acme", PluginVersion: "1",
		Title: "Board", Mode: host.PaneModeProcess, Trusted: false,
		LoadError:      "process pane blocked until plugin trust is granted",
		DefinitionJSON: def,
	}}}
	srv, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{Panes: fake}})
	if err != nil {
		t.Fatal(err)
	}
	res := postJSON(t, srv, "/v1/panes/acme.board/mount", `{}`)
	if res.Code != http.StatusConflict {
		t.Fatalf("want conflict, got %d %s", res.Code, res.Body.String())
	}
}
