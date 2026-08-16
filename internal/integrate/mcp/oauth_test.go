package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOAuthDiscoveryRefreshRevoke(t *testing.T) {
	var refreshN atomic.Int32
	var revokeN atomic.Int32
	tokenFile := filepath.Join(t.TempDir(), "tok.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "oauth-authorization-server"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 "http://example",
				"authorization_endpoint": "http://example/auth",
				"token_endpoint":         "http://" + r.Host + "/token",
				"revocation_endpoint":    "http://" + r.Host + "/revoke",
			})
		case r.URL.Path == "/token":
			refreshN.Add(1)
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "refresh_token" {
				http.Error(w, "bad grant", 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-new",
				"refresh_token": "refresh-new",
				"expires_in":    3600,
			})
		case r.URL.Path == "/revoke":
			revokeN.Add(1)
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	// Seed token file with expired access + refresh.
	seed := oauthTokenFile{
		AccessToken:  "access-old",
		RefreshToken: "refresh-old",
		Expiry:       time.Now().Add(-time.Hour),
		TokenURL:     srv.URL + "/token",
		RevokeURL:    srv.URL + "/revoke",
		AuthorizeURL: srv.URL + "/auth",
	}
	raw, _ := json.Marshal(seed)
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := ServerConfig{
		Name: "oa",
		URL:  srv.URL + "/mcp",
		OAuth: &OAuthConfig{
			ClientID:     "cid",
			DiscoveryURL: srv.URL + "/.well-known/oauth-authorization-server",
			TokenFile:    tokenFile,
		},
	}
	s, err := newOAuthSession(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.accessToken() != "access-new" {
		t.Fatalf("access=%q after refresh on load", s.accessToken())
	}
	if refreshN.Load() < 1 {
		t.Fatal("expected refresh call")
	}

	// Authorize URL
	u, err := s.AuthorizeURL("http://127.0.0.1/cb", "st", "challenge")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u, "client_id=cid") || !strings.Contains(u, "code_challenge=challenge") {
		t.Fatalf("authorize url=%q", u)
	}

	if err := s.Revoke(context.Background()); err != nil {
		t.Fatal(err)
	}
	if revokeN.Load() < 1 {
		t.Fatal("expected revoke")
	}
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Fatal("token file should be removed")
	}
}

func TestOAuthLoginAuthorizationCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "a1",
			"refresh_token": "r1",
			"expires_in":    60,
		})
	}))
	t.Cleanup(srv.Close)
	tokenFile := filepath.Join(t.TempDir(), "t.json")
	s := &oauthSession{
		clientID:  "c",
		tokenURL:  srv.URL + "/token",
		tokenFile: tokenFile,
		hc:        srv.Client(),
	}
	if err := s.LoginAuthorizationCode(context.Background(), "code", "http://127.0.0.1/cb"); err != nil {
		t.Fatal(err)
	}
	if s.accessToken() != "a1" {
		t.Fatalf("access=%q", s.accessToken())
	}
	if _, err := os.Stat(tokenFile); err != nil {
		t.Fatal(err)
	}
}

func TestHTTP401TriggersRefresh(t *testing.T) {
	var hits atomic.Int32
	tokenFile := filepath.Join(t.TempDir(), "tok.json")
	_ = os.WriteFile(tokenFile, []byte(`{"access_token":"stale","refresh_token":"r","expiry":"2020-01-01T00:00:00Z"}`), 0o600)

	var tokenURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if r.URL.Path == "/token" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "fresh", "refresh_token": "r2", "expires_in": 3600,
			})
			return
		}
		// MCP endpoint
		auth := r.Header.Get("Authorization")
		if n == 1 || strings.Contains(auth, "stale") {
			// First MCP call with stale → 401
			if strings.Contains(auth, "stale") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		// Success path
		var env struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&env)
		if env.Method == "initialize" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": env.ID,
				"result": map[string]any{
					"protocolVersion": ProtocolVersion,
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]string{"name": "t", "version": "1"},
				},
			})
			return
		}
		if env.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if env.Method == "tools/list" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": env.ID,
				"result": map[string]any{"tools": []any{}},
			})
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)
	tokenURL = srv.URL + "/token"

	// Rewrite token file with this token URL
	_ = os.WriteFile(tokenFile, []byte(`{"access_token":"stale","refresh_token":"r","token_url":"`+tokenURL+`","expiry":"2020-01-01T00:00:00Z"}`), 0o600)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := startHTTP(ctx, ServerConfig{
		Name: "h",
		URL:  srv.URL + "/mcp",
		OAuth: &OAuthConfig{
			ClientID:  "c",
			TokenFile: tokenFile,
			TokenURL:  tokenURL,
		},
	})
	if err != nil {
		// initialize may refresh first due to expired token — still OK
		t.Fatalf("startHTTP: %v", err)
	}
	defer client.Close()
	if tok := client.oauth.accessToken(); tok == "stale" {
		t.Fatal("expected refreshed token")
	}
}
