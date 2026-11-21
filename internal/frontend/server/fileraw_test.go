package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
	"github.com/jonathanung/strike/pkg/protocol"
)

func TestRawContentTypeAndDisposition(t *testing.T) {
	cases := []struct {
		path, ctype string
		inline      bool
		disp        string
	}{
		{"shots/a.png", "image/png", true, `inline; filename=a.png`},
		{"A.JPG", "image/jpeg", true, `inline; filename=A.JPG`},
		{"x.jpeg", "image/jpeg", true, `inline; filename=x.jpeg`},
		{"x.gif", "image/gif", true, `inline; filename=x.gif`},
		{"x.webp", "image/webp", true, `inline; filename=x.webp`},
		{"x.avif", "image/avif", true, `inline; filename=x.avif`},
		{"x.bmp", "image/bmp", true, `inline; filename=x.bmp`},
		{"favicon.ico", "image/x-icon", true, `inline; filename=favicon.ico`},
		{"d/logo.svg", "image/svg+xml", true, `inline; filename=logo.svg`},
		{"build/app.bin", "application/octet-stream", false, `attachment; filename=app.bin`},
		{"page.html", "application/octet-stream", false, `attachment; filename=page.html`},
		{"noext", "application/octet-stream", false, `attachment; filename=noext`},
	}
	for _, tc := range cases {
		ctype, inline := rawContentType(tc.path)
		if ctype != tc.ctype || inline != tc.inline {
			t.Errorf("%s: type %q inline %v", tc.path, ctype, inline)
		}
		if got := rawDisposition(tc.path, inline); got != tc.disp {
			t.Errorf("%s: disposition %q, want %q", tc.path, got, tc.disp)
		}
	}
	if got := rawDisposition("dir/my file.png", true); got != `inline; filename="my file.png"` {
		t.Errorf("quoted filename = %q", got)
	}
}

func TestFileRawRoute(t *testing.T) {
	dir := t.TempDir()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool.bin"), []byte("a\x00b"), 0o644); err != nil {
		t.Fatal(err)
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	if err := os.WriteFile(filepath.Join(dir, "logo.svg"), svg, 0o644); err != nil {
		t.Fatal(err)
	}
	big, err := os.Create(filepath.Join(dir, "huge.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(maxRawViewBytes + 1); err != nil {
		t.Fatal(err)
	}
	big.Close()
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-out.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-out.png")) })

	files := local.NewFiles(dir)
	live := NewLive("live-raw", dir, nil, make(chan protocol.Op, 1))
	srv := mustServer(t, Options{Live: live, Services: &host.Services{Files: files}})
	get := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/file/raw?path="+path, nil))
		return rr
	}

	rr := get("shot.png")
	if rr.Code != http.StatusOK || !bytes.Equal(rr.Body.Bytes(), png) {
		t.Fatalf("png %d %q", rr.Code, rr.Body.String())
	}
	h := rr.Header()
	if h.Get("Content-Type") != "image/png" || !strings.HasPrefix(h.Get("Content-Disposition"), "inline") {
		t.Fatalf("png headers %v", h)
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("png hardening headers %v", h)
	}

	rr = get("tool.bin")
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "application/octet-stream" ||
		rr.Header().Get("Content-Disposition") != "attachment; filename=tool.bin" {
		t.Fatalf("bin %d %v", rr.Code, rr.Header())
	}

	rr = get("logo.svg")
	csp := rr.Header().Get("Content-Security-Policy")
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "image/svg+xml" ||
		!strings.Contains(csp, "sandbox") || !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("svg %d %v", rr.Code, rr.Header())
	}

	rr = get("huge.png")
	var body documentError
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if rr.Code != http.StatusRequestEntityTooLarge || body.Code != "too_large" || body.Size != maxRawViewBytes+1 {
		t.Fatalf("oversize %d %s", rr.Code, rr.Body.String())
	}

	for _, p := range []string{"../" + filepath.Base(dir) + "-out.png", "missing.png", ""} {
		rr = get(p)
		if rr.Code != http.StatusBadRequest && rr.Code != http.StatusNotFound {
			t.Fatalf("%q → %d %s", p, rr.Code, rr.Body.String())
		}
		if bytes.Contains(rr.Body.Bytes(), []byte("PNG")) {
			t.Fatalf("%q leaked file bytes", p)
		}
	}

	// No files service: the capability is reported, not a panic.
	bare := mustServer(t, Options{})
	rr = httptest.NewRecorder()
	bare.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/file/raw?path=shot.png", nil))
	if rr.Code == http.StatusOK {
		t.Fatalf("no files service → %d", rr.Code)
	}
}

func TestFileRawRequiresAuth(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), []byte("\x89PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := mustServer(t, Options{
		Auth:       true,
		Token:      "secret",
		SessionDir: t.TempDir(),
		Services:   &host.Services{Files: local.NewFiles(dir)},
	})
	cases := []struct {
		name   string
		url    string
		header string
		cookie string
		want   int
	}{
		{name: "none", url: "/v1/file/raw?path=shot.png", want: http.StatusUnauthorized},
		{name: "bearer", url: "/v1/file/raw?path=shot.png", header: "Bearer secret", want: http.StatusOK},
		{name: "cookie", url: "/v1/file/raw?path=shot.png", cookie: "secret", want: http.StatusOK},
		{name: "wrong bearer", url: "/v1/file/raw?path=shot.png", header: "Bearer nope", want: http.StatusUnauthorized},
		// Query secrets stay refused: an <img src> must not carry the token.
		{name: "query token", url: "/v1/file/raw?path=shot.png&token=secret", want: http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: authCookieName, Value: tc.cookie})
			}
			rr := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("%s → %d %s", tc.name, rr.Code, rr.Body.String())
			}
		})
	}
}
