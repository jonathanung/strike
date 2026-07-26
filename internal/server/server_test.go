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

func TestNewRequiresToken(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New() with empty token: want error")
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
	if !strings.Contains(body, "strike") || !strings.Contains(body, "EventSource") {
		t.Fatalf("attach page missing expected content")
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
