package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host"
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

type testSettings struct{ saved chan [5]string }

func (testSettings) Defaults() host.UserDefaults { return host.UserDefaults{} }

func (s testSettings) SaveDefaults(provider, model, agent, effort, mode string) error {
	s.saved <- [5]string{provider, model, agent, effort, mode}
	return nil
}
func (testSettings) SaveTheme(string) error                        { return nil }
func (testSettings) SavePresentation(string, string, string) error { return nil }

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

func TestServiceAPIsUnavailableWithoutConfiguredHost(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/providers", "/v1/models?provider=echo", "/v1/history", "/v1/files", "/v1/memory", "/v1/issues"} {
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code != http.StatusNotImplemented || !strings.Contains(res.Body.String(), "capability unavailable") {
			t.Errorf("GET %s = %d %q, want tested unavailable state", path, res.Code, res.Body.String())
		}
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
