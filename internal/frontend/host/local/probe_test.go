package local

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/product/auth"
	"github.com/jonathanung/strike/internal/product/config"
)

func TestKeyProbeRequestShapes(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		dialect string
		wantURL string
		wantHdr string
		wantVal string
	}{
		{"anthropic origin appends v1", "https://api.anthropic.com", dialectAnthropic, "https://api.anthropic.com/v1/models", "x-api-key", "k"},
		{"anthropic v1 base", "https://api.anthropic.com/v1", dialectAnthropic, "https://api.anthropic.com/v1/models", "x-api-key", "k"},
		{"anthropic trailing slash", "https://api.anthropic.com/", dialectAnthropic, "https://api.anthropic.com/v1/models", "x-api-key", "k"},
		{"openai bearer", "https://api.openai.com/v1", dialectOpenAI, "https://api.openai.com/v1/models", "Authorization", "Bearer k"},
		{"google header", "https://generativelanguage.googleapis.com/v1beta", dialectGoogle, "https://generativelanguage.googleapis.com/v1beta/models", "x-goog-api-key", "k"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, h, err := keyProbeRequest(tc.base, tc.dialect, "k", nil)
			if err != nil {
				t.Fatal(err)
			}
			if u != tc.wantURL {
				t.Errorf("url = %q, want %q", u, tc.wantURL)
			}
			if got := h.Get(tc.wantHdr); got != tc.wantVal {
				t.Errorf("header %s = %q, want %q", tc.wantHdr, got, tc.wantVal)
			}
			if tc.dialect == dialectAnthropic && h.Get("anthropic-version") == "" {
				t.Error("anthropic probe is missing anthropic-version")
			}
		})
	}
}

func TestKeyProbeRequestRejectsBadBase(t *testing.T) {
	for _, base := range []string{"", "not a url", "ftp://example.com", "https://"} {
		if _, _, err := keyProbeRequest(base, dialectOpenAI, "k", nil); err == nil {
			t.Errorf("base %q: want error", base)
		}
	}
}

func TestClassifyProbe(t *testing.T) {
	cases := map[int]string{
		200: host.ProbeValid,
		204: host.ProbeValid,
		401: host.ProbeInvalid,
		403: host.ProbeInvalid,
		404: host.ProbeUnreachable,
		429: host.ProbeUnreachable,
		500: host.ProbeUnreachable,
	}
	for code, want := range cases {
		if got := classifyProbe(code); got != want {
			t.Errorf("classifyProbe(%d) = %q, want %q", code, got, want)
		}
	}
}

func prober(t *testing.T, svc host.Services) host.KeyProber {
	t.Helper()
	p, ok := svc.Auth.(host.KeyProber)
	if !ok {
		t.Fatal("local Auth does not implement host.KeyProber")
	}
	return p
}

func TestProbeNoKeyUnsupportedAndUnknown(t *testing.T) {
	svc, _ := newTestServices(t)
	p := prober(t, svc)
	if res := p.ProbeAPIKey(context.Background(), "anthropic"); res.Status != host.ProbeNoKey {
		t.Errorf("anthropic without key = %q, want %q", res.Status, host.ProbeNoKey)
	}
	if res := p.ProbeAPIKey(context.Background(), "echo"); res.Status != host.ProbeUnsupported {
		t.Errorf("echo = %q, want %q", res.Status, host.ProbeUnsupported)
	}
	if res := p.ProbeAPIKey(context.Background(), "nosuch"); res.Status != host.ProbeUnsupported {
		t.Errorf("unknown = %q, want %q", res.Status, host.ProbeUnsupported)
	}
}

func TestProbeOAuthOnlyIsUnsupported(t *testing.T) {
	svc, store := newTestServices(t)
	if err := store.Set("openai", auth.Credential{Type: auth.TypeOAuth, Access: "tok"}); err != nil {
		t.Fatal(err)
	}
	res := prober(t, svc).ProbeAPIKey(context.Background(), "openai")
	if res.Status != host.ProbeUnsupported {
		t.Errorf("oauth-only openai = %q (%s), want %q", res.Status, res.Detail, host.ProbeUnsupported)
	}
}

