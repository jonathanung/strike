package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/protocol"
)

// probeAuth is a host.Auth that also implements host.KeyProber.
type probeAuth struct {
	testAuth
	probed string
	result host.KeyProbeResult
}

func (a *probeAuth) Statuses() []host.ProviderStatus {
	return []host.ProviderStatus{
		{Name: "echo", Builtin: true, Authed: true},
		{Name: "openai", APIKey: true, Authed: true, KeyState: host.KeyStored},
	}
}

func (a *probeAuth) ProbeAPIKey(_ context.Context, provider string) host.KeyProbeResult {
	a.probed = provider
	return a.result
}

func newProbeServer(t *testing.T, auth host.Auth) *Server {
	t.Helper()
	services := &host.Services{Auth: auth}
	live := NewLive("live", t.TempDir(), nil, make(chan protocol.Op, 1))
	srv, err := New(Options{SessionDir: t.TempDir(), Services: services, Live: live})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestAuthProbeReportsVerdictAndRefreshedStatus(t *testing.T) {
	auth := &probeAuth{result: host.KeyProbeResult{Status: host.ProbeInvalid, Detail: "the provider refused the stored key (HTTP 401)"}}
	srv := newProbeServer(t, auth)
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/auth/probe", strings.NewReader(`{"provider":"openai"}`)))
	if res.Code != http.StatusOK {
		t.Fatalf("probe = %d %s, want 200", res.Code, res.Body.String())
	}
	if auth.probed != "openai" {
		t.Fatalf("probed provider = %q, want openai", auth.probed)
	}
	var body struct {
		Status         string         `json:"status"`
		Detail         string         `json:"detail"`
		ProviderStatus map[string]any `json:"providerStatus"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != host.ProbeInvalid {
		t.Errorf("status = %q, want %q", body.Status, host.ProbeInvalid)
	}
	if body.ProviderStatus["keyState"] != host.KeyStored {
		t.Errorf("providerStatus.keyState = %v, want %q", body.ProviderStatus["keyState"], host.KeyStored)
	}
}

func TestAuthProbeRejectsUnknownAndMissingProvider(t *testing.T) {
	srv := newProbeServer(t, &probeAuth{})
	for _, payload := range []string{`{"provider":"nosuch"}`, `{}`, `{"provider":"  "}`} {
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/auth/probe", strings.NewReader(payload)))
		if res.Code != http.StatusBadRequest {
			t.Errorf("probe %s = %d, want 400", payload, res.Code)
		}
	}
}

func TestAuthProbeUnavailableWithoutProber(t *testing.T) {
	srv := newProbeServer(t, &testAuth{})
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/auth/probe", strings.NewReader(`{"provider":"echo"}`)))
	if res.Code != http.StatusNotImplemented {
		t.Fatalf("probe without prober = %d, want 501", res.Code)
	}
}

func TestProvidersDTOIncludesKeyState(t *testing.T) {
	srv := newProbeServer(t, &probeAuth{})
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/v1/providers", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("providers = %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), `"keyState":"stored"`) {
		t.Errorf("providers DTO missing keyState: %s", res.Body.String())
	}
}
