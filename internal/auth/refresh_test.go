package auth

import (
	"context"
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

func TestFlowRefresh(t *testing.T) {
	t.Run("rotates refresh token", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			vals, _ := url.ParseQuery(string(body))
			if vals.Get("grant_type") != "refresh_token" {
				t.Errorf("grant_type = %q", vals.Get("grant_type"))
			}
			if vals.Get("refresh_token") != "old-refresh" || vals.Get("client_id") != "cid" {
				t.Errorf("form = %v", vals)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "new-access",
				"refresh_token": "new-refresh",
				"id_token":      "new-id",
				"expires_in":    1800,
			})
		}))
		defer srv.Close()

		tok, err := FlowConfig{TokenURL: srv.URL, ClientID: "cid"}.Refresh(context.Background(), "old-refresh")
		if err != nil {
			t.Fatal(err)
		}
		if tok.Access != "new-access" || tok.Refresh != "new-refresh" || tok.IDToken != "new-id" {
			t.Fatalf("tokens = %+v", tok)
		}
		if time.Until(tok.ExpiresAt) < 20*time.Minute {
			t.Errorf("ExpiresAt too soon: %v", tok.ExpiresAt)
		}
	})

	t.Run("carries forward refresh when omitted", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "only-access",
				"expires_in":   60,
			})
		}))
		defer srv.Close()
		tok, err := FlowConfig{TokenURL: srv.URL, ClientID: "c"}.Refresh(context.Background(), "kept-refresh")
		if err != nil {
			t.Fatal(err)
		}
		if tok.Access != "only-access" || tok.Refresh != "kept-refresh" {
			t.Fatalf("tokens = %+v", tok)
		}
	})

	t.Run("error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":             "invalid_grant",
				"error_description": "refresh revoked",
			})
		}))
		defer srv.Close()
		_, err := FlowConfig{TokenURL: srv.URL, ClientID: "c"}.Refresh(context.Background(), "bad")
		if err == nil || !strings.Contains(err.Error(), "token refresh") {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(err.Error(), "refresh revoked") {
			t.Errorf("err = %v, want description", err)
		}
	})
}

func TestBearerSourceRefreshesNearExpiry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		vals, _ := url.ParseQuery(string(body))
		if vals.Get("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q", vals.Get("grant_type"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "refreshed-access",
			"refresh_token": "refreshed-refresh",
			"expires_in":    3600,
		})
	}))
	defer srv.Close()
	rewriteDefaultClientHost(t, srv.URL, "auth.x.ai")

	st, err := OpenStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XAI_API_KEY", "")
	// Within refreshSkew (2m) of expiry → triggers Refresh.
	if err := st.Set("xai", Credential{
		Type:      TypeOAuth,
		Access:    "stale-access",
		Refresh:   "stale-refresh",
		ExpiresAt: time.Now().Add(30 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := BearerSource("xai", st)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "refreshed-access" {
		t.Errorf("bearer = %q", got)
	}
	cred, ok := st.Get("xai")
	if !ok || cred.Access != "refreshed-access" || cred.Refresh != "refreshed-refresh" {
		t.Fatalf("persisted cred = %+v", cred)
	}
}

func TestBearerSourceRefreshFailure(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "gone"})
	}))
	defer srv.Close()
	rewriteDefaultClientHost(t, srv.URL, "auth.x.ai")

	st, err := OpenStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XAI_API_KEY", "")
	_ = st.Set("xai", Credential{
		Type: TypeOAuth, Access: "a", Refresh: "r", ExpiresAt: time.Now().Add(time.Second),
	})
	_, err = BearerSource("xai", st)(context.Background())
	// BearerSource maps refresh failures to the generic "no credentials" hint.
	if err == nil || !strings.Contains(err.Error(), "no credentials for xai") {
		t.Fatalf("err = %v", err)
	}
	if hits < 1 {
		t.Error("expected refresh HTTP attempt")
	}
}

func TestChatGPTSource(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	idTok := fakeJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "from-auth-claim"},
	})
	_ = st.Set("openai", Credential{
		Type: TypeOAuth, Access: "oa-access", Refresh: "r",
		IDToken: idTok, ExpiresAt: time.Now().Add(time.Hour),
	})
	access, acct, err := ChatGPTSource(st)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if access != "oa-access" || acct != "from-auth-claim" {
		t.Fatalf("access=%q acct=%q", access, acct)
	}

	_ = st.Set("openai", Credential{
		Type: TypeOAuth, Access: "oa", Refresh: "r", ExpiresAt: time.Now().Add(time.Hour),
	})
	_, _, err = ChatGPTSource(st)(context.Background())
	if err == nil || !strings.Contains(err.Error(), "account id") {
		t.Fatalf("err = %v", err)
	}
}

func TestAccountIDFromToken(t *testing.T) {
	cases := []struct {
		name string
		tok  string
		want string
	}{
		{"empty", "", ""},
		{"opaque", "not-a-jwt", ""},
		{"top-level", fakeJWT(map[string]any{"chatgpt_account_id": "A"}), "A"},
		{"nested auth", fakeJWT(map[string]any{
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "B"},
		}), "B"},
		{"orgs", fakeJWT(map[string]any{
			"organizations": []map[string]any{{"id": "org-1"}},
		}), "org-1"},
	}
	for _, tc := range cases {
		if got := AccountIDFromToken(tc.tok); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
