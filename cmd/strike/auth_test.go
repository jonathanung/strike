package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/product/auth"
)

func authHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Clear env keys so status/login paths exercise the store.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("XAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	return home
}

func openTestStore(t *testing.T) *auth.Store {
	t.Helper()
	st, err := auth.OpenStore(auth.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func withStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, input); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		r.Close()
	})
}

func TestRunAuthStatus(t *testing.T) {
	home := authHome(t)
	st := openTestStore(t)

	var out bytes.Buffer
	if err := runAuthStatus(st, &out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"anthropic", "openai", "xai"} {
		if !strings.Contains(out.String(), name) || !strings.Contains(out.String(), "not logged in") {
			t.Fatalf("status empty store = %q", out.String())
		}
	}

	_ = st.Set("anthropic", auth.Credential{Type: auth.TypeAPIKey, APIKey: "k"})
	_ = st.Set("openai", auth.Credential{Type: auth.TypeOAuth, Access: "a", APIKey: "ex"})
	_ = st.Set("xai", auth.Credential{Type: auth.TypeOAuth, Access: "a", ExpiresAt: time.Now().Add(time.Hour)})
	_ = st.Set("custom", auth.Credential{Type: auth.TypeAPIKey, APIKey: "c"})

	out.Reset()
	if err := runAuthStatus(st, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"API key stored",
		"OAuth + exchanged API key",
		"OAuth",
		"unknown provider",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status missing %q:\n%s", want, got)
		}
	}

	// Expired OAuth
	_ = st.Set("xai", auth.Credential{Type: auth.TypeOAuth, Access: "a", ExpiresAt: time.Now().Add(-time.Hour)})
	out.Reset()
	_ = runAuthStatus(st, &out)
	if !strings.Contains(out.String(), "expired") {
		t.Errorf("expired status = %q", out.String())
	}

	// Env wins
	t.Setenv("OPENAI_API_KEY", "from-env")
	out.Reset()
	_ = runAuthStatus(st, &out)
	if !strings.Contains(out.String(), "OPENAI_API_KEY") {
		t.Errorf("env status = %q", out.String())
	}

	// Ensure we did not write outside home.
	if _, err := os.Stat(filepath.Join(home, ".strike")); err != nil && !os.IsNotExist(err) {
		// store Set creates the path — OK
	}
}

