package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return out
}

func TestStaticGzipEncoding(t *testing.T) {
	wasm := bytes.Repeat([]byte("\x00asm wasm body "), 4096)
	js := []byte(strings.Repeat("console.log('strike');\n", 512))
	png := bytes.Repeat([]byte{0x89, 'P', 'N', 'G'}, 1024)
	sw := []byte(strings.Repeat("self.addEventListener('fetch', () => {});\n", 64))
	index := []byte("<!doctype html><title>Strike</title>" + strings.Repeat("<div></div>", 200))
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Static: fstest.MapFS{
			"index.html":          {Data: index},
			"assets/app.wasm":     {Data: wasm},
			"assets/app.js":       {Data: js},
			"assets/icon-192.png": {Data: png},
			"assets/tiny.css":     {Data: []byte("a{}")},
			"sw.js":               {Data: sw},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		path      string
		accept    string
		raw       []byte
		wantGzip  bool
		wantVary  bool
		wantCache string
	}{
		{"wasm gzip", "/assets/app.wasm", "gzip, deflate, br", wasm, true, true, "immutable"},
		{"wasm no accept", "/assets/app.wasm", "", wasm, false, true, "immutable"},
		{"wasm gzip q=0", "/assets/app.wasm", "gzip;q=0, br", wasm, false, true, "immutable"},
		{"wasm br only", "/assets/app.wasm", "br", wasm, false, true, "immutable"},
		{"wasm star", "/assets/app.wasm", "*", wasm, true, true, "immutable"},
		{"js gzip", "/assets/app.js", "gzip", js, true, true, "immutable"},
		{"png never encoded", "/assets/icon-192.png", "gzip", png, false, false, "immutable"},
		{"tiny css stays raw", "/assets/tiny.css", "gzip", []byte("a{}"), false, true, "immutable"},
		{"sw.js gzip no-store", "/sw.js", "gzip", sw, true, true, "no-store"},
		{"index gzip no-store", "/", "gzip", index, true, true, "no-store"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.accept != "" {
				req.Header.Set("Accept-Encoding", tc.accept)
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d", tc.path, rec.Code)
			}
			h := rec.Header()
			if got := h.Get("Cache-Control"); !strings.Contains(got, tc.wantCache) {
				t.Fatalf("Cache-Control = %q, want %q", got, tc.wantCache)
			}
			if got := strings.Contains(strings.Join(h.Values("Vary"), ","), "Accept-Encoding"); got != tc.wantVary {
				t.Fatalf("Vary = %q, want Accept-Encoding=%v", h.Values("Vary"), tc.wantVary)
			}
			body := rec.Body.Bytes()
			if got := h.Get("Content-Length"); got != strconv.Itoa(len(body)) {
				t.Fatalf("Content-Length = %q, body %d", got, len(body))
			}
			if tc.wantGzip {
				if h.Get("Content-Encoding") != "gzip" {
					t.Fatalf("Content-Encoding = %q, want gzip", h.Get("Content-Encoding"))
				}
				if len(body) >= len(tc.raw) {
					t.Fatalf("gzip body %d not smaller than raw %d", len(body), len(tc.raw))
				}
				body = gunzip(t, body)
			} else if ce := h.Get("Content-Encoding"); ce != "" {
				t.Fatalf("Content-Encoding = %q, want none", ce)
			}
			if !bytes.Equal(body, tc.raw) {
				t.Fatalf("decoded body mismatch: %d vs %d bytes", len(body), len(tc.raw))
			}
		})
	}
}

func TestStaticGzipConcurrentFirstRequest(t *testing.T) {
	raw := bytes.Repeat([]byte("wasm-bytes-"), 8192)
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Static: fstest.MapFS{
			"index.html":      {Data: []byte("<title>Strike</title>")},
			"assets/app.wasm": {Data: raw},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan string, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/assets/app.wasm", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Header().Get("Content-Encoding") != "gzip" {
				errs <- "missing gzip encoding"
				return
			}
			zr, err := gzip.NewReader(rec.Body)
			if err != nil {
				errs <- err.Error()
				return
			}
			out, err := io.ReadAll(zr)
			if err != nil || !bytes.Equal(out, raw) {
				errs <- "decoded mismatch"
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}

func TestStaticGzipEmbeddedWasmBundle(t *testing.T) {
	// The real committed bundle: the wasm must go over the wire gzipped and
	// materially smaller (#1514 measured ~3.3x).
	matches, _ := fs.Glob(staticFS, "assets/*.wasm")
	if len(matches) == 0 {
		t.Skip("no wasm in embedded bundle")
	}
	raw, err := fs.ReadFile(staticFS, matches[0])
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/"+matches[0], nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("embedded wasm Content-Encoding = %q", rec.Header().Get("Content-Encoding"))
	}
	if got := rec.Header().Get("Content-Type"); got != "application/wasm" {
		t.Fatalf("Content-Type = %q", got)
	}
	if rec.Body.Len()*2 > len(raw) {
		t.Fatalf("gzip wasm %d bytes, raw %d: expected at least 2x reduction", rec.Body.Len(), len(raw))
	}
	t.Logf("wasm %s: raw %d bytes, gzip %d bytes", matches[0], len(raw), rec.Body.Len())
}

func TestAcceptsGzip(t *testing.T) {
	cases := []struct {
		in   []string
		want bool
	}{
		{nil, false},
		{[]string{""}, false},
		{[]string{"gzip"}, true},
		{[]string{"GZIP"}, true},
		{[]string{"x-gzip"}, true},
		{[]string{"deflate, gzip;q=0.5"}, true},
		{[]string{"gzip;q=0"}, false},
		{[]string{"gzip; q=0.0"}, false},
		{[]string{"gzip;q=bogus"}, false},
		{[]string{"br", "gzip"}, true},
		{[]string{"identity"}, false},
		{[]string{"*"}, true},
		{[]string{"*;q=0"}, false},
		{[]string{"*, gzip;q=0"}, false},
	}
	for _, tc := range cases {
		if got := acceptsGzip(tc.in); got != tc.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestWarmStaticGzipPrecompressesAssets(t *testing.T) {
	js := []byte(strings.Repeat("console.log('warm');\n", 256))
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Static: fstest.MapFS{
			"index.html":          {Data: []byte("<title>Strike</title>")},
			"assets/app.js":       {Data: js},
			"assets/icon-192.png": {Data: bytes.Repeat([]byte{1}, 512)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.warmStaticGzip()
	srv.gzip.mu.Lock()
	_, jsWarm := srv.gzip.m["assets/app.js"]
	_, pngWarm := srv.gzip.m["assets/icon-192.png"]
	srv.gzip.mu.Unlock()
	if !jsWarm || pngWarm {
		t.Fatalf("warm cache js=%v png=%v, want js only", jsWarm, pngWarm)
	}
}
