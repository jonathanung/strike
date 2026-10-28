package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/protocol"
)

func TestEventCursor(t *testing.T) {
	cases := []struct {
		header string
		query  string
		want   int64
	}{
		{"", "", 0},
		{"128", "", 128},
		{"", "64", 64},
		{"128", "64", 128},
		{"nope", "", 0},
		{"-1", "", 0},
		{" 40 ", "", 40},
	}
	for _, tc := range cases {
		target := "/v1/live/events"
		if tc.query != "" {
			target += "?after=" + tc.query
		}
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if tc.header != "" {
			req.Header.Set("Last-Event-ID", tc.header)
		}
		if got := eventCursor(req); got != tc.want {
			t.Errorf("header %q query %q = %d, want %d", tc.header, tc.query, got, tc.want)
		}
	}
}

func TestClampCursor(t *testing.T) {
	if got := clampCursor(10, 8); got != 0 {
		t.Fatalf("past end = %d, want 0", got)
	}
	if got := clampCursor(4, 8); got != 4 {
		t.Fatalf("inside = %d, want 4", got)
	}
	if got := clampCursor(-1, 8); got != 0 {
		t.Fatalf("negative = %d, want 0", got)
	}
}

func TestSSEResumeCursorSkipsSeenBytes(t *testing.T) {
	dir := t.TempDir()
	writeFixtureSession(t, dir, "s1",
		protocol.UserMessage{Text: "one"},
		protocol.TextDelta{Text: "two"},
	)
	srv := testServer(t, dir, "secret")
	path := session.LogPath(dir, "s1")

	full := httptest.NewRecorder()
	if _, err := srv.writeEventsFrom(context.Background(), full, full, path, 0); err != nil {
		t.Fatal(err)
	}
	body := full.Body.String()
	if !strings.Contains(body, "id: ") {
		t.Fatalf("SSE body missing id: %s", body)
	}
	if !strings.Contains(body, `"text":"one"`) || !strings.Contains(body, `"text":"two"`) {
		t.Fatalf("full replay missing events: %s", body)
	}
	firstID := firstSSEID(t, body)

	rest := httptest.NewRecorder()
	if _, err := srv.writeEventsFrom(context.Background(), rest, rest, path, firstID); err != nil {
		t.Fatal(err)
	}
	got := rest.Body.String()
	if strings.Contains(got, `"text":"one"`) {
		t.Fatalf("resume from %d replayed the first event: %s", firstID, got)
	}
	if !strings.Contains(got, `"text":"two"`) {
		t.Fatalf("resume from %d dropped the second event: %s", firstID, got)
	}
}

func TestSessionEventsLastEventIDAtEOFEmitsNothing(t *testing.T) {
	dir := t.TempDir()
	writeFixtureSession(t, dir, "s1", protocol.UserMessage{Text: "one"})
	srv := testServer(t, dir, "secret")
	path := session.LogPath(dir, "s1")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/sessions/s1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Last-Event-ID", strconv.FormatInt(info.Size(), 10))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if strings.Contains(string(body), `"text":"one"`) {
		t.Fatalf("EOF cursor replayed history: %s", body)
	}
}

func TestMagicDNSOriginMatchesHost(t *testing.T) {
	srv := testServer(t, t.TempDir(), "secret")
	post := func(origin, host string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/roots", strings.NewReader(`{}`))
		req.Host = host
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer secret")
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, req)
		return res.Code
	}
	// Same-host MagicDNS is the phone page behind tailscale serve.
	// The route may still fail for a missing engine; it must not be the
	// origin check.
	if code := post("https://desktop.tailnet.ts.net", "desktop.tailnet.ts.net"); code == http.StatusForbidden {
		t.Fatalf("same-host MagicDNS POST = %d, want not 403", code)
	}
	if code := post("https://desktop.tailnet.ts.net:8443", "desktop.tailnet.ts.net:8443"); code == http.StatusForbidden {
		t.Fatalf("same-host with port = %d, want not 403", code)
	}
	// A public page must not ride the cookie.
	if code := post("https://evil.example", "desktop.tailnet.ts.net"); code != http.StatusForbidden {
		t.Fatalf("cross-host POST = %d, want 403", code)
	}
	// Direct tailnet IP stays on the private allowlist.
	if code := post("http://100.64.0.5:8787", "100.64.0.5:8787"); code == http.StatusForbidden {
		t.Fatalf("tailnet IP POST = %d, want not 403", code)
	}
}

func TestCORSReflectsSameHostMagicDNS(t *testing.T) {
	srv := testServer(t, t.TempDir(), "secret")
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Host = "desktop.tailnet.ts.net"
	req.Header.Set("Origin", "https://desktop.tailnet.ts.net")
	srv.Handler().ServeHTTP(res, req)
	if got := res.Header().Get("Access-Control-Allow-Origin"); got != "https://desktop.tailnet.ts.net" {
		t.Fatalf("Allow-Origin = %q", got)
	}

	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Host = "desktop.tailnet.ts.net"
	req.Header.Set("Origin", "https://evil.example")
	srv.Handler().ServeHTTP(res, req)
	if got := res.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("public origin reflected: %q", got)
	}
}

func TestAttachCookieSecureOnlyBehindLoopbackHTTPS(t *testing.T) {
	srv := testServer(t, t.TempDir(), "secret")
	cookie := func(remote, proto string) *http.Cookie {
		t.Helper()
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/?token=secret", nil)
		req.RemoteAddr = remote
		if proto != "" {
			req.Header.Set("X-Forwarded-Proto", proto)
		}
		srv.Handler().ServeHTTP(res, req)
		for _, c := range res.Result().Cookies() {
			if c.Name == authCookieName {
				return c
			}
		}
		t.Fatal("missing auth cookie")
		return nil
	}
	if c := cookie("127.0.0.1:1234", "https"); !c.Secure {
		t.Fatal("loopback X-Forwarded-Proto=https should set Secure")
	}
	if c := cookie("100.64.0.8:1234", "https"); c.Secure {
		t.Fatal("X-Forwarded-Proto from a tailnet client must not set Secure")
	}
	if c := cookie("127.0.0.1:1234", ""); c.Secure {
		t.Fatal("plain loopback HTTP should not set Secure")
	}
	if c := cookie("192.0.2.1:1234", ""); c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("http cookie flags = Secure:%v HttpOnly:%v SameSite:%v", c.Secure, c.HttpOnly, c.SameSite)
	}
}

func TestWebSocketRejectsCrossHostMagicDNSOrigin(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("live", t.TempDir(), nil, ops)
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live})
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/ws", nil)
	req.Host = "desktop.tailnet.ts.net"
	req.Header.Set("Origin", "https://evil.example")
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.Code)
	}
}

func firstSSEID(t *testing.T, body string) int64 {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "id:") {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "id:")), 10, 64)
		if err != nil {
			t.Fatalf("id line %q: %v", line, err)
		}
		return n
	}
	t.Fatalf("no id line in %s", body)
	return 0
}
