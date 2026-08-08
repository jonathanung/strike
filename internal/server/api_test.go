package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

type testAuth struct {
	keyProvider, key, logout string
}

func (*testAuth) Statuses() []host.ProviderStatus {
	return []host.ProviderStatus{{Name: "echo", Builtin: true, Authed: true}}
}
func (*testAuth) Describe(string) string { return "test" }
func (a *testAuth) SetAPIKey(provider, key string) error {
	a.keyProvider, a.key = provider, key
	return nil
}
func (a *testAuth) Logout(provider string) error                                 { a.logout = provider; return nil }
func (*testAuth) BeginOAuth(context.Context, string) (*host.OAuthLogin, error)   { return nil, nil }
func (*testAuth) BeginDevice(context.Context, string) (*host.DeviceLogin, error) { return nil, nil }

type testHistory struct{ entries []string }

func (h testHistory) Entries() []string { return h.entries }
func (h testHistory) Enqueue(string) <-chan error {
	ch := make(chan error, 1)
	ch <- nil
	close(ch)
	return ch
}

type testSettings struct {
	saved     chan [5]string
	defaults  host.UserDefaults
	savedDial chan string
	dialErr   error
}

func (s testSettings) Defaults() host.UserDefaults { return s.defaults }

func (s testSettings) SaveDefaults(provider, model, agent, effort, mode string) error {
	if s.saved != nil {
		s.saved <- [5]string{provider, model, agent, effort, mode}
	}
	return nil
}
func (testSettings) SaveTheme(string) error                        { return nil }
func (testSettings) SavePresentation(string, string, string) error { return nil }
func (s testSettings) SaveConfigDials(sandboxMode, notify, leanCode, deferTools, sessionWorktree, autoupdate string) error {
	if s.dialErr != nil {
		return s.dialErr
	}
	if s.savedDial != nil && sandboxMode != "" {
		s.savedDial <- sandboxMode
	}
	return nil
}
func (testSettings) SaveAutoApproveDials(string, *[]string, string) error { return nil }
func (testSettings) SaveCompactionDials(host.CompactionDials) error       { return nil }
func (testSettings) SaveKeybinds(map[string][]string) error               { return nil }

