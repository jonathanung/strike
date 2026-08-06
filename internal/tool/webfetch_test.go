package tool

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const htmlFixture = `<!DOCTYPE html>
<html>
<head><title>Demo</title><style>body{color:red}</style></head>
<body>
<script>alert(1)</script>
<h1>Hello World</h1>
<p>A <a href="https://example.com/page">link</a> and <code>inline</code>.</p>
<ul><li>one</li><li>two</li></ul>
<pre>line1
line2</pre>
</body>
</html>`

func allowWebfetchTestHost(t *testing.T, host string) {
	t.Helper()
	prev := webfetchTestAllowHost
	webfetchTestAllowHost = host
	t.Cleanup(func() { webfetchTestAllowHost = prev })
}

// webfetchServer starts a TLS httptest server. Production webfetch upgrades
// http:// to https://, so plain httptest.NewServer cannot be used.
func webfetchServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	allowWebfetchTestHost(t, u.Hostname())

	prev := webfetchTestTransport
	webfetchTestTransport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test-only against httptest
	}
	t.Cleanup(func() { webfetchTestTransport = prev })
	return srv
}

func TestWebFetchFormats(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(htmlFixture))
	})
	tc := allowAll(t.TempDir())

	cases := []struct {
		format string
		want   []string
		not    []string
	}{
		{
			format: "html",
			want:   []string{"<h1>Hello World</h1>", "<script>"},
		},
		{
			format: "text",
			want:   []string{"Hello World", "link", "one"},
			not:    []string{"<h1>", "<script>", "color:red"},
		},
		{
			format: "markdown",
			want:   []string{"# Hello World", "[link](https://example.com/page)", "`inline`", "- one"},
			not:    []string{"<script>", "<style>"},
		},
	}
	for _, tt := range cases {
		t.Run(tt.format, func(t *testing.T) {
			res, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
				"url":    srv.URL,
				"format": tt.format,
			}), tc)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(res.Output, w) {
					t.Errorf("format %s missing %q\noutput:\n%s", tt.format, w, res.Output)
				}
			}
			for _, n := range tt.not {
				if strings.Contains(res.Output, n) {
					t.Errorf("format %s unexpectedly contains %q\noutput:\n%s", tt.format, n, res.Output)
				}
			}
			if !strings.Contains(string(res.Metadata), `"format":"`+tt.format+`"`) {
				t.Errorf("metadata = %s", res.Metadata)
			}
		})
	}
}

