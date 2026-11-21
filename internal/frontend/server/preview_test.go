package server

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// loopbackApp starts a fixture app on 127.0.0.1 and returns its port.
func loopbackApp(t *testing.T, h http.Handler) int {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	_, p, err := net.SplitHostPort(strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(p)
	return port
}

func newPreviewServer(t *testing.T) *Server {
	t.Helper()
	srv, err := New(Options{SessionDir: t.TempDir(), Addr: "127.0.0.1:8787"})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func previewDo(t *testing.T, srv *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func selectPreview(t *testing.T, srv *Server, port int) previewTarget {
	t.Helper()
	rec := previewDo(t, srv, httptest.NewRequest(http.MethodPut, "/v1/preview", strings.NewReader(`{"port":`+strconv.Itoa(port)+`}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("select %d = %d %s", port, rec.Code, rec.Body.String())
	}
	var got previewTarget
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPreviewProxiesOnlyTheSelectedLoopbackPort(t *testing.T) {
	// The upstream handler runs on another goroutine; the race detector
	// does not see the socket as a sync point, so guard what it records.
	var mu sync.Mutex
	var seenAuth, seenCookie, seenHost, seenPath string
	app := loopbackApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenAuth, seenCookie, seenHost, seenPath = r.Header.Get("Authorization"), r.Header.Get("Cookie"), r.Host, r.URL.RequestURI()
		mu.Unlock()
		w.Header().Set("Set-Cookie", "app=1")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<h1>fixture app</h1>"))
	}))
	other := loopbackApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("other app"))
	}))
	srv := newPreviewServer(t)

	// Nothing is proxied before a selection.
	if rec := previewDo(t, srv, httptest.NewRequest(http.MethodGet, "/preview/anything/", nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("unselected proxy = %d", rec.Code)
	}

	got := selectPreview(t, srv, app)
	if !got.Selected || got.Port != app || got.Origin != "http://127.0.0.1:"+strconv.Itoa(app) {
		t.Fatalf("target = %+v", got)
	}
	if got.Probe == nil || !got.Probe.Reachable || got.Probe.Status != http.StatusOK || got.Probe.Framing != "ok" {
		t.Fatalf("probe = %+v", got.Probe)
	}
	if !strings.HasPrefix(got.Path, previewPathPrefix) || len(got.Path) < len(previewPathPrefix)+32 {
		t.Fatalf("path = %q, want an unguessable key", got.Path)
	}

	req := httptest.NewRequest(http.MethodGet, got.Path+"page?q=1", nil)
	req.Header.Set("Authorization", "Bearer gateway-secret")
	req.Header.Set("Cookie", authCookieName+"=gateway-secret")
	rec := previewDo(t, srv, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "fixture app") {
		t.Fatalf("proxy = %d %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	host, path, auth, cookie := seenHost, seenPath, seenAuth, seenCookie
	mu.Unlock()
	// The label matches the dialed destination.
	if host != "127.0.0.1:"+strconv.Itoa(app) || path != "/page?q=1" {
		t.Fatalf("upstream saw host %q path %q", host, path)
	}
	if auth != "" || cookie != "" {
		t.Fatalf("gateway credentials forwarded: auth=%q cookie=%q", auth, cookie)
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Fatal("app Set-Cookie reached the gateway origin")
	}
	csp := strings.Join(rec.Header().Values("Content-Security-Policy"), " | ")
	if !strings.Contains(csp, "sandbox") || strings.Contains(csp, "allow-same-origin") || strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("proxied CSP = %q", csp)
	}
	if xfo := rec.Header().Get("X-Frame-Options"); xfo != "" {
		t.Fatalf("gateway X-Frame-Options leaked onto the preview: %q", xfo)
	}

	// A wrong key, or the key of another port, is not proxied.
	key := strings.TrimSuffix(strings.TrimPrefix(got.Path, previewPathPrefix), "/")
	for _, path := range []string{"/preview/" + strings.Repeat("0", len(key)) + "/", "/preview/" + key + "x/"} {
		if rec := previewDo(t, srv, httptest.NewRequest(http.MethodGet, path, nil)); rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d", path, rec.Code)
		}
	}
	otherTarget := selectPreview(t, srv, other)
	if otherTarget.Path == got.Path {
		t.Fatal("two ports share a preview path")
	}
	if rec := previewDo(t, srv, httptest.NewRequest(http.MethodGet, got.Path, nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("old port still proxied after switching: %d", rec.Code)
	}
	// Reselecting keeps the same path for the life of the gateway.
	if again := selectPreview(t, srv, app); again.Path != got.Path {
		t.Fatalf("reselect path %q, want %q", again.Path, got.Path)
	}

	// After DELETE nothing is proxied.
	if rec := previewDo(t, srv, httptest.NewRequest(http.MethodDelete, "/v1/preview", nil)); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	if rec := previewDo(t, srv, httptest.NewRequest(http.MethodGet, got.Path, nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("proxy after delete = %d", rec.Code)
	}
	cur := previewDo(t, srv, httptest.NewRequest(http.MethodGet, "/v1/preview", nil))
	if !strings.Contains(cur.Body.String(), `"selected":false`) {
		t.Fatalf("current after delete = %s", cur.Body.String())
	}
}

func TestPreviewRejectsHostsAndBadPorts(t *testing.T) {
	srv := newPreviewServer(t)
	cases := []struct {
		name string
		body string
		want string
	}{
		{"zero", `{"port":0}`, "1-65535"},
		{"too big", `{"port":70000}`, "1-65535"},
		{"negative", `{"port":-1}`, "1-65535"},
		{"gateway itself", `{"port":8787}`, "cannot preview itself"},
		{"host field", `{"port":3000,"host":"10.0.0.5"}`, "unknown field"},
		{"url field", `{"url":"http://example.com"}`, "unknown field"},
		{"string port", `{"port":"3000"}`, "cannot unmarshal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := previewDo(t, srv, httptest.NewRequest(http.MethodPut, "/v1/preview", strings.NewReader(tc.body)))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("PUT %s = %d %s", tc.body, rec.Code, rec.Body.String())
			}
		})
	}
	cur := previewDo(t, srv, httptest.NewRequest(http.MethodGet, "/v1/preview", nil))
	if !strings.Contains(cur.Body.String(), `"selected":false`) {
		t.Fatalf("rejected selections changed state: %s", cur.Body.String())
	}
}

func TestPreviewReportsNoListenerAndFramingRefusal(t *testing.T) {
	srv := newPreviewServer(t)
	// A port that was listening and is now closed.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	got := selectPreview(t, srv, closed)
	if got.Probe == nil || got.Probe.Reachable || !strings.Contains(got.Probe.Error, "nothing answered") {
		t.Fatalf("closed-port probe = %+v", got.Probe)
	}
	rec := previewDo(t, srv, httptest.NewRequest(http.MethodGet, got.Path, nil))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "nothing answered on 127.0.0.1:"+strconv.Itoa(closed)) {
		t.Fatalf("proxy to closed port = %d %s", rec.Code, rec.Body.String())
	}

	deny := loopbackApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		_, _ = w.Write([]byte("no frames"))
	}))
	got = selectPreview(t, srv, deny)
	if got.Probe == nil || got.Probe.Framing != "refused" || !strings.Contains(got.Probe.FramingHeader, "DENY") {
		t.Fatalf("framing probe = %+v", got.Probe)
	}
}

func TestPreviewRewritesRedirectsIntoThePreviewPath(t *testing.T) {
	app := loopbackApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login?next=%2F", http.StatusFound)
	}))
	srv := newPreviewServer(t)
	got := selectPreview(t, srv, app)
	rec := previewDo(t, srv, httptest.NewRequest(http.MethodGet, got.Path, nil))
	want := strings.TrimSuffix(got.Path, "/") + "/login?next=%2F"
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("redirect = %d %q, want %q", rec.Code, rec.Header().Get("Location"), want)
	}
	// /preview/<key> without the slash lands inside the prefix.
	bare := previewDo(t, srv, httptest.NewRequest(http.MethodGet, strings.TrimSuffix(got.Path, "/"), nil))
	if bare.Code != http.StatusFound || bare.Header().Get("Location") != got.Path {
		t.Fatalf("bare = %d %q", bare.Code, bare.Header().Get("Location"))
	}
}

func TestRewritePreviewLocation(t *testing.T) {
	const prefix = "/preview/k"
	cases := []struct {
		in, want string
	}{
		{"/a/b?c=1", "/preview/k/a/b?c=1"},
		{"http://127.0.0.1:3000/x?y=2", "/preview/k/x?y=2"},
		{"http://localhost:3000/", "/preview/k/"},
		{"http://localhost:3000", "/preview/k/"},
		{"http://127.0.0.1:4000/x", "http://127.0.0.1:4000/x"},
		{"https://example.com/x", "https://example.com/x"},
		{"//evil.example/x", "//evil.example/x"},
		{"relative/path", "relative/path"},
	}
	for _, tc := range cases {
		if got := rewritePreviewLocation(tc.in, prefix, 3000); got != tc.want {
			t.Errorf("rewrite(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPreviewFraming(t *testing.T) {
	cases := []struct {
		name   string
		header http.Header
		want   string
	}{
		{"none", http.Header{}, "ok"},
		{"xfo deny", http.Header{"X-Frame-Options": {"DENY"}}, "refused"},
		{"xfo sameorigin", http.Header{"X-Frame-Options": {"SAMEORIGIN"}}, "ok"},
		{"csp none", http.Header{"Content-Security-Policy": {"default-src 'self'; frame-ancestors 'none'"}}, "refused"},
		{"csp self", http.Header{"Content-Security-Policy": {"frame-ancestors 'self'"}}, "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := previewFraming(tc.header); got != tc.want {
				t.Fatalf("framing = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseProcNetTCP(t *testing.T) {
	// Rows: 127.0.0.1:3000 LISTEN, 0.0.0.0:8080 LISTEN, 192.168.1.5:9000
	// LISTEN (LAN only, excluded), 127.0.0.1:5000 ESTABLISHED (excluded).
	v4 := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1 0 100 0 0 10 0
   1: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 2 1 0 100 0 0 10 0
   2: 0501A8C0:2328 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 3 1 0 100 0 0 10 0
   3: 0100007F:1388 0100007F:D431 01 00000000:00000000 00:00000000 00000000  1000        0 4 1 0 100 0 0 10 0
`
	if got := parseProcNetTCP([]byte(v4)); len(got) != 2 || got[0] != 3000 || got[1] != 8080 {
		t.Fatalf("v4 ports = %v", got)
	}
	// ::1:5173 LISTEN, :::4000 LISTEN, ::ffff:127.0.0.1:6006 LISTEN.
	v6 := `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:1435 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 5 1 0 100 0 0 10 0
   1: 00000000000000000000000000000000:0FA0 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 6 1 0 100 0 0 10 0
   2: 0000000000000000FFFF00000100007F:1776 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 7 1 0 100 0 0 10 0
`
	if got := parseProcNetTCP([]byte(v6)); len(got) != 3 || got[0] != 5173 || got[1] != 4000 || got[2] != 6006 {
		t.Fatalf("v6 ports = %v", got)
	}
	if got := parseProcNetTCP([]byte("header only\n")); len(got) != 0 {
		t.Fatalf("empty table = %v", got)
	}
}

func TestPreviewPortsListsAndHidesGateway(t *testing.T) {
	old := procNetFiles
	t.Cleanup(func() { procNetFiles = old })
	dir := t.TempDir()
	path := dir + "/tcp"
	table := "header\n   0: 0100007F:0BB8 00000000:0000 0A x\n   1: 0100007F:2253 00000000:0000 0A x\n"
	if err := os.WriteFile(path, []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	procNetFiles = []string{path}
	srv := newPreviewServer(t)
	rec := previewDo(t, srv, httptest.NewRequest(http.MethodGet, "/v1/preview/ports", nil))
	var got previewPortsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Detection != "proc" || got.Self != 8787 || len(got.Ports) != 1 || got.Ports[0].Port != 3000 {
		t.Fatalf("ports = %+v", got)
	}

	procNetFiles = []string{dir + "/missing"}
	rec = previewDo(t, srv, httptest.NewRequest(http.MethodGet, "/v1/preview/ports", nil))
	if !strings.Contains(rec.Body.String(), `"detection":"unsupported"`) || !strings.Contains(rec.Body.String(), `"ports":[]`) {
		t.Fatalf("unsupported = %s", rec.Body.String())
	}
}

func TestPreviewFormPostFromSandboxedFrameReachesApp(t *testing.T) {
	var mu sync.Mutex
	var got string
	app := loopbackApp(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		got = r.Method + " " + r.Form.Get("name")
		mu.Unlock()
		_, _ = w.Write([]byte("saved"))
	}))
	srv := newPreviewServer(t)
	target := selectPreview(t, srv, app)
	req := httptest.NewRequest(http.MethodPost, target.Path+"save", strings.NewReader("name=ada"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "null")
	rec := previewDo(t, srv, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "saved" {
		t.Fatalf("sandboxed form post = %d %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if got != "POST ada" {
		t.Fatalf("app saw %q", got)
	}

	// Gateway routes keep the origin check.
	gw := httptest.NewRequest(http.MethodPut, "/v1/preview", strings.NewReader(`{"port":3000}`))
	gw.Header.Set("Origin", "null")
	if rec := previewDo(t, srv, gw); rec.Code != http.StatusForbidden {
		t.Fatalf("null-origin gateway PUT = %d, want 403", rec.Code)
	}
}