func TestAttachOnlyBootstrapDeclaresProtocolOpsUnavailable(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	body := res.Body.String()
	for _, want := range []string{`"attachOnly":true`, `"auth":false`, `"roots":false`, `"protocolOps":null`} {
		if !strings.Contains(body, want) {
			t.Errorf("bootstrap missing %s: %s", want, body)
		}
	}
	for _, unwanted := range []string{`"set.fast"`, `"rewind"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("attach-only bootstrap unexpectedly includes %s: %s", unwanted, body)
		}
	}
}

func TestChangedFilesAPIReportsGitDiffs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init")
	path := filepath.Join(dir, "alpha.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "alpha.txt")
	runGit("commit", "-m", "initial")
	if err := os.WriteFile(path, []byte("one\nthree\nfour\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	live := NewLive("live", dir, nil, make(chan protocol.Op))
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/changed-files", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("changed files = %d %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for _, want := range []string{`"path":"alpha.txt"`, `"added":2`, `"deleted":1`, "+three", "-two"} {
		if !strings.Contains(body, want) {
			t.Errorf("changed files missing %q: %s", want, body)
		}
	}
}

func TestServiceAPIsUnavailableWithoutConfiguredHost(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/v1/providers", "/v1/models?provider=echo", "/v1/history", "/v1/files",
		"/v1/memory", "/v1/issues", "/v1/plans",
		"/v1/permissions/explain?permission=bash", "/v1/permissions/presets",
	} {
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code != http.StatusNotImplemented || !strings.Contains(res.Body.String(), "capability unavailable") {
			t.Errorf("GET %s = %d %q, want tested unavailable state", path, res.Code, res.Body.String())
		}
	}
}

type testPermissions struct {
	lastPerm, lastPat string
	presets           []host.PermissionPresetInfo
}

func (p *testPermissions) Explain(permission, pattern string) host.PermissionExplanation {
	p.lastPerm, p.lastPat = permission, pattern
	if pattern == "" {
		pattern = "*"
	}
	return host.PermissionExplanation{
		Permission: permission,
		Pattern:    pattern,
		Action:     "ask",
		Layer:      "defaults",
		Matched: host.PermissionMatch{
			Layer:      "defaults",
			Permission: permission,
			Pattern:    "*",
			Action:     "ask",
		},
		Summary: "bash * → ask (defaults)",
	}
}

func (p *testPermissions) ExplainPreset(permission, pattern, presetID string) host.PermissionExplanation {
	return p.Explain(permission, pattern)
}

func (p *testPermissions) DiffPresets(leftID, rightID string) (host.PermissionDiff, error) {
	return host.PermissionDiff{}, nil
}

func (p *testPermissions) Presets() []host.PermissionPresetInfo {
	if p.presets != nil {
		return p.presets
	}
	return []host.PermissionPresetInfo{
		{ID: "read-only", Name: "Read only", Description: "deny writes"},
	}
}

func TestPermissionServiceAPIs(t *testing.T) {
	perms := &testPermissions{
		presets: []host.PermissionPresetInfo{
			{ID: "dev", Name: "Dev", Description: "local development"},
		},
	}
	services := &host.Services{Permissions: perms}
	srv, err := New(Options{SessionDir: t.TempDir(), Services: services})
	if err != nil {
		t.Fatal(err)
	}

	// Bootstrap capability flag.
	boot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if boot.Code != http.StatusOK || !strings.Contains(boot.Body.String(), `"permissions":true`) {
		t.Fatalf("bootstrap permissions cap = %d %s", boot.Code, boot.Body.String())
	}

	// Explain requires permission query.
	missing := httptest.NewRecorder()
	srv.Handler().ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/permissions/explain", nil))
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("explain missing perm = %d %s", missing.Code, missing.Body.String())
	}

	// Explain happy path.
	ex := httptest.NewRecorder()
	srv.Handler().ServeHTTP(ex, httptest.NewRequest(http.MethodGet, "/v1/permissions/explain?permission=bash&pattern=git+status", nil))
	if ex.Code != http.StatusOK {
		t.Fatalf("explain = %d %s", ex.Code, ex.Body.String())
	}
	body := ex.Body.String()
	for _, want := range []string{`"Permission":"bash"`, `"Pattern":"git status"`, `"Action":"ask"`, `"Summary":"bash * → ask (defaults)"`} {
		if !strings.Contains(body, want) {
			t.Errorf("explain missing %q: %s", want, body)
		}
	}
	if perms.lastPerm != "bash" || perms.lastPat != "git status" {
		t.Fatalf("explain args = %q %q", perms.lastPerm, perms.lastPat)
	}

	// Presets list.
	presets := httptest.NewRecorder()
	srv.Handler().ServeHTTP(presets, httptest.NewRequest(http.MethodGet, "/v1/permissions/presets", nil))
	if presets.Code != http.StatusOK || !strings.Contains(presets.Body.String(), `"ID":"dev"`) {
		t.Fatalf("presets = %d %s", presets.Code, presets.Body.String())
	}
}

func TestAttachOnlyBootstrapPermissionsFalse(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), `"permissions":false`) {
		t.Errorf("attach-only should declare permissions false: %s", res.Body.String())
	}
}

func TestAuthServiceAPIs(t *testing.T) {
	auth := &testAuth{}
	services := &host.Services{Auth: auth}
	live := NewLive("live", t.TempDir(), nil, make(chan protocol.Op, 1))
	srv, err := New(Options{SessionDir: t.TempDir(), Services: services, Live: live})
	if err != nil {
		t.Fatal(err)
	}

	providers := httptest.NewRecorder()
	srv.Handler().ServeHTTP(providers, httptest.NewRequest(http.MethodGet, "/v1/providers", nil))
	if providers.Code != http.StatusOK || !strings.Contains(providers.Body.String(), "echo") {
		t.Fatalf("providers = %d %s", providers.Code, providers.Body.String())
	}
	if !strings.Contains(providers.Body.String(), `"methods"`) {
		t.Fatalf("providers missing methods: %s", providers.Body.String())
	}
	key := httptest.NewRecorder()
	srv.Handler().ServeHTTP(key, httptest.NewRequest(http.MethodPost, "/v1/auth/key", strings.NewReader(`{"provider":"openai","key":"fixture-only"}`)))
	if key.Code != http.StatusOK || auth.keyProvider != "openai" || auth.key != "fixture-only" {
		t.Fatalf("key = %d, auth = %#v body %s", key.Code, auth, key.Body.String())
	}
	logout := httptest.NewRecorder()
	srv.Handler().ServeHTTP(logout, httptest.NewRequest(http.MethodDelete, "/v1/auth/openai", nil))
	if logout.Code != http.StatusOK || auth.logout != "openai" {
		t.Fatalf("logout = %d, auth = %#v body %s", logout.Code, auth, logout.Body.String())
	}

	// Attach-only blocks credential mutations.
	ro, err := New(Options{SessionDir: t.TempDir(), Services: services})
	if err != nil {
		t.Fatal(err)
	}
	deny := httptest.NewRecorder()
	ro.Handler().ServeHTTP(deny, httptest.NewRequest(http.MethodPost, "/v1/auth/key", strings.NewReader(`{"provider":"openai","key":"x"}`)))
	if deny.Code != http.StatusForbidden {
		t.Fatalf("attach-only key = %d %s", deny.Code, deny.Body.String())
	}
}

type testCatalog struct {
	ids map[string][]string
}

func (c testCatalog) ModelIDs(ctx context.Context, provider string) ([]string, error) {
	infos, err := c.Models(ctx, provider)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(infos))
	for i, info := range infos {
		out[i] = info.ID
	}
	return out, nil
}

func (c testCatalog) Models(_ context.Context, provider string) ([]host.ModelInfo, error) {
	ids := c.ids[provider]
	if len(ids) == 0 {
		return nil, errNoModels(provider)
	}
	out := make([]host.ModelInfo, len(ids))
	for i, id := range ids {
		out[i] = host.ModelInfo{ID: id, Provider: provider}
	}
	return out, nil
}

func (c testCatalog) ModelsForProviders(ctx context.Context, providers []string) ([]host.ModelInfo, error) {
	var out []host.ModelInfo
	var lastErr error
	tried := 0
	for _, p := range providers {
		if p == "" {
			continue
		}
		tried++
		infos, err := c.Models(ctx, p)
		if err != nil {
			lastErr = err
			continue
		}
		out = append(out, infos...)
	}
	if len(out) == 0 && tried > 0 && lastErr != nil {
		return nil, lastErr
	}
	return out, nil
}

func (testCatalog) ContextWindow(context.Context, string, string) (int, bool, error) {
	return 0, false, nil
}
func (testCatalog) OutputLimit(context.Context, string, string) (int, bool, error) {
	return 0, false, nil
}
func (testCatalog) ResolveVariant(context.Context, string, string, string) (string, bool, error) {
	return "", false, nil
}

type catalogErr string

func (e catalogErr) Error() string { return string(e) }

func errNoModels(provider string) error {
	return catalogErr("no models listed for " + provider)
}

func TestModelsServiceAPI(t *testing.T) {
	cat := testCatalog{ids: map[string][]string{
		"openai": {"gpt-a"},
		"xai":    {"grok-b"},
		"echo":   {"echo"},
	}}
	auth := &testAuthMulti{statuses: []host.ProviderStatus{
		{Name: "openai", Authed: true},
		{Name: "xai", Authed: true},
		{Name: "echo", Authed: true, Builtin: true},
		{Name: "anthropic", Authed: false},
	}}
	services := &host.Services{Auth: auth, Catalog: cat}
	srv, err := New(Options{SessionDir: t.TempDir(), Services: services})
	if err != nil {
		t.Fatal(err)
	}

	// Filtered by provider.
	one := httptest.NewRecorder()
	srv.Handler().ServeHTTP(one, httptest.NewRequest(http.MethodGet, "/v1/models?provider=openai", nil))
	if one.Code != http.StatusOK || !strings.Contains(one.Body.String(), "gpt-a") {
		t.Fatalf("provider filter = %d %s", one.Code, one.Body.String())
	}
	if strings.Contains(one.Body.String(), "grok-b") {
		t.Fatalf("provider filter leaked other models: %s", one.Body.String())
	}

	// No provider: all authenticated.
	all := httptest.NewRecorder()
	srv.Handler().ServeHTTP(all, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if all.Code != http.StatusOK {
		t.Fatalf("all models = %d %s", all.Code, all.Body.String())
	}
	body := all.Body.String()
	for _, want := range []string{"gpt-a", "grok-b", "echo", `"provider":"openai"`, `"provider":"xai"`} {
		if !strings.Contains(body, want) {
			t.Errorf("all models missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "anthropic") {
		t.Errorf("unauthenticated provider leaked: %s", body)
	}
}

type testAuthMulti struct {
	testAuth
	statuses []host.ProviderStatus
}

func (a *testAuthMulti) Statuses() []host.ProviderStatus { return a.statuses }

func TestHistoryAndSettingsServiceAPIs(t *testing.T) {
	saved := make(chan [5]string, 1)
	services := &host.Services{History: testHistory{entries: []string{"first", "second"}}, Settings: testSettings{saved: saved}}
	live := NewLive("live", t.TempDir(), nil, make(chan protocol.Op, 1))
	srv, err := New(Options{SessionDir: t.TempDir(), Services: services, Live: live})
	if err != nil {
		t.Fatal(err)
	}

	history := httptest.NewRecorder()
	srv.Handler().ServeHTTP(history, httptest.NewRequest(http.MethodGet, "/v1/history", nil))
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), `"second"`) {
		t.Fatalf("history = %d %s", history.Code, history.Body.String())
	}

	settings := httptest.NewRecorder()
	body := `{"provider":"echo","model":"dev","agent":"build","effort":"high","mode":"plan"}`
	srv.Handler().ServeHTTP(settings, httptest.NewRequest(http.MethodPatch, "/v1/settings", strings.NewReader(body)))
	if settings.Code != http.StatusOK {
		t.Fatalf("settings = %d %s", settings.Code, settings.Body.String())
	}
	if got := <-saved; got != [5]string{"echo", "dev", "build", "high", "plan"} {
		t.Fatalf("saved = %#v", got)
	}
}

func TestSettingsRejectsUnknownAndOversizePayloads(t *testing.T) {
	services := &host.Services{Settings: testSettings{saved: make(chan [5]string, 1)}}
	live := NewLive("live", t.TempDir(), nil, make(chan protocol.Op, 1))
	srv, err := New(Options{SessionDir: t.TempDir(), Services: services, Live: live})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"unknown":  `{"unexpected":true}`,
		"oversize": `{"provider":"` + strings.Repeat("x", maxHTTPPayload) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			res := httptest.NewRecorder()
			srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPatch, "/v1/settings", strings.NewReader(body)))
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", res.Code)
			}
		})
	}
}

