package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rewriteDefaultClientHost routes DefaultClient requests for host to baseURL.
func rewriteDefaultClientHost(t *testing.T, baseURL, host string) {
	t.Helper()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	old := http.DefaultClient
	transport := http.DefaultTransport
	if old != nil && old.Transport != nil {
		transport = old.Transport
	}
	http.DefaultClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			r2 := req.Clone(req.Context())
			if r2.URL.Host == host {
				r2.URL.Scheme = u.Scheme
				r2.URL.Host = u.Host
				r2.Host = u.Host
			}
			return transport.RoundTrip(r2)
		}),
	}
	t.Cleanup(func() { http.DefaultClient = old })
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + body + ".sig"
}

func TestCompleteLoginXAI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	msg, err := CompleteLogin(context.Background(), st, "xai", &Tokens{
		Access: "a", Refresh: "r", IDToken: "id", ExpiresAt: exp,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "xai") {
		t.Errorf("message = %q", msg)
	}
	cred, ok := st.Get("xai")
	if !ok || cred.Type != TypeOAuth || cred.Access != "a" || cred.Refresh != "r" {
		t.Fatalf("cred = %+v ok=%v", cred, ok)
	}
	if !cred.ExpiresAt.Equal(exp) {
		t.Errorf("ExpiresAt = %v, want %v", cred.ExpiresAt, exp)
	}
}

func TestCompleteLoginOpenAISubscriptionAndExchange(t *testing.T) {
	var sawExchange bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		vals, _ := url.ParseQuery(string(body))
		if vals.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" {
			t.Errorf("grant_type = %q", vals.Get("grant_type"))
		}
		if vals.Get("requested_token") != "openai-api-key" {
			t.Errorf("requested_token = %q", vals.Get("requested_token"))
		}
		sawExchange = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "sk-exchanged",
			"expires_in":   3600,
		})
	}))
	defer srv.Close()
	rewriteDefaultClientHost(t, srv.URL, "auth.openai.com")

	path := filepath.Join(t.TempDir(), "auth.json")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	idTok := fakeJWT(map[string]any{"chatgpt_account_id": "acct-99"})
	msg, err := CompleteLogin(context.Background(), st, "openai", &Tokens{
		Access: "oa", Refresh: "or", IDToken: idTok, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawExchange {
		t.Error("expected API key exchange request")
	}
	if !strings.Contains(msg, "ChatGPT subscription mode") {
		t.Errorf("message = %q", msg)
	}
	cred, ok := st.Get("openai")
	if !ok || cred.AccountID != "acct-99" || cred.APIKey != "sk-exchanged" {
		t.Fatalf("cred = %+v", cred)
	}
}

func TestCompleteLoginOpenAINoAccountIDWarns(t *testing.T) {
	// Exchange fails (no rewrite) — CompleteLogin still stores OAuth tokens.
	path := filepath.Join(t.TempDir(), "auth.json")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	// Opaque tokens: no JWT account id; exchange will fail against real host —
	// block network by pointing DefaultClient at a closed local server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", 500)
	}))
	// Close immediately so exchange fails fast.
	srv.Close()
	rewriteDefaultClientHost(t, srv.URL, "auth.openai.com")

	msg, err := CompleteLogin(context.Background(), st, "openai", &Tokens{
		Access: "opaque", Refresh: "r", IDToken: "also-opaque", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "no ChatGPT account id") {
		t.Errorf("message = %q", msg)
	}
	cred, ok := st.Get("openai")
	if !ok || cred.Access != "opaque" || cred.APIKey != "" {
		t.Fatalf("cred = %+v", cred)
	}
}

func TestOpenAIExchangeAPIKey(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/oauth/token" {
				t.Errorf("path = %s", r.URL.Path)
			}
			body, _ := io.ReadAll(r.Body)
			vals, _ := url.ParseQuery(string(body))
			if vals.Get("subject_token") != "id-tok" {
				t.Errorf("subject_token = %q", vals.Get("subject_token"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "sk-new", "expires_in": 1})
		}))
		defer srv.Close()
		rewriteDefaultClientHost(t, srv.URL, "auth.openai.com")
		key, err := OpenAIExchangeAPIKey(context.Background(), "id-tok")
		if err != nil || key != "sk-new" {
			t.Fatalf("key=%q err=%v", key, err)
		}
	})
	t.Run("empty key", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "", "expires_in": 1})
		}))
		defer srv.Close()
		rewriteDefaultClientHost(t, srv.URL, "auth.openai.com")
		_, err := OpenAIExchangeAPIKey(context.Background(), "id")
		if err == nil || !strings.Contains(err.Error(), "empty key") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("endpoint error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_token", "error_description": "bad id"})
		}))
		defer srv.Close()
		rewriteDefaultClientHost(t, srv.URL, "auth.openai.com")
		_, err := OpenAIExchangeAPIKey(context.Background(), "id")
		if err == nil || !strings.Contains(err.Error(), "API key exchange") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDescribe(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XAI_API_KEY", "")
	if got := Describe("xai", st); got != "none" {
		t.Errorf("Describe empty = %q", got)
	}
	_ = st.Set("xai", Credential{Type: TypeAPIKey, APIKey: "k"})
	if got := Describe("xai", st); got != "api key" {
		t.Errorf("Describe api = %q", got)
	}
	_ = st.Set("xai", Credential{Type: TypeOAuth, Access: "a", APIKey: "k"})
	if got := Describe("xai", st); got != "oauth+key" {
		t.Errorf("Describe oauth+key = %q", got)
	}
	_ = st.Set("xai", Credential{Type: TypeOAuth, Access: "a"})
	if got := Describe("xai", st); got != "oauth" {
		t.Errorf("Describe oauth = %q", got)
	}
	t.Setenv("XAI_API_KEY", "env")
	if got := Describe("xai", st); got != "XAI_API_KEY" {
		t.Errorf("Describe env = %q", got)
	}
}

func TestFlowConfigShapes(t *testing.T) {
	o := OpenAIFlow()
	if o.ClientID == "" || o.RedirectPort != openaiRedirectPort || !strings.Contains(o.AuthorizeURL, "openai.com") {
		t.Errorf("OpenAIFlow = %+v", o)
	}
	x := XAIFlow()
	if x.ClientID == "" || x.RedirectPort != xaiRedirectPort || !x.IncludeNonce {
		t.Errorf("XAIFlow = %+v", x)
	}
	if x.ExtraParams["plan"] != "generic" {
		t.Errorf("ExtraParams = %v", x.ExtraParams)
	}
}