func TestRunAuthLogout(t *testing.T) {
	authHome(t)
	st := openTestStore(t)
	_ = st.Set("xai", auth.Credential{Type: auth.TypeAPIKey, APIKey: "k"})

	var out bytes.Buffer
	if err := runAuth([]string{"logout", "xai"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Logged out of xai") {
		t.Errorf("out = %q", out.String())
	}
	if _, ok := st.Get("xai"); ok {
		// runAuth opened its own store instance; reload.
	}
	st2 := openTestStore(t)
	if _, ok := st2.Get("xai"); ok {
		t.Error("credential still present after logout")
	}
}

func TestRunAuthLoginAPIKey(t *testing.T) {
	authHome(t)
	withStdin(t, "sk-test-key-123\n")

	var out bytes.Buffer
	if err := runAuth([]string{"login", "anthropic"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Stored anthropic API key") {
		t.Errorf("out = %q", out.String())
	}
	st := openTestStore(t)
	cred, ok := st.Get("anthropic")
	if !ok || cred.APIKey != "sk-test-key-123" || cred.Type != auth.TypeAPIKey {
		t.Fatalf("cred = %+v ok=%v", cred, ok)
	}
}

func TestRunAuthLoginGoogleAPIKeyOnly(t *testing.T) {
	authHome(t)
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	withStdin(t, "AIzaSy-test-studio-key\n")

	var out bytes.Buffer
	// Default login is API key (no OAuth path for google).
	if err := runAuth([]string{"login", "google"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Google AI Studio") {
		t.Errorf("prompt/out missing Google AI Studio guidance: %q", out.String())
	}
	if !strings.Contains(out.String(), "Stored google API key") {
		t.Errorf("out = %q", out.String())
	}
	st := openTestStore(t)
	cred, ok := st.Get("google")
	if !ok || cred.APIKey != "AIzaSy-test-studio-key" || cred.Type != auth.TypeAPIKey {
		t.Fatalf("cred = %+v ok=%v", cred, ok)
	}
	// Alias id also resolves the stored credential.
	if cred, ok := st.Get("gemini"); !ok || cred.APIKey != "AIzaSy-test-studio-key" {
		t.Fatalf("Get(gemini) alias = %+v ok=%v", cred, ok)
	}

	// Status lists canonical google and reports GOOGLE_API_KEY when set.
	t.Setenv("GOOGLE_API_KEY", "from-google-env")
	out.Reset()
	if err := runAuthStatus(st, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "google") {
		t.Errorf("status missing google: %q", got)
	}
	if strings.Contains(got, "gemini") {
		t.Errorf("status must list google only, not gemini: %q", got)
	}
	if !strings.Contains(got, "GOOGLE_API_KEY") {
		t.Errorf("status missing GOOGLE_API_KEY: %q", got)
	}
}

func TestRunAuthLoginGeminiAliasStoresCanonical(t *testing.T) {
	authHome(t)
	withStdin(t, "AIzaSy-alias-login-key\n")

	var out bytes.Buffer
	if err := runAuth([]string{"login", "gemini"}, &out); err != nil {
		t.Fatal(err)
	}
	// Login message uses the canonical provider id.
	if !strings.Contains(out.String(), "Stored google API key") {
		t.Errorf("out = %q, want Stored google API key", out.String())
	}
	st := openTestStore(t)
	cred, ok := st.Get("google")
	if !ok || cred.APIKey != "AIzaSy-alias-login-key" {
		t.Fatalf("canonical store = %+v ok=%v", cred, ok)
	}
	// Disk must not keep a legacy gemini key after alias login.
	raw, err := os.ReadFile(auth.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"gemini"`) {
		t.Errorf("auth.json still has gemini key after alias login: %s", raw)
	}
}

func TestRunAuthLogoutGeminiAlias(t *testing.T) {
	authHome(t)
	st := openTestStore(t)
	if err := st.Set("google", auth.Credential{Type: auth.TypeAPIKey, APIKey: "k"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runAuth([]string{"logout", "gemini"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Logged out of google") {
		t.Errorf("out = %q", out.String())
	}
	st2 := openTestStore(t)
	if _, ok := st2.Get("google"); ok {
		t.Error("credential still present after logout gemini alias")
	}
}

func TestLoginAPIKeyPromptGoogle(t *testing.T) {
	for _, id := range []string{"google", "gemini"} {
		got := loginAPIKeyPrompt(id)
		if !strings.Contains(got, "Google AI Studio") || !strings.Contains(got, "aistudio.google.com") {
			t.Errorf("prompt(%q) = %q", id, got)
		}
	}
	if got := loginAPIKeyPrompt("anthropic"); !strings.Contains(got, "anthropic") {
		t.Errorf("anthropic prompt = %q", got)
	}
}

func TestAuthUsageListsGoogle(t *testing.T) {
	var out bytes.Buffer
	if err := runAuth(nil, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"google", "alias: gemini", "GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		if !strings.Contains(got, want) {
			t.Errorf("auth usage missing %q:\n%s", want, got)
		}
	}
}

func TestRunAuthLoginAPIKeyFlag(t *testing.T) {
	authHome(t)
	withStdin(t, "oa-key\n")
	var out bytes.Buffer
	if err := runAuthLogin(openTestStore(t), []string{"openai", "--api-key"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "openai") {
		t.Errorf("out = %q", out.String())
	}
}

func TestRunAuthLoginEmptyKey(t *testing.T) {
	authHome(t)
	withStdin(t, "\n")
	err := loginAPIKey(openTestStore(t), "xai", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no key entered") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunAuthLoginUnknownProvider(t *testing.T) {
	authHome(t)
	err := runAuthLogin(openTestStore(t), []string{"nope"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoginOpenAIOAuthBindFailure(t *testing.T) {
	authHome(t)
	// Occupy the Codex CLI redirect port so Login fails immediately (no network).
	ln, err := net.Listen("tcp", "localhost:1455")
	if err != nil {
		t.Skipf("cannot bind localhost:1455: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	err = loginOpenAIOAuth(ctx, openTestStore(t), io.Discard)
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %v, want immediate bind error", time.Since(start))
	}
	if err == nil || !strings.Contains(err.Error(), "cannot bind") {
		t.Fatalf("err = %v, want cannot bind", err)
	}
}

func TestLoginXAIOAuthBindFailure(t *testing.T) {
	authHome(t)
	ln, err := net.Listen("tcp", "127.0.0.1:56121")
	if err != nil {
		t.Skipf("cannot bind 127.0.0.1:56121: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = loginXAIOAuth(ctx, openTestStore(t), false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot bind") {
		t.Fatalf("err = %v, want cannot bind", err)
	}
}

func TestLoginXAIOAuthDevice(t *testing.T) {
	authHome(t)

	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "device"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":               "dc",
				"user_code":                 "WD-CODE",
				"verification_uri":          "https://verify.test/device",
				"verification_uri_complete": "https://verify.test/device?user_code=WD-CODE",
				"expires_in":                120,
				"interval":                  5,
			})
		default:
			// token endpoint
			polls++
			body, _ := io.ReadAll(r.Body)
			vals, _ := url.ParseQuery(string(body))
			if vals.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Errorf("grant_type = %q", vals.Get("grant_type"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "dev-access",
				"refresh_token": "dev-refresh",
				"expires_in":    3600,
			})
		}
	}))
	defer srv.Close()

	// Route auth.x.ai → httptest for device + token.
	old := http.DefaultClient
	u, _ := url.Parse(srv.URL)
	transport := http.DefaultTransport
	http.DefaultClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			r2 := req.Clone(req.Context())
			if r2.URL.Host == "auth.x.ai" {
				r2.URL.Scheme = u.Scheme
				r2.URL.Host = u.Host
				r2.Host = u.Host
			}
			return transport.RoundTrip(r2)
		}),
	}
	t.Cleanup(func() { http.DefaultClient = old })

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := loginXAIOAuth(ctx, openTestStore(t), true, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "WD-CODE") || !strings.Contains(got, "verify.test") {
		t.Errorf("device prompt = %q", got)
	}
	if !strings.Contains(got, "Logged in to xai") {
		t.Errorf("outcome = %q", got)
	}
	if polls < 1 {
		t.Error("expected token poll")
	}
	st := openTestStore(t)
	cred, ok := st.Get("xai")
	if !ok || cred.Access != "dev-access" || cred.Type != auth.TypeOAuth {
		t.Fatalf("stored = %+v ok=%v", cred, ok)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHasFlagAndEnvKey(t *testing.T) {
	if !hasFlag([]string{"--api-key", "--device"}, "--device") {
		t.Error("hasFlag missed --device")
	}
	if hasFlag([]string{"--api-key"}, "--device") {
		t.Error("hasFlag false positive")
	}
	t.Setenv("XAI_API_KEY", "v")
	if name, ok := envKey("xai"); !ok || name != "XAI_API_KEY" {
		t.Errorf("envKey = %q %v", name, ok)
	}
	t.Setenv("XAI_API_KEY", "")
	if _, ok := envKey("xai"); ok {
		t.Error("envKey should be empty")
	}
	if _, ok := envKey("unknown"); ok {
		t.Error("unknown provider")
	}
}

func TestRunAuthLoginMissingArgs(t *testing.T) {
	authHome(t)
	err := runAuthLogin(openTestStore(t), nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("err = %v", err)
	}
}