func TestSandboxCapabilityDenyAndExplain(t *testing.T) {
	// Deny path: no sandbox capability.
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/sandbox", nil))
	if res.Code != http.StatusNotImplemented || !strings.Contains(res.Body.String(), "sandbox capability unavailable") {
		t.Fatalf("deny GET = %d %s", res.Code, res.Body.String())
	}
	res = httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPatch, "/v1/sandbox", strings.NewReader(`{"mode":"read-only"}`)))
	if res.Code != http.StatusNotImplemented {
		t.Fatalf("deny PATCH = %d %s", res.Code, res.Body.String())
	}

	// Bootstrap advertises sandbox when Options.Sandbox is set.
	ops := make(chan protocol.Op, 1)
	live := NewLive("s1", t.TempDir(), nil, ops)
	live.SetSandbox("read-only", "bwrap", true, []string{"github.com"}, "sandbox mode: read-only\nprofile:\ntest\n")
	dials := make(chan string, 1)
	settings := testSettings{
		defaults:  host.UserDefaults{Sandbox: "workspace-write", PermissionMode: "default"},
		savedDial: dials,
	}
	srv, err = New(Options{
		SessionDir: t.TempDir(),
		Live:       live,
		Services:   &host.Services{Settings: settings},
		Sandbox: &SandboxSnapshot{
			Mode:         "read-only",
			Backend:      "bwrap",
			Available:    true,
			NetworkAllow: []string{"github.com"},
			Explain:      "sandbox mode: read-only\nprofile:\ntest\n",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	boot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if boot.Code != http.StatusOK || !strings.Contains(boot.Body.String(), `"sandbox":true`) {
		t.Fatalf("bootstrap sandbox cap = %d %s", boot.Code, boot.Body.String())
	}
	if !strings.Contains(boot.Body.String(), `"sandbox":"read-only"`) {
		t.Fatalf("bootstrap status missing sandbox: %s", boot.Body.String())
	}

	get := httptest.NewRecorder()
	srv.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/v1/sandbox", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET sandbox = %d %s", get.Code, get.Body.String())
	}
	body := get.Body.String()
	for _, want := range []string{
		`"mode":"read-only"`,
		`"backend":"bwrap"`,
		`"available":true`,
		`"github.com"`,
		`sandbox mode: read-only`,
		`"defaultMode":"workspace-write"`,
		`"canChangeDefault":true`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET sandbox missing %s: %s", want, body)
		}
	}

	// Status carries sandbox chrome.
	st := httptest.NewRecorder()
	srv.Handler().ServeHTTP(st, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if st.Code != http.StatusOK || !strings.Contains(st.Body.String(), `"sandbox":"read-only"`) {
		t.Fatalf("status = %d %s", st.Code, st.Body.String())
	}

	// PATCH default mode.
	patch := httptest.NewRecorder()
	srv.Handler().ServeHTTP(patch, httptest.NewRequest(http.MethodPatch, "/v1/sandbox", strings.NewReader(`{"mode":"workspace-write"}`)))
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH sandbox = %d %s", patch.Code, patch.Body.String())
	}
	if got := <-dials; got != "workspace-write" {
		t.Fatalf("saved dial = %q", got)
	}
}

