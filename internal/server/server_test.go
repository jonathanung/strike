package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/session"
	"github.com/jonathanung/strike-cli/internal/version"
)

func TestNewAuthValidation(t *testing.T) {
	if _, err := New(Options{}); err != nil {
		t.Fatalf("New() without auth: %v", err)
	}
	if _, err := New(Options{Auth: true}); err == nil {
		t.Fatal("New() with auth and empty token: want error")
	}
	if _, err := New(Options{Token: "secret"}); err == nil {
		t.Fatal("New() with token and no auth: want error")
	}
}

func TestHealth(t *testing.T) {
	srv := testServer(t, t.TempDir(), "secret")
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	var body healthResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.OK {
		t.Fatalf("ok = false")
	}
	if body.Version != version.Version {
		t.Fatalf("version = %q, want %q", body.Version, version.Version)
	}
	ct := res.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestEventsRequiresAuth(t *testing.T) {
	dir := t.TempDir()
	writeFixtureSession(t, dir, "s1", protocol.UserMessage{Text: "hi"})
	srv := testServer(t, dir, "secret")

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions/s1/events", nil)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("no auth status = %d, want 401", res.Code)
	}

	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/sessions/s1/events?token=wrong", nil)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d, want 401", res.Code)
	}
}

func TestEventsSSEBacklogAndBearer(t *testing.T) {
	dir := t.TempDir()
	writeFixtureSession(t, dir, "s1",
		protocol.UserMessage{Text: "hello"},
		protocol.TextDelta{Text: "world"},
	)
	srv := testServer(t, dir, "secret")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use a real test server so the ResponseWriter implements http.Flusher.
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/sessions/s1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}

	events := readSSEData(t, res.Body, 2, 2*time.Second)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %v", len(events), events)
	}
	if events[0]["type"] != "user.message" {
		t.Fatalf("event0 type = %v", events[0]["type"])
	}
	if events[1]["type"] != "text.delta" {
		t.Fatalf("event1 type = %v", events[1]["type"])
	}
	cancel()
}

func TestEventsSSETailsNewAppends(t *testing.T) {
	dir := t.TempDir()
	id := "live1"
	store, err := session.Open(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(protocol.UserMessage{Text: "first"}); err != nil {
		t.Fatal(err)
	}

	srv, err := New(Options{
		Auth:         true,
		Token:        "secret",
		SessionDir:   dir,
		PollInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/sessions/"+id+"/events?token=secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	// First event from backlog.
	got := readSSEData(t, res.Body, 1, 2*time.Second)
	if got[0]["type"] != "user.message" {
		t.Fatalf("backlog = %v", got[0])
	}

	if err := store.Append(protocol.TextDelta{Text: "second"}); err != nil {
		t.Fatal(err)
	}
	_ = store.Sync()

	got2 := readSSEData(t, res.Body, 1, 2*time.Second)
	if got2[0]["type"] != "text.delta" {
		t.Fatalf("tail = %v", got2[0])
	}
	cancel()
	_ = store.Close()
}

func TestEventsNotFoundAndBadID(t *testing.T) {
	srv := testServer(t, t.TempDir(), "secret")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	res, err := http.Get(ts.URL + "/v1/sessions/missing/events?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", res.StatusCode)
	}

	res, err = http.Get(ts.URL + "/v1/sessions/../etc/events?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusNotFound {
		// Go mux may 404 before handler; either is fine vs 200.
		t.Fatalf("path traversal status = %d", res.StatusCode)
	}
}

func TestAttachPage(t *testing.T) {
	srv := testServer(t, t.TempDir(), "secret")
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/attach", nil)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	body := res.Body.String()
	if !strings.Contains(body, "Strike Workspace") || !strings.Contains(body, "/assets/") {
		t.Fatalf("attach page missing expected content")
	}
}

func TestAuthAcceptsBearerCookieAndQuery(t *testing.T) {
	srv := testServer(t, t.TempDir(), "secret")
	cases := []struct {
		name string
		mod  func(*http.Request)
		code int
	}{
		{
			name: "missing",
			mod:  func(*http.Request) {},
			code: http.StatusUnauthorized,
		},
		{
			name: "bearer",
			mod:  func(r *http.Request) { r.Header.Set("Authorization", "Bearer secret") },
			code: http.StatusOK,
		},
		{
			name: "bearer case-insensitive scheme",
			mod:  func(r *http.Request) { r.Header.Set("Authorization", "bearer secret") },
			code: http.StatusOK,
		},
		{
			name: "bad bearer",
			mod:  func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") },
			code: http.StatusUnauthorized,
		},
		{
			name: "query token",
			mod:  func(r *http.Request) { r.URL.RawQuery = "token=secret" },
			code: http.StatusOK,
		},
		{
			name: "bad query token",
			mod:  func(r *http.Request) { r.URL.RawQuery = "token=wrong" },
			code: http.StatusUnauthorized,
		},
		{
			name: "cookie",
			mod: func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: authCookieName, Value: "secret"})
			},
			code: http.StatusOK,
		},
		{
			name: "bad cookie",
			mod: func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: authCookieName, Value: "wrong"})
			},
			code: http.StatusUnauthorized,
		},
		{
			name: "bad bearer still allows valid cookie",
			mod: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer wrong")
				r.AddCookie(&http.Cookie{Name: authCookieName, Value: "secret"})
			},
			code: http.StatusOK,
		},
		{
			name: "bad bearer still allows valid query",
			mod: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer wrong")
				r.URL.RawQuery = "token=secret"
			},
			code: http.StatusOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil)
			tc.mod(req)
			srv.Handler().ServeHTTP(res, req)
			if res.Code != tc.code {
				t.Fatalf("status = %d, want %d body=%q", res.Code, tc.code, res.Body.String())
			}
		})
	}
}

