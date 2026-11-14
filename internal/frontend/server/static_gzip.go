package server

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// gzipAssets memoizes gzip encodings of static bundle files (#1514). The
// static FS is immutable for a Server's lifetime (go:embed or an injected
// test FS), so each path is compressed at most once, on first request, and
// the wasm bundle (~3-4x smaller gzipped) never goes over the wire raw to a
// client that accepts gzip.
type gzipAssets struct {
	mu sync.Mutex
	m  map[string]*gzipEntry
}

type gzipEntry struct {
	once sync.Once
	data []byte // nil when compression did not shrink the payload
}

// get returns the gzip encoding of raw for path, or nil when gzip would not
// make it smaller.
func (g *gzipAssets) get(path string, raw []byte) []byte {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*gzipEntry)
	}
	e, ok := g.m[path]
	if !ok {
		e = &gzipEntry{}
		g.m[path] = e
	}
	g.mu.Unlock()
	e.once.Do(func() {
		var buf bytes.Buffer
		zw, err := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
		if err != nil {
			return
		}
		if _, err := zw.Write(raw); err != nil {
			return
		}
		if err := zw.Close(); err != nil {
			return
		}
		if buf.Len() < len(raw) {
			e.data = buf.Bytes()
		}
	})
	return e.data
}

// warmStaticGzip precompresses compressible files under assets/ so the
// first cold load does not pay the (~0.3 s for the wasm) compression cost.
// Started from Serve/ListenAndServe, not New, so unit tests that only use
// Handler() never spend CPU on the embedded bundle.
func (s *Server) warmStaticGzip() {
	_ = fs.WalkDir(s.static, "assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !compressibleExt[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if data, err := fs.ReadFile(s.static, path); err == nil {
			s.gzip.get(path, data)
		}
		return nil
	})
}

// compressibleExt lists text-like bundle types worth gzipping. Images are
// already compressed and are served as-is.
var compressibleExt = map[string]bool{
	".js":          true,
	".css":         true,
	".wasm":        true,
	".svg":         true,
	".html":        true,
	".json":        true,
	".webmanifest": true,
}

// writeStatic writes a static file body, gzip-encoded when the file type is
// compressible and the client accepts gzip. Callers set Content-Type and
// Cache-Control first; Vary is always added for compressible types so shared
// caches never hand a gzip body to a client that did not ask for it.
func (s *Server) writeStatic(w http.ResponseWriter, r *http.Request, path string, data []byte) {
	if !compressibleExt[strings.ToLower(filepath.Ext(path))] {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
		return
	}
	w.Header().Add("Vary", "Accept-Encoding")
	if acceptsGzip(r.Header.Values("Accept-Encoding")) {
		if gz := s.gzip.get(path, data); gz != nil {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Length", strconv.Itoa(len(gz)))
			_, _ = w.Write(gz)
			return
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// acceptsGzip reports whether Accept-Encoding values permit gzip (RFC 9110
// §12.5.3): an explicit gzip/x-gzip coding with non-zero q wins; otherwise a
// non-zero "*" allows it.
func acceptsGzip(values []string) bool {
	star := false
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			name, params, _ := strings.Cut(part, ";")
			name = strings.ToLower(strings.TrimSpace(name))
			q := 1.0
			for _, p := range strings.Split(params, ";") {
				k, val, ok := strings.Cut(strings.TrimSpace(p), "=")
				if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
					f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
					if err != nil {
						f = 0
					}
					q = f
				}
			}
			switch name {
			case "gzip", "x-gzip":
				return q > 0
			case "*":
				star = q > 0
			}
		}
	}
	return star
}