func TestSandboxPatchYoloOffRequiresIKnow(t *testing.T) {
	dials := make(chan string, 1)
	settings := testSettings{
		defaults:  host.UserDefaults{PermissionMode: "yolo", Sandbox: "workspace-write"},
		savedDial: dials,
	}
	live := NewLive("live", t.TempDir(), nil, make(chan protocol.Op, 1))
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Services:   &host.Services{Settings: settings},
		Sandbox:    &SandboxSnapshot{Mode: "workspace-write", Available: true},
		Live:       live,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Deny without iKnow.
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPatch, "/v1/sandbox", strings.NewReader(`{"mode":"off"}`)))
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "iKnow") {
		t.Fatalf("yolo+off without iKnow = %d %s", res.Code, res.Body.String())
	}
	select {
	case got := <-dials:
		t.Fatalf("unexpected save without iKnow: %q", got)
	default:
	}

	// Allow with iKnow.
	res = httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPatch, "/v1/sandbox", strings.NewReader(`{"mode":"off","iKnow":true}`)))
	if res.Code != http.StatusOK {
		t.Fatalf("yolo+off with iKnow = %d %s", res.Code, res.Body.String())
	}
	if got := <-dials; got != "off" {
		t.Fatalf("saved = %q", got)
	}

	// Settings PATCH also gates sandbox.
	res = httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPatch, "/v1/settings", strings.NewReader(`{"sandbox":"off"}`)))
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "iKnow") {
		t.Fatalf("settings yolo+off = %d %s", res.Code, res.Body.String())
	}

	// Unknown mode rejected.
	res = httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPatch, "/v1/sandbox", strings.NewReader(`{"mode":"nope"}`)))
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "unknown sandbox") {
		t.Fatalf("unknown mode = %d %s", res.Code, res.Body.String())
	}

	// PATCH without settings capability.
	srv2, err := New(Options{SessionDir: t.TempDir(), Sandbox: &SandboxSnapshot{Mode: "off"}})
	if err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	srv2.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPatch, "/v1/sandbox", strings.NewReader(`{"mode":"read-only"}`)))
	if res.Code != http.StatusNotImplemented || !strings.Contains(res.Body.String(), "settings capability unavailable") {
		t.Fatalf("patch without settings = %d %s", res.Code, res.Body.String())
	}
}

