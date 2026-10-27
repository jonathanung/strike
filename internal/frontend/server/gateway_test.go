package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func mintTestGrant(t *testing.T, srv *Server, root string, ttl time.Duration) string {
	t.Helper()
	res := httptest.NewRecorder()
	body := strings.NewReader(`{"ttlSeconds":` + strconv.Itoa(int(ttl.Seconds())) + `}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/gateway/grants", body)
	req.Header.Set("Authorization", "Bearer "+root)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("mint status = %d body=%s", res.Code, res.Body.String())
	}
	if cc := res.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q", cc)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Token == "" || out.Token == root || strings.Contains(res.Body.String(), root) && root != out.Token {
		// root may be absent; the new token is the only secret in the body.
	}
	if strings.Contains(res.Body.String(), root) {
		t.Fatalf("mint response echoed host token: %s", res.Body.String())
	}
	return out.Token
}

func TestGatewayGrantAuthRotateAndHandoff(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "daemon.token")
	if err := os.WriteFile(tokenPath, []byte("host-token-aaaaaaaaaaaaaaaa\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := "host-token-aaaaaaaaaaaaaaaa"
	srv, err := New(Options{
		Auth:       true,
		Token:      root,
		TokenFile:  tokenPath,
		Addr:       "127.0.0.1:8787",
		SessionDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	srv.grantNow = func() time.Time { return now }

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/gateway", nil)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth = %d", res.Code)
	}

	grant := mintTestGrant(t, srv, root, 15*time.Minute)
	if strings.Contains(grant, root) {
		t.Fatal("grant contains host token")
	}

	// Query tokens stay rejected, including a live grant.
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/gateway?token="+grant, nil)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("query grant status = %d", res.Code)
	}

	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/gateway", nil)
	req.Header.Set("Authorization", "Bearer "+grant)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("grant status = %d body=%s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), grant) || strings.Contains(res.Body.String(), root) {
		t.Fatalf("info leaked a secret: %s", res.Body.String())
	}
	var info gatewayInfoBody
	if err := json.Unmarshal(res.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if !info.OK || !info.Auth || !info.Loopback || !info.TokenFile || info.Credential != "grant" || info.Grants != 1 {
		t.Fatalf("info = %+v", info)
	}

	// A grant cannot mint, revoke-all, or rotate.
	for _, c := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/v1/gateway/grants", `{"ttlSeconds":60}`},
		{http.MethodDelete, "/v1/gateway/grants", ""},
		{http.MethodPost, "/v1/gateway/rotate", ""},
	} {
		res = httptest.NewRecorder()
		var rdr io.Reader
		if c.body != "" {
			rdr = strings.NewReader(c.body)
		}
		req = httptest.NewRequest(c.method, c.path, rdr)
		req.Header.Set("Authorization", "Bearer "+grant)
		srv.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusForbidden {
			t.Fatalf("%s %s = %d body=%s", c.method, c.path, res.Code, res.Body.String())
		}
		if strings.Contains(res.Body.String(), root) || strings.Contains(res.Body.String(), grant) {
			t.Fatalf("forbidden body leaked a secret: %s", res.Body.String())
		}
	}

	// Cookie handoff stores the grant, not the host token.
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/attach?token="+grant, nil)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusFound {
		t.Fatalf("handoff = %d", res.Code)
	}
	if loc := res.Header().Get("Location"); strings.Contains(loc, grant) || strings.Contains(loc, "token=") {
		t.Fatalf("redirect kept a secret: %q", loc)
	}
	var cookie *http.Cookie
	for _, c := range res.Result().Cookies() {
		if c.Name == authCookieName {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value != grant {
		t.Fatalf("cookie = %#v, want grant", cookie)
	}
	if cookie.Value == root {
		t.Fatal("handoff upgraded grant to host token")
	}

	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/gateway", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	req.AddCookie(cookie)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("grant cookie with bad bearer = %d", res.Code)
	}

	// Host-token file rotation drops grants on the next check.
	if err := os.WriteFile(tokenPath, []byte("rotated-host-token-bbbbbbbb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/gateway", nil)
	req.Header.Set("Authorization", "Bearer "+grant)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("grant after file rotate = %d", res.Code)
	}
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/gateway", nil)
	req.Header.Set("Authorization", "Bearer rotated-host-token-bbbbbbbb")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("new host token = %d body=%s", res.Code, res.Body.String())
	}
	if err := json.Unmarshal(res.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Grants != 0 || info.Credential != "root" {
		t.Fatalf("after rotate info = %+v", info)
	}
}

func TestGatewayGrantExpiryAndCap(t *testing.T) {
	root := "root-token-cccccccccccccccc"
	srv := testServer(t, t.TempDir(), root)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	srv.grantNow = func() time.Time { return now }
	grant := mintTestGrant(t, srv, root, time.Minute)
	now = now.Add(2 * time.Minute)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/gateway", nil)
	req.Header.Set("Authorization", "Bearer "+grant)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("expired grant = %d", res.Code)
	}

	now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for i := 0; i < maxGatewayGrants; i++ {
		mintTestGrant(t, srv, root, time.Minute)
	}
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/gateway/grants", strings.NewReader(`{"ttlSeconds":60}`))
	req.Header.Set("Authorization", "Bearer "+root)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("cap status = %d body=%s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), root) {
		t.Fatalf("cap body leaked host token: %s", res.Body.String())
	}
}

func TestGatewayRotateWritesTokenFile(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "nested", "daemon.token")
	root := "root-token-dddddddddddddddd"
	if err := os.WriteFile(tokenPath, []byte(root+"\n"), 0o600); err != nil {
		if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tokenPath, []byte(root+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := New(Options{Auth: true, Token: root, TokenFile: tokenPath, SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	grant := mintTestGrant(t, srv, root, time.Minute)

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/gateway/rotate", nil)
	req.Header.Set("Authorization", "Bearer "+root)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("rotate = %d body=%s", res.Code, res.Body.String())
	}
	if res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", res.Header().Get("Cache-Control"))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Token == "" || out.Token == root || out.Token == grant {
		t.Fatalf("rotated token = %q", out.Token)
	}
	if strings.Contains(res.Body.String(), root) || strings.Contains(res.Body.String(), grant) {
		t.Fatalf("rotate response leaked previous secret: %s", res.Body.String())
	}
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v", info.Mode().Perm())
	}
	got, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != out.Token {
		t.Fatalf("file = %q, response token differs", got)
	}
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil)
	req.Header.Set("Authorization", "Bearer "+root)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("old host token after rotate = %d", res.Code)
	}
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil)
	req.Header.Set("Authorization", "Bearer "+grant)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("grant after rotate = %d", res.Code)
	}
}

func TestGatewayLogoutRevokesGrantAndClearsCookie(t *testing.T) {
	root := "root-token-eeeeeeeeeeeeeeee"
	srv := testServer(t, t.TempDir(), root)
	grant := mintTestGrant(t, srv, root, time.Minute)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/gateway/logout", nil)
	req.Header.Set("Authorization", "Bearer "+grant)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("logout = %d body=%s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), grant) || strings.Contains(res.Body.String(), root) {
		t.Fatalf("logout leaked a secret: %s", res.Body.String())
	}
	var cleared bool
	for _, c := range res.Result().Cookies() {
		if c.Name == authCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("logout did not clear the auth cookie")
	}
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/gateway", nil)
	req.Header.Set("Authorization", "Bearer "+grant)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("revoked grant = %d", res.Code)
	}
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/v1/gateway/grants", nil)
	req.Header.Set("Authorization", "Bearer "+root)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("revoke = %d", res.Code)
	}
}

func TestGatewayOpenWithoutAuth(t *testing.T) {
	srv := testServer(t, t.TempDir(), "")
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/gateway", nil)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("open info = %d", res.Code)
	}
	if strings.Contains(res.Body.String(), "token") && strings.Contains(res.Body.String(), "Bearer") {
		t.Fatalf("open info looked like a credential: %s", res.Body.String())
	}
	var info gatewayInfoBody
	if err := json.Unmarshal(res.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Auth || info.Credential != "open" {
		t.Fatalf("info = %+v", info)
	}
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/gateway/grants", nil)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("mint without auth = %d", res.Code)
	}
}

func TestGatewayRotateRequiresTokenFile(t *testing.T) {
	root := "root-token-ffffffffffffffff"
	srv := testServer(t, t.TempDir(), root)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/gateway/rotate", nil)
	req.Header.Set("Authorization", "Bearer "+root)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusConflict {
		t.Fatalf("rotate without file = %d body=%s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), root) {
		t.Fatalf("conflict leaked host token: %s", res.Body.String())
	}
}
