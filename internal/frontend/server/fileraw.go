package server

import (
	"errors"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/jonathanung/strike/internal/frontend/host/local"
)

// maxRawViewBytes caps GET /v1/file/raw. It matches the document view's
// read cap (#1393 open question (a): same cap, clear error past it).
const maxRawViewBytes = 1 << 20

// rawImageTypes is the extension allowlist served inline as images. Every
// other file is served as an octet-stream attachment.
var rawImageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
	".svg":  "image/svg+xml",
}

// rawContentType maps a workspace path to its served Content-Type and
// whether it is an inline image.
func rawContentType(p string) (string, bool) {
	if t, ok := rawImageTypes[strings.ToLower(path.Ext(p))]; ok {
		return t, true
	}
	return "application/octet-stream", false
}

// rawDisposition builds Content-Disposition for p: inline for allowlisted
// images, attachment for everything else, with the base name as filename.
func rawDisposition(p string, inline bool) string {
	kind := "attachment"
	if inline {
		kind = "inline"
	}
	name := path.Base(strings.ReplaceAll(p, "\\", "/"))
	if name == "" || name == "." || name == "/" {
		return kind
	}
	if v := mime.FormatMediaType(kind, map[string]string{"filename": name}); v != "" {
		return v
	}
	return kind
}

// handleFileRaw serves GET /v1/file/raw?path=… (#1393): one workspace
// file's bytes under the document view's confinement (files root plus the
// allowed-roots ceiling), behind the same /v1 auth as every other route.
// Read-only. The response is sandboxed by CSP so an SVG (or anything else)
// opened directly can never run script on the daemon's origin.
func (s *Server) handleFileRaw(w http.ResponseWriter, r *http.Request) {
	files := s.documentFiles()
	if files == nil {
		capabilityUnavailable(w, "files")
		return
	}
	ApplyDocumentRoots(files, s.opts.Workspaces)
	doc, ok, err := local.ReadDocumentRaw(files, r.URL.Query().Get("path"), maxRawViewBytes)
	if !ok {
		capabilityUnavailable(w, "files")
		return
	}
	if err != nil {
		status, code := documentStatus(err), "read_failed"
		if errors.Is(err, local.ErrRawTooLarge) {
			status, code = http.StatusRequestEntityTooLarge, "too_large"
		}
		writeJSON(w, status, documentError{Error: clipErr(err), Code: code, Size: doc.Size})
		return
	}
	ctype, inline := rawContentType(doc.Path)
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Disposition", rawDisposition(doc.Path, inline))
	h.Set("Content-Length", strconv.Itoa(len(doc.Data)))
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc.Data)
}