func TestSettingsGETReturnsDefaults(t *testing.T) {
	ts := &testSettings{defaults: host.UserDefaults{Provider: "echo", Model: "dev", Sandbox: "workspace-write"}}
	srv, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{Settings: ts}})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/settings", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("GET status %d body %s", res.Code, res.Body.String())
	}
	var got map[string]any
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["provider"] != "echo" || got["model"] != "dev" {
		t.Fatalf("unexpected payload %#v", got)
	}
}

type testMemory struct {
	mu      sync.Mutex
	entries map[string]host.MemoryEntry
	exports []string
	imports []struct {
		path    string
		replace bool
	}
}

type testIssues struct {
	mu      sync.Mutex
	nextID  int
	items   map[int]host.Issue
	exports []string
}

func newTestMemory(entries ...host.MemoryEntry) *testMemory {
	m := &testMemory{entries: make(map[string]host.MemoryEntry)}
	for _, e := range entries {
		m.entries[e.Key] = e
	}
	return m
}

func newTestIssues(items ...host.Issue) *testIssues {
	iss := &testIssues{nextID: 1, items: make(map[int]host.Issue)}
	for _, item := range items {
		if item.ID >= iss.nextID {
			iss.nextID = item.ID + 1
		}
		iss.items[item.ID] = item
	}
	return iss
}