func TestAttachTokenHandoffSetsCookieAndStripsQuery(t *testing.T) {
	srv := testServer(t, t.TempDir(), "secret")

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/attach?token=secret&tab=history", nil)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusFound {
		t.Fatalf("handoff status = %d, want %d", res.Code, http.StatusFound)
	}
	if loc := res.Header().Get("Location"); loc != "/attach?tab=history" {
		t.Fatalf("Location = %q, want /attach?tab=history", loc)
	}
	cookie := res.Result().Cookies()
	var got *http.Cookie
	for _, c := range cookie {
		if c.Name == authCookieName {
			got = c
			break
		}
	}
	if got == nil || got.Value != "secret" {
		t.Fatalf("Set-Cookie = %#v, want %s=secret", cookie, authCookieName)
	}
	if !got.HttpOnly || got.Path != "/" || got.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie flags = HttpOnly:%v Path:%q SameSite:%v", got.HttpOnly, got.Path, got.SameSite)
	}

	// Follow-up API call with only the handoff cookie succeeds.
	api := httptest.NewRecorder()
	apiReq := httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil)
	apiReq.AddCookie(got)
	srv.Handler().ServeHTTP(api, apiReq)
	if api.Code != http.StatusOK {
		t.Fatalf("cookie auth status = %d, want 200 body=%q", api.Code, api.Body.String())
	}

	// Root path handoff.
	root := httptest.NewRecorder()
	srv.Handler().ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/?token=secret", nil))
	if root.Code != http.StatusFound {
		t.Fatalf("root handoff status = %d, want %d", root.Code, http.StatusFound)
	}
	if loc := root.Header().Get("Location"); loc != "/" {
		t.Fatalf("root Location = %q, want /", loc)
	}

	// Invalid token does not set cookie or redirect.
	bad := httptest.NewRecorder()
	srv.Handler().ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/attach?token=wrong", nil))
	if bad.Code != http.StatusOK {
		t.Fatalf("bad token status = %d, want 200 (serve page)", bad.Code)
	}
	for _, c := range bad.Result().Cookies() {
		if c.Name == authCookieName {
			t.Fatalf("invalid token must not set auth cookie, got %#v", c)
		}
	}
}

func TestEmbeddedAssetAndSecurityHeaders(t *testing.T) {
	srv := testServer(t, t.TempDir(), "")
	index := httptest.NewRecorder()
	srv.Handler().ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := index.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
		t.Fatalf("Content-Security-Policy = %q", got)
	}
	body := index.Body.String()
	start := strings.Index(body, "/assets/")
	if start < 0 {
		t.Fatalf("asset path missing from %q", body)
	}
	end := strings.Index(body[start:], "\"")
	if end < 0 {
		t.Fatalf("asset path unterminated in %q", body)
	}
	assetPath := body[start : start+end]
	asset := httptest.NewRecorder()
	srv.Handler().ServeHTTP(asset, httptest.NewRequest(http.MethodGet, assetPath, nil))
	if asset.Code != http.StatusOK || asset.Body.Len() == 0 {
		t.Fatalf("GET %s = %d, %d bytes", assetPath, asset.Code, asset.Body.Len())
	}
	if got := asset.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestUnauthenticatedLoopbackAPIRoutes(t *testing.T) {
	srv := testServer(t, t.TempDir(), "")
	for _, path := range []string{"/v1/bootstrap", "/v1/sessions"} {
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, res.Code)
		}
	}
}

