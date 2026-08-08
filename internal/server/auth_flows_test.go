package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

type testAuthDevice struct {
	testAuth
	deviceErr error
	oauthErr  error
	device    *host.DeviceLogin
	oauth     *host.OAuthLogin
}

func (a *testAuthDevice) BeginDevice(ctx context.Context, provider string) (*host.DeviceLogin, error) {
	if a.deviceErr != nil {
		return nil, a.deviceErr
	}
	if a.device != nil {
		return a.device, nil
	}
	return host.NewDeviceLogin("WD-TEST", "https://verify.test/device", func(ctx context.Context) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(20 * time.Millisecond):
			return "device ok", nil
		}
	}), nil
}

func (a *testAuthDevice) BeginOAuth(ctx context.Context, provider string) (*host.OAuthLogin, error) {
	if a.oauthErr != nil {
		return nil, a.oauthErr
	}
	if a.oauth != nil {
		return a.oauth, nil
	}
	return nil, context.Canceled
}

func (a *testAuthDevice) Statuses() []host.ProviderStatus {
	return []host.ProviderStatus{
		{Name: "xai", Detail: "none", Authed: false, OAuth: true, Device: true, APIKey: true},
		{Name: "echo", Detail: "offline", Authed: true, Builtin: true},
	}
}

type testProviders struct {
	items map[string]host.CustomProvider
}

func (p *testProviders) List() []host.CustomProvider {
	out := make([]host.CustomProvider, 0, len(p.items))
	for _, v := range p.items {
		out = append(out, v)
	}
	return out
}
func (p *testProviders) Get(name string) (host.CustomProvider, bool) {
	v, ok := p.items[name]
	return v, ok
}
func (p *testProviders) Upsert(cp host.CustomProvider) error {
	if p.items == nil {
		p.items = map[string]host.CustomProvider{}
	}
	if strings.TrimSpace(cp.Name) == "" {
		return context.Canceled
	}
	p.items[cp.Name] = cp
	return nil
}
func (p *testProviders) Remove(name string) error {
	delete(p.items, name)
	return nil
}

func TestDeviceLoginFlow(t *testing.T) {
	auth := &testAuthDevice{}
	live := NewLive("live", t.TempDir(), nil, make(chan protocol.Op, 1))
	srv, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{Auth: auth}, Live: live})
	if err != nil {
		t.Fatal(err)
	}
	start := httptest.NewRecorder()
	srv.Handler().ServeHTTP(start, httptest.NewRequest(http.MethodPost, "/v1/auth/device", strings.NewReader(`{"provider":"xai"}`)))
	if start.Code != http.StatusOK {
		t.Fatalf("start = %d %s", start.Code, start.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(start.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	id, _ := body["id"].(string)
	if id == "" || body["userCode"] != "WD-TEST" {
		t.Fatalf("body = %#v", body)
	}
	// Poll until completed
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := httptest.NewRecorder()
		srv.Handler().ServeHTTP(st, httptest.NewRequest(http.MethodGet, "/v1/auth/device/"+id, nil))
		if st.Code != http.StatusOK {
			t.Fatalf("status = %d %s", st.Code, st.Body.String())
		}
		var got map[string]any
		_ = json.NewDecoder(st.Body).Decode(&got)
		if got["status"] == "completed" {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("device login did not complete")
}

func TestOAuthUnsupportedMessage(t *testing.T) {
	auth := &testAuth{} // echo only, no oauth
	live := NewLive("live", t.TempDir(), nil, make(chan protocol.Op, 1))
	srv, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{Auth: auth}, Live: live})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/v1/auth/oauth", strings.NewReader(`{"provider":"echo"}`)))
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "OAuth is unavailable") {
		t.Fatalf("oauth = %d %s", res.Code, res.Body.String())
	}
}

func TestCustomProvidersCRUD(t *testing.T) {
	p := &testProviders{items: map[string]host.CustomProvider{}}
	live := NewLive("live", t.TempDir(), nil, make(chan protocol.Op, 1))
	srv, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{Providers: p}, Live: live})
	if err != nil {
		t.Fatal(err)
	}
	// bootstrap cap
	boot := httptest.NewRecorder()
	srv.Handler().ServeHTTP(boot, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if !strings.Contains(boot.Body.String(), `"providers":true`) {
		t.Fatalf("bootstrap: %s", boot.Body.String())
	}
	create := httptest.NewRecorder()
	srv.Handler().ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/v1/custom-providers", strings.NewReader(
		`{"name":"acme","baseURL":"https://api.acme.test/v1","api":"openai","models":["m1"]}`,
	)))
	if create.Code != http.StatusOK || !strings.Contains(create.Body.String(), "acme") {
		t.Fatalf("create = %d %s", create.Code, create.Body.String())
	}
	if strings.Contains(create.Body.String(), "sk-") || strings.Contains(create.Body.String(), "key") && strings.Contains(create.Body.String(), "secret") {
		t.Fatalf("must not return credentials: %s", create.Body.String())
	}
	list := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/custom-providers", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "acme") {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	del := httptest.NewRecorder()
	srv.Handler().ServeHTTP(del, httptest.NewRequest(http.MethodDelete, "/v1/custom-providers/acme", nil))
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", del.Code, del.Body.String())
	}
	// attach-only deny
	ro, _ := New(Options{SessionDir: t.TempDir(), Services: &host.Services{Providers: p}})
	deny := httptest.NewRecorder()
	ro.Handler().ServeHTTP(deny, httptest.NewRequest(http.MethodPost, "/v1/custom-providers", strings.NewReader(
		`{"name":"x","baseURL":"https://x.test","api":"openai"}`,
	)))
	if deny.Code != http.StatusForbidden {
		t.Fatalf("attach-only = %d %s", deny.Code, deny.Body.String())
	}
}

func TestRedactAuthErrBounds(t *testing.T) {
	long := strings.Repeat("sk-secret-token-", 40)
	got := redactAuthErr(context.DeadlineExceeded)
	if got == "" {
		// deadline may not look like a secret; just ensure no panic
		_ = long
	}
	got = redactAuthErr(&longErr{s: long})
	if len(got) > 300 {
		t.Fatalf("not bounded: %d", len(got))
	}
}

type longErr struct{ s string }

func (e *longErr) Error() string { return e.s }