func (m *testMemory) List(tag string) ([]host.MemoryEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]host.MemoryEntry, 0, len(m.entries))
	for _, e := range m.entries {
		if tag != "" {
			found := false
			for _, t := range e.Tags {
				if t == tag {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func (m *testMemory) Get(key string) (host.MemoryEntry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	return e, ok, nil
}

func (m *testMemory) Put(key, value string, tags []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = host.MemoryEntry{Key: key, Value: value, Tags: append([]string(nil), tags...)}
	return nil
}

func (m *testMemory) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[key]; !ok {
		return errNotFound("memory: key not found")
	}
	delete(m.entries, key)
	return nil
}

func (m *testMemory) Export(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exports = append(m.exports, path)
	entries := make([]map[string]any, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, map[string]any{"key": e.Key, "value": e.Value, "tags": e.Tags})
	}
	data, err := json.MarshalIndent(map[string]any{
		"format":  "strike.memory",
		"version": 1,
		"entries": entries,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func (m *testMemory) Import(path string, replace bool) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.imports = append(m.imports, struct {
		path    string
		replace bool
	}{path, replace})
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var doc struct {
		Entries []struct {
			Key   string   `json:"key"`
			Value string   `json:"value"`
			Tags  []string `json:"tags"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, err
	}
	if replace {
		m.entries = make(map[string]host.MemoryEntry)
	}
	for _, e := range doc.Entries {
		m.entries[e.Key] = host.MemoryEntry{Key: e.Key, Value: e.Value, Tags: append([]string(nil), e.Tags...)}
	}
	return len(doc.Entries), nil
}

func (i *testIssues) List(status string) ([]host.Issue, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]host.Issue, 0, len(i.items))
	for _, item := range i.items {
		if status != "" && item.Status != status {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (i *testIssues) Get(id int) (host.Issue, bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	item, ok := i.items[id]
	return item, ok, nil
}

func (i *testIssues) Create(title, body string) (host.Issue, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	item := host.Issue{ID: i.nextID, Title: title, Body: body, Status: "open"}
	i.nextID++
	i.items[item.ID] = item
	return item, nil
}

func (i *testIssues) Update(id int, title, body, status *string) (host.Issue, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	item, ok := i.items[id]
	if !ok {
		return host.Issue{}, errNotFound("issue: not found")
	}
	if title != nil {
		item.Title = *title
	}
	if body != nil {
		item.Body = *body
	}
	if status != nil {
		item.Status = *status
	}
	i.items[id] = item
	return item, nil
}

func (i *testIssues) Close(id int) (host.Issue, error) {
	closed := "closed"
	return i.Update(id, nil, nil, &closed)
}

func (i *testIssues) Export(path string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.exports = append(i.exports, path)
	issues := make([]map[string]any, 0, len(i.items))
	for _, item := range i.items {
		issues = append(issues, map[string]any{
			"id": item.ID, "title": item.Title, "body": item.Body, "status": item.Status,
		})
	}
	data, err := json.MarshalIndent(map[string]any{
		"format":  "strike.issues",
		"version": 1,
		"next_id": i.nextID,
		"issues":  issues,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func (i *testIssues) Import(path string, replace bool) (int, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var doc struct {
		NextID int `json:"next_id"`
		Issues []struct {
			ID     int    `json:"id"`
			Title  string `json:"title"`
			Body   string `json:"body"`
			Status string `json:"status"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, err
	}
	if replace {
		i.items = make(map[int]host.Issue)
	}
	for _, item := range doc.Issues {
		i.items[item.ID] = host.Issue{ID: item.ID, Title: item.Title, Body: item.Body, Status: item.Status}
		if item.ID >= i.nextID {
			i.nextID = item.ID + 1
		}
	}
	if doc.NextID > i.nextID {
		i.nextID = doc.NextID
	}
	return len(doc.Issues), nil
}

func TestMemoryMutatingRoutes(t *testing.T) {
	mem := newTestMemory(host.MemoryEntry{Key: "prefs", Value: "use tests", Tags: []string{"project"}})
	live := NewLive("live", t.TempDir(), nil, make(chan protocol.Op))
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live, Services: &host.Services{Memory: mem}})
	if err != nil {
		t.Fatal(err)
	}

	list := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/memory", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "prefs") {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}

	put := httptest.NewRecorder()
	srv.Handler().ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/v1/memory/prefs", strings.NewReader(`{"value":"prefer table tests","tags":["project","style"]}`)))
	if put.Code != http.StatusOK || !strings.Contains(put.Body.String(), "prefer table tests") {
		t.Fatalf("put = %d %s", put.Code, put.Body.String())
	}

	exp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(exp, httptest.NewRequest(http.MethodGet, "/v1/memory/export", nil))
	if exp.Code != http.StatusOK {
		t.Fatalf("export = %d %s", exp.Code, exp.Body.String())
	}
	if ct := exp.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("export content-type = %q", ct)
	}
	if cd := exp.Header().Get("Content-Disposition"); !strings.Contains(cd, "strike-memory.json") {
		t.Fatalf("export disposition = %q", cd)
	}
	if !strings.Contains(exp.Body.String(), `"format": "strike.memory"`) && !strings.Contains(exp.Body.String(), `"format":"strike.memory"`) {
		t.Fatalf("export missing portable format: %s", exp.Body.String())
	}

	imp := httptest.NewRecorder()
	payload := `{"replace":true,"data":{"format":"strike.memory","version":1,"entries":[{"key":"imported","value":"yes","tags":["t"]}]}}`
	srv.Handler().ServeHTTP(imp, httptest.NewRequest(http.MethodPost, "/v1/memory/import", strings.NewReader(payload)))
	if imp.Code != http.StatusOK || !strings.Contains(imp.Body.String(), `"imported":1`) {
		t.Fatalf("import = %d %s", imp.Code, imp.Body.String())
	}
	got, ok, err := mem.Get("imported")
	if err != nil || !ok || got.Value != "yes" {
		t.Fatalf("imported entry = %#v ok=%v err=%v", got, ok, err)
	}

	del := httptest.NewRecorder()
	srv.Handler().ServeHTTP(del, httptest.NewRequest(http.MethodDelete, "/v1/memory/imported", nil))
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", del.Code, del.Body.String())
	}
	if _, ok, _ := mem.Get("imported"); ok {
		t.Fatal("expected imported key deleted")
	}
}

func TestMemoryIssuesMutationsBlockedInAttachOnly(t *testing.T) {
	mem := newTestMemory()
	issues := newTestIssues()
	srv, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{Memory: mem, Issues: issues}})
	if err != nil {
		t.Fatal(err)
	}
	// No Live → attach-only. Reads still work; writes are forbidden.
	list := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/memory", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("attach-only list = %d %s", list.Code, list.Body.String())
	}
	exp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(exp, httptest.NewRequest(http.MethodGet, "/v1/memory/export", nil))
	if exp.Code != http.StatusOK {
		t.Fatalf("attach-only export = %d %s", exp.Code, exp.Body.String())
	}
	for _, tc := range []struct {
		method, path, body string
	}{
		{http.MethodPut, "/v1/memory/k", `{"value":"v"}`},
		{http.MethodDelete, "/v1/memory/k", ""},
		{http.MethodPost, "/v1/memory/import", `{"path":"x.json"}`},
		{http.MethodPost, "/v1/issues", `{"title":"t"}`},
		{http.MethodPost, "/v1/issues/1/close", `{}`},
		{http.MethodPost, "/v1/issues/import", `{"path":"x.json"}`},
	} {
		res := httptest.NewRecorder()
		var body *strings.Reader
		if tc.body != "" {
			body = strings.NewReader(tc.body)
		} else {
			body = strings.NewReader("")
		}
		req := httptest.NewRequest(tc.method, tc.path, body)
		srv.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusForbidden || !strings.Contains(res.Body.String(), "read-only attach") {
			t.Errorf("%s %s = %d %s, want 403 attach-only", tc.method, tc.path, res.Code, res.Body.String())
		}
	}
}