func TestCORSLocalhostOnly(t *testing.T) {
	srv := testServer(t, t.TempDir(), "secret")

	cases := []struct {
		origin string
		allow  bool
	}{
		{"http://localhost:5173", true},
		{"http://127.0.0.1:3000", true},
		{"https://evil.example", false},
		{"http://192.168.1.1", false},
		{"", false},
	}
	for _, tc := range cases {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		srv.Handler().ServeHTTP(res, req)
		got := res.Header().Get("Access-Control-Allow-Origin")
		if tc.allow {
			if got != tc.origin {
				t.Errorf("origin %q: Allow-Origin = %q, want %q", tc.origin, got, tc.origin)
			}
		} else if got != "" {
			t.Errorf("origin %q: Allow-Origin = %q, want empty", tc.origin, got)
		}
	}
}

func TestCORSPreflightAllowsMutationMethods(t *testing.T) {
	srv := testServer(t, t.TempDir(), "secret")
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodOptions, "/v1/settings", nil)
		req.Header.Set("Origin", "http://localhost:5173")
		req.Header.Set("Access-Control-Request-Method", method)
		srv.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusNoContent {
			t.Errorf("%s preflight status = %d", method, res.Code)
		}
		if methods := res.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(methods, method) {
			t.Errorf("%s preflight methods = %q", method, methods)
		}
	}
}

func TestCORSExposePrivateOrigins(t *testing.T) {
	srv, err := New(Options{Auth: true, Token: "secret", SessionDir: t.TempDir(), Expose: true})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		origin string
		allow  bool
	}{
		{"http://192.168.1.20:5173", true},
		{"http://10.0.0.5:3000", true},
		{"http://localhost:5173", true},
		{"https://evil.example", false},
		{"http://8.8.8.8:80", false},
	}
	for _, tc := range cases {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Header.Set("Origin", tc.origin)
		srv.Handler().ServeHTTP(res, req)
		got := res.Header().Get("Access-Control-Allow-Origin")
		if tc.allow && got != tc.origin {
			t.Errorf("origin %q: Allow-Origin = %q, want %q", tc.origin, got, tc.origin)
		}
		if !tc.allow && got != "" {
			t.Errorf("origin %q: Allow-Origin = %q, want empty", tc.origin, got)
		}
	}
}

func TestWebSocketRejectsUntrustedOriginBeforeUpgrade(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("live", t.TempDir(), nil, ops)
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/ws", nil)
	req.Header.Set("Origin", "https://evil.example")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.Code)
	}
}

func TestAllowCIDRMiddleware(t *testing.T) {
	nets, err := ParseCIDRs([]string{"127.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{
		Auth:       true,
		Token:      "secret",
		SessionDir: t.TempDir(),
		AllowCIDRs: nets,
	})
	if err != nil {
		t.Fatal(err)
	}
	// httptest uses 192.0.2.1 by default — should be forbidden.
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("default remote status = %d, want 403", res.Code)
	}

	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("loopback status = %d, want 200", res.Code)
	}
}

func TestIsLocalhostBind(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8787": true,
		"localhost:9":    true,
		"[::1]:8787":     true,
		"0.0.0.0:8787":   false,
		"192.168.0.1:80": false,
		":8787":          false,
	}
	for addr, want := range cases {
		if got := IsLocalhostBind(addr); got != want {
			t.Errorf("IsLocalhostBind(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestMintToken(t *testing.T) {
	a, err := MintToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := MintToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) < 16 || a == b {
		t.Fatalf("tokens weak or equal: %q %q", a, b)
	}
}

func testServer(t *testing.T, sessionDir, token string) *Server {
	t.Helper()
	srv, err := New(Options{
		Auth:         token != "",
		Token:        token,
		SessionDir:   sessionDir,
		PollInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func writeFixtureSession(t *testing.T, dir, id string, events ...protocol.Event) {
	t.Helper()
	store, err := session.Open(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if err := store.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, id+".jsonl")); err != nil {
		t.Fatal(err)
	}
}

func readSSEData(t *testing.T, r io.Reader, n int, timeout time.Duration) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var out []map[string]any
	for len(out) < n {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %d SSE events; got %d", n, len(out))
		}
		// Set a short read deadline via context is hard on body; rely on test server.
		if !sc.Scan() {
			if err := sc.Err(); err != nil && err != io.EOF {
				t.Fatalf("scan: %v", err)
			}
			// If scanner ends early, fail.
			if len(out) < n {
				time.Sleep(10 * time.Millisecond)
				// can't restart scanner easily; fail
				t.Fatalf("stream ended after %d events, want %d", len(out), n)
			}
			break
		}
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var env map[string]any
		if err := json.Unmarshal([]byte(payload), &env); err != nil {
			t.Fatalf("json: %v (%q)", err, payload)
		}
		out = append(out, env)
		// consume blank line after data if present
	}
	return out
}