// TestProbeStoredKeyIsNotVerifiedUntilProbed is the #1531 regression test:
// storing a key must surface KeyStored, and only a probe the vendor answers
// 2xx may flip the state to KeyVerified.
func TestProbeStoredKeyIsNotVerifiedUntilProbed(t *testing.T) {
	var sawAuth, sawPath string
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawPath = r.URL.Path
		if sawAuth == "Bearer test-good-key" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer vendor.Close()

	svc, store := newTestServices(t)
	customs := config.NewCustomStore([]config.CustomProvider{{
		Name: "stub", BaseURL: vendor.URL + "/v1", API: config.WireOpenAI, Models: []string{"m1"},
	}}, "")
	svc = New(store, nil, nil, nil, nil, nil, customs, "")
	p := prober(t, svc)

	if err := svc.Auth.SetAPIKey("stub", "test-bad-key"); err != nil {
		t.Fatal(err)
	}
	if got := statusByName(svc.Auth.Statuses())["stub"].KeyState; got != host.KeyStored {
		t.Fatalf("stored key KeyState = %q, want %q (never pre-verified)", got, host.KeyStored)
	}
	res := p.ProbeAPIKey(context.Background(), "stub")
	if res.Status != host.ProbeInvalid {
		t.Fatalf("bad key probe = %q (%s), want %q", res.Status, res.Detail, host.ProbeInvalid)
	}
	if sawPath != "/v1/models" {
		t.Errorf("probe path = %q, want /v1/models", sawPath)
	}
	if got := statusByName(svc.Auth.Statuses())["stub"].KeyState; got != host.KeyRejected {
		t.Fatalf("rejected key KeyState = %q, want %q", got, host.KeyRejected)
	}

	// Replacing the key resets verification, then a good probe verifies it.
	if err := svc.Auth.SetAPIKey("stub", "test-good-key"); err != nil {
		t.Fatal(err)
	}
	if got := statusByName(svc.Auth.Statuses())["stub"].KeyState; got != host.KeyStored {
		t.Fatalf("replaced key KeyState = %q, want %q", got, host.KeyStored)
	}
	res = p.ProbeAPIKey(context.Background(), "stub")
	if res.Status != host.ProbeValid {
		t.Fatalf("good key probe = %q (%s), want %q", res.Status, res.Detail, host.ProbeValid)
	}
	if got := statusByName(svc.Auth.Statuses())["stub"].KeyState; got != host.KeyVerified {
		t.Fatalf("verified key KeyState = %q, want %q", got, host.KeyVerified)
	}

	// Logout clears the recorded verdict.
	if err := svc.Auth.Logout("stub"); err != nil {
		t.Fatal(err)
	}
	if got := statusByName(svc.Auth.Statuses())["stub"].KeyState; got == host.KeyVerified {
		t.Fatal("KeyState still verified after logout")
	}
}

func TestProbeAnthropicDialectCustom(t *testing.T) {
	var sawKey, sawPath string
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawKey = r.Header.Get("x-api-key")
		sawPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer vendor.Close()

	svc, store := newTestServices(t)
	customs := config.NewCustomStore([]config.CustomProvider{{
		Name: "astub", BaseURL: vendor.URL, API: config.WireAnthropic, Models: []string{"m1"},
	}}, "")
	svc = New(store, nil, nil, nil, nil, nil, customs, "")
	if err := svc.Auth.SetAPIKey("astub", "test-ant-key"); err != nil {
		t.Fatal(err)
	}
	res := prober(t, svc).ProbeAPIKey(context.Background(), "astub")
	if res.Status != host.ProbeValid {
		t.Fatalf("anthropic-dialect probe = %q (%s), want %q", res.Status, res.Detail, host.ProbeValid)
	}
	if sawKey != "test-ant-key" {
		t.Errorf("x-api-key = %q, want the stored key", sawKey)
	}
	if sawPath != "/v1/models" {
		t.Errorf("probe path = %q, want /v1/models", sawPath)
	}
}

func TestProbeUnreachableKeepsStoredState(t *testing.T) {
	svc, store := newTestServices(t)
	customs := config.NewCustomStore([]config.CustomProvider{{
		Name: "down", BaseURL: "http://127.0.0.1:1/v1", API: config.WireOpenAI, Models: []string{"m1"},
	}}, "")
	svc = New(store, nil, nil, nil, nil, nil, customs, "")
	if err := svc.Auth.SetAPIKey("down", "test-key"); err != nil {
		t.Fatal(err)
	}
	res := prober(t, svc).ProbeAPIKey(context.Background(), "down")
	if res.Status != host.ProbeUnreachable {
		t.Fatalf("dead endpoint probe = %q (%s), want %q", res.Status, res.Detail, host.ProbeUnreachable)
	}
	if got := statusByName(svc.Auth.Statuses())["down"].KeyState; got != host.KeyStored {
		t.Fatalf("unreachable probe KeyState = %q, want %q (inconclusive is not a verdict)", got, host.KeyStored)
	}
}

func TestProbeServerErrorIsUnreachable(t *testing.T) {
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer vendor.Close()
	svc, store := newTestServices(t)
	customs := config.NewCustomStore([]config.CustomProvider{{
		Name: "sad", BaseURL: vendor.URL + "/v1", API: config.WireOpenAI, Models: []string{"m1"},
	}}, "")
	svc = New(store, nil, nil, nil, nil, nil, customs, "")
	if err := svc.Auth.SetAPIKey("sad", "test-key"); err != nil {
		t.Fatal(err)
	}
	if res := prober(t, svc).ProbeAPIKey(context.Background(), "sad"); res.Status != host.ProbeUnreachable {
		t.Fatalf("HTTP 500 probe = %q, want %q", res.Status, host.ProbeUnreachable)
	}
}

func TestProbeEnvKeyCountsAsStored(t *testing.T) {
	svc, _ := newTestServices(t)
	t.Setenv("ANTHROPIC_API_KEY", "test-env-key")
	st := statusByName(svc.Auth.Statuses())["anthropic"]
	if st.KeyState != host.KeyStored {
		t.Fatalf("env key KeyState = %q, want %q", st.KeyState, host.KeyStored)
	}
	if !st.Authed {
		t.Fatal("env key should still report Authed (credential present)")
	}
}