func TestWebFetchDefaultFormatMarkdown(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<h2>Title</h2><p>Body</p>`))
	})
	res, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
		"url": srv.URL,
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "## Title") {
		t.Errorf("expected markdown heading, got %q", res.Output)
	}
	if !strings.Contains(string(res.Metadata), `"format":"markdown"`) {
		t.Errorf("metadata = %s", res.Metadata)
	}
}

func TestWebFetchRejectsNonHTTPSchemes(t *testing.T) {
	tc := allowAll(t.TempDir())
	cases := []string{
		"file:///etc/passwd",
		"ftp://example.com/a",
		"javascript:alert(1)",
		"data:text/plain,hi",
	}
	for _, raw := range cases {
		_, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
			"url": raw,
		}), tc)
		if err == nil {
			t.Errorf("%s: expected error", raw)
			continue
		}
		if !strings.Contains(err.Error(), "http") && !strings.Contains(err.Error(), "url") {
			t.Errorf("%s: unexpected error %v", raw, err)
		}
	}
}

func TestWebFetchRejectsPrivateAndLoopback(t *testing.T) {
	// Ensure the test allowlist is off so real SSRF checks run.
	prev := webfetchTestAllowHost
	webfetchTestAllowHost = ""
	t.Cleanup(func() { webfetchTestAllowHost = prev })

	tc := allowAll(t.TempDir())
	cases := []string{
		"https://127.0.0.1/",
		"https://10.0.0.5/path",
		"https://169.254.169.254/latest/meta-data/",
		"http://192.168.1.1/",
		// CGNAT / shared address space (RFC 6598) — cloud metadata-ish (e.g. Alibaba 100.100.100.200)
		"https://100.64.1.1/",
		"https://100.100.100.200/",
		// Benchmarking range (RFC 2544) 198.18.0.0/15
		"https://198.18.0.1/",
	}
	for _, raw := range cases {
		_, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
			"url": raw,
		}), tc)
		if err == nil {
			t.Errorf("%s: expected private/loopback rejection", raw)
			continue
		}
		if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "local") {
			t.Errorf("%s: got %v, want private/local refusal", raw, err)
		}
	}
}

// TestIsBlockedIP pins SSRF address classification used by assertPublicHTTPHost
// (literal hosts and DNS results) and by any dial-time pinning that reuses the
// same helper. Dial pinning itself is not unit-tested here (needs a custom
// Dialer/net.Conn harness); CGNAT coverage is asserted via isBlockedIP.
func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		// loopback / unspecified
		{"127.0.0.1", true},
		{"::1", true},
		{"0.0.0.0", true},
		// RFC 1918
		{"10.0.0.5", true},
		{"192.168.1.1", true},
		{"172.16.0.1", true},
		// link-local / metadata
		{"169.254.169.254", true},
		// CGNAT shared address space (RFC 6598) 100.64.0.0/10
		{"100.64.0.1", true},
		{"100.64.1.1", true},
		{"100.100.100.200", true},
		{"100.127.255.254", true},
		// just outside CGNAT
		{"100.63.255.255", false},
		{"100.128.0.1", false},
		// benchmarking (RFC 2544) 198.18.0.0/15
		{"198.18.0.1", true},
		{"198.19.255.255", true},
		// public
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"2001:4860:4860::8888", false},
	}
	for _, tt := range cases {
		ip := net.ParseIP(tt.ip)
		if ip == nil {
			t.Fatalf("ParseIP(%q) = nil", tt.ip)
		}
		got := isBlockedIP(ip)
		if got != tt.blocked {
			t.Errorf("isBlockedIP(%s) = %v, want %v", tt.ip, got, tt.blocked)
		}
	}
}

func TestWebFetchRedirectToLoopbackDenied(t *testing.T) {
	// Public-looking first hop (allowlisted loopback httptest) redirects to a
	// private IP that must still be blocked by CheckRedirect.
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://10.0.0.1/secret", http.StatusFound)
	})
	_, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
		"url": srv.URL,
	}), allowAll(t.TempDir()))
	if err == nil {
		t.Fatal("expected redirect to private IP to be denied")
	}
	if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "local") && !strings.Contains(err.Error(), "redirect") {
		t.Errorf("got %v", err)
	}
}

func TestWebFetchBodyOversize(t *testing.T) {
	// Content-Length over the limit is rejected before reading.
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", webfetchMaxBody+1))
		w.WriteHeader(http.StatusOK)
		// Body may not be fully written; client should error on CL check.
	})
	_, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
		"url":    srv.URL,
		"format": "text",
	}), allowAll(t.TempDir()))
	if err == nil {
		t.Fatal("expected oversize error")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("got %v", err)
	}
}

func TestWebFetchBodyOversizeWithoutContentLength(t *testing.T) {
	// Chunked / no CL: read past limit then error.
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		chunk := bytesRepeat('x', 1<<20)
		for i := 0; i < 6; i++ {
			_, _ = w.Write(chunk)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	})
	_, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
		"url":    srv.URL,
		"format": "text",
	}), allowAll(t.TempDir()))
	if err == nil {
		t.Fatal("expected oversize error")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("got %v", err)
	}
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestWebFetchTimeout(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			_, _ = w.Write([]byte("late"))
		}
	})
	timeout := 0.2
	start := time.Now()
	_, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
		"url":     srv.URL,
		"timeout": timeout,
	}), allowAll(t.TempDir()))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "timeout") {
		t.Errorf("got %v, want deadline/timeout", err)
	}
	if elapsed > 1*time.Second {
		t.Errorf("took %v, want quick timeout failure (<1s)", elapsed)
	}
}

func TestWebFetchOutputTruncation(t *testing.T) {
	// Body under webfetchMaxBody but more than webfetchMaxOutputRunes after conversion.
	// Use plain text so output ≈ body.
	n := webfetchMaxOutputRunes + 5000
	body := strings.Repeat("a", n)
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	})
	res, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
		"url":    srv.URL,
		"format": "text",
	}), allowAll(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "output truncated") {
		t.Errorf("expected truncation marker, len=%d out=%q", len(res.Output), res.Output[len(res.Output)-80:])
	}
	// Truncated output should be much smaller than the full body.
	if len(res.Output) >= n {
		t.Errorf("output len %d not truncated (body %d)", len(res.Output), n)
	}
}

func TestWebFetchPermissionDenied(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, AskRequest) error { return errors.New("denied") },
	}
	_, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
		"url": srv.URL,
	}), tc)
	if err == nil {
		t.Fatal("expected permission deny")
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("got %v", err)
	}
}

func TestWebFetchNon2xx(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	})
	_, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
		"url": srv.URL,
	}), allowAll(t.TempDir()))
	if err == nil {
		t.Fatal("expected non-2xx error")
	}
	if !strings.Contains(err.Error(), "404") && !strings.Contains(err.Error(), "status") {
		t.Errorf("got %v", err)
	}
}

func TestWebFetchAskPatterns(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	})
	var saw AskRequest
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask: func(_ context.Context, req AskRequest) error {
			saw = req
			return nil
		},
	}
	_, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
		"url":    srv.URL,
		"format": "text",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if saw.Permission != "webfetch" {
		t.Errorf("permission = %q", saw.Permission)
	}
	if len(saw.Patterns) != 1 || !strings.Contains(saw.Patterns[0], "127.0.0.1") {
		t.Errorf("patterns = %#v", saw.Patterns)
	}
}

func TestWebFetchNetworkAllowlist(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	})
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	host := u.Hostname()
	allowAll := func(_ context.Context, _ AskRequest) error { return nil }

	t.Run("empty allow permits", func(t *testing.T) {
		tc := &Context{WorkDir: t.TempDir(), Ask: allowAll}
		res, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
			"url": srv.URL, "format": "text",
		}), tc)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Output, "ok") {
			t.Fatalf("output = %q", res.Output)
		}
	})

	t.Run("host on list permits", func(t *testing.T) {
		tc := &Context{
			WorkDir:      t.TempDir(),
			Ask:          allowAll,
			NetworkAllow: []string{host, "example.com"},
		}
		if _, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
			"url": srv.URL, "format": "text",
		}), tc); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("host not on list denies", func(t *testing.T) {
		tc := &Context{
			WorkDir:      t.TempDir(),
			Ask:          allowAll,
			NetworkAllow: []string{"example.com", "api.github.com"},
		}
		_, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
			"url": srv.URL, "format": "text",
		}), tc)
		if err == nil {
			t.Fatal("want allowlist error")
		}
		if !strings.Contains(err.Error(), "allowlist") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("redirect off allowlist denies", func(t *testing.T) {
		// First hop is allowlisted test host; redirect target is not.
		redir := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://evil.example/path", http.StatusFound)
		})
		ru, err := url.Parse(redir.URL)
		if err != nil {
			t.Fatal(err)
		}
		tc := &Context{
			WorkDir:      t.TempDir(),
			Ask:          allowAll,
			NetworkAllow: []string{ru.Hostname()},
		}
		_, err = NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{
			"url": redir.URL, "format": "text",
		}), tc)
		if err == nil {
			t.Fatal("want redirect allowlist error")
		}
		// May fail on allowlist or on SSRF/resolve of evil.example — either is deny.
		if !strings.Contains(err.Error(), "allowlist") &&
			!strings.Contains(err.Error(), "evil.example") &&
			!strings.Contains(err.Error(), "resolving") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestWebfetchSafeTransportDialAllowlist(t *testing.T) {
	// Public IP not on allowlist must fail at dial even if SSRF would pass.
	tr := newWebfetchSafeTransport([]string{"1.1.1.1"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := tr.DialContext(ctx, "tcp", "8.8.8.8:53")
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("want allowlist dial deny")
	}
	if !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("err = %v", err)
	}
	// Exact IP on list: dial may still fail to connect, but not allowlist/SSRF.
	conn, err = tr.DialContext(ctx, "tcp", "1.1.1.1:9")
	if conn != nil {
		_ = conn.Close()
	}
	if err != nil && strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("allowlisted IP rejected by allowlist: %v", err)
	}
	if err != nil && (strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "local")) {
		t.Fatalf("public allowlisted IP treated as private: %v", err)
	}
}

func TestResolvePublicDialAddr(t *testing.T) {
	ctx := context.Background()

	t.Run("public literal", func(t *testing.T) {
		got, err := resolvePublicDialAddr(ctx, "8.8.8.8")
		if err != nil {
			t.Fatal(err)
		}
		if got != "8.8.8.8" {
			t.Errorf("got %q", got)
		}
	})

	blocked := []struct {
		host string
		sub  string
	}{
		{"127.0.0.1", "private or local"},
		{"10.0.0.5", "private or local"},
		{"192.168.1.1", "private or local"},
		{"172.16.0.1", "private or local"},
		{"169.254.169.254", "private or local"},
		{"::1", "private or local"},
		{"0.0.0.0", "private or local"},
		{"100.64.1.1", "private or local"},
		{"198.18.0.1", "private or local"},
	}
	for _, tt := range blocked {
		t.Run(tt.host, func(t *testing.T) {
			_, err := resolvePublicDialAddr(ctx, tt.host)
			if err == nil {
				t.Fatal("expected block")
			}
			if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "local") {
				t.Errorf("err = %v, want private/local refusal", err)
			}
		})
	}
}

func TestNewWebfetchSafeTransportDialBlocksPrivate(t *testing.T) {
	tr := newWebfetchSafeTransport(nil)
	if tr == nil || tr.DialContext == nil {
		t.Fatal("expected transport with DialContext")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cases := []string{
		"127.0.0.1:80",
		"10.0.0.1:443",
		"192.168.0.1:80",
		"169.254.169.254:80",
		"[::1]:80",
		"100.64.0.1:80",
		"198.18.0.1:80",
	}
	for _, addr := range cases {
		t.Run(addr, func(t *testing.T) {
			conn, err := tr.DialContext(ctx, "tcp", addr)
			if conn != nil {
				_ = conn.Close()
				t.Fatal("expected dial to be blocked")
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "local") {
				t.Errorf("err = %v, want private/local refusal", err)
			}
		})
	}
}

// Dial-time guard must reject even when the pre-check allowlist would pass
// (simulates DNS rebinding: hostname allowlisted at plan time, dial sees private IP).
func TestWebfetchSafeTransportDialIgnoresTestAllowHost(t *testing.T) {
	// assertPublicHTTPHost may allow webfetchTestAllowHost; DialContext must still
	// filter the resolved/literal address.
	prev := webfetchTestAllowHost
	webfetchTestAllowHost = "127.0.0.1"
	t.Cleanup(func() { webfetchTestAllowHost = prev })

	if err := assertPublicHTTPHost("127.0.0.1"); err != nil {
		t.Fatalf("allowlist should pass assertPublicHTTPHost: %v", err)
	}

	tr := newWebfetchSafeTransport(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := tr.DialContext(ctx, "tcp", "127.0.0.1:9")
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("DialContext must block loopback even when test allow host is set")
	}
	if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "local") {
		t.Errorf("err = %v", err)
	}
}
