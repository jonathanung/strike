package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	srv, err := New(Options{SessionDir: t.TempDir(), Services: services})
	if err != nil {
		t.Fatal(err)
	}

	providers := httptest.NewRecorder()
	srv.Handler().ServeHTTP(providers, httptest.NewRequest(http.MethodGet, "/v1/providers", nil))
	if providers.Code != http.StatusOK || !strings.Contains(providers.Body.String(), "echo") {
		t.Fatalf("providers = %d %s", providers.Code, providers.Body.String())
	}
	key := httptest.NewRecorder()
	srv.Handler().ServeHTTP(key, httptest.NewRequest(http.MethodPost, "/v1/auth/key", strings.NewReader(`{"provider":"openai","key":"fixture-only"}`)))
	if key.Code != http.StatusOK || auth.keyProvider != "openai" || auth.key != "fixture-only" {
		t.Fatalf("key = %d, auth = %#v", key.Code, auth)
	}
	logout := httptest.NewRecorder()
	srv.Handler().ServeHTTP(logout, httptest.NewRequest(http.MethodDelete, "/v1/auth/openai", nil))
	if logout.Code != http.StatusNoContent || auth.logout != "openai" {
		t.Fatalf("logout = %d, auth = %#v", logout.Code, auth)
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
	for _, want := range []string{"gpt-a", "grok-b", "echo", `"Provider":"openai"`, `"Provider":"xai"`} {
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
	srv, err := New(Options{SessionDir: t.TempDir(), Services: services})
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
	srv, err := New(Options{SessionDir: t.TempDir(), Services: services})
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
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Services:   &host.Services{Settings: settings},
		Sandbox:    &SandboxSnapshot{Mode: "workspace-write", Available: true},
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