func TestIssuesMutatingRoutes(t *testing.T) {
	issues := newTestIssues(host.Issue{ID: 7, Title: "Fix panel", Body: "Resize it", Status: "open"})
	live := NewLive("live", t.TempDir(), nil, make(chan protocol.Op))
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live, Services: &host.Services{Issues: issues}})
	if err != nil {
		t.Fatal(err)
	}

	create := httptest.NewRecorder()
	srv.Handler().ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/issues", strings.NewReader(`{"title":"Ship web","body":"parity"}`)))
	if create.Code != http.StatusCreated || !strings.Contains(create.Body.String(), "Ship web") {
		t.Fatalf("create = %d %s", create.Code, create.Body.String())
	}

	closeRes := httptest.NewRecorder()
	srv.Handler().ServeHTTP(closeRes, httptest.NewRequest(http.MethodPost, "/v1/issues/7/close", strings.NewReader(`{}`)))
	if closeRes.Code != http.StatusOK || !strings.Contains(closeRes.Body.String(), `"Status":"closed"`) && !strings.Contains(closeRes.Body.String(), `"status":"closed"`) {
		// host.Issue has no json tags — exported field names are capitalized.
		if closeRes.Code != http.StatusOK || !strings.Contains(closeRes.Body.String(), "closed") {
			t.Fatalf("close = %d %s", closeRes.Code, closeRes.Body.String())
		}
	}

	exp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(exp, httptest.NewRequest(http.MethodGet, "/v1/issues/export", nil))
	if exp.Code != http.StatusOK || !strings.Contains(exp.Body.String(), "strike.issues") {
		t.Fatalf("export = %d %s", exp.Code, exp.Body.String())
	}
	if cd := exp.Header().Get("Content-Disposition"); !strings.Contains(cd, "strike-issues.json") {
		t.Fatalf("export disposition = %q", cd)
	}

	imp := httptest.NewRecorder()
	payload := `{"replace":false,"data":{"format":"strike.issues","version":1,"next_id":20,"issues":[{"id":19,"title":"From import","body":"","status":"open"}]}}`
	srv.Handler().ServeHTTP(imp, httptest.NewRequest(http.MethodPost, "/v1/issues/import", strings.NewReader(payload)))
	if imp.Code != http.StatusOK || !strings.Contains(imp.Body.String(), `"imported":1`) {
		t.Fatalf("import = %d %s", imp.Code, imp.Body.String())
	}
}

type errNotFound string

func (e errNotFound) Error() string { return string(e) }
