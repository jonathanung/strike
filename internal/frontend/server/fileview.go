package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
)

// allowedRootLister is the hook #1260 can satisfy on the workspace registry.
// A non-empty list is a hard ceiling: document reads and writes whose resolved
// path is outside every entry are rejected. An empty list, or a registry that
// does not implement this, keeps the files work root as the only confinement.
type allowedRootLister interface {
	AllowedRoots() []string
}

func allowedRootsFrom(source any) ([]string, bool) {
	if source == nil {
		return nil, false
	}
	lister, ok := source.(allowedRootLister)
	if !ok {
		return nil, false
	}
	var roots []string
	for _, root := range lister.AllowedRoots() {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		roots = append(roots, root)
	}
	if len(roots) == 0 {
		return nil, false
	}
	return roots, true
}

// ApplyDocumentRoots pushes an explicit allow-list onto the files service
// when source exposes one. Otherwise the ceiling is cleared.
func ApplyDocumentRoots(files host.Files, source any) {
	if files == nil {
		return
	}
	if roots, ok := allowedRootsFrom(source); ok {
		local.SetFilesRootCeiling(files, roots, true)
		return
	}
	local.SetFilesRootCeiling(files, nil, false)
}

type documentView struct {
	Path          string `json:"path"`
	Content       string `json:"content,omitempty"`
	SHA256        string `json:"sha256"`
	MtimeUnixNano string `json:"mtimeUnixNano"`
	Size          int64  `json:"size"`
	Binary        bool   `json:"binary"`
	Truncated     bool   `json:"truncated"`
	Notice        string `json:"notice,omitempty"`
	Writable      bool   `json:"writable"`
	WriteBlock    string `json:"writeBlock,omitempty"`
}

type documentPutRequest struct {
	Path          string  `json:"path"`
	Content       *string `json:"content"`
	SHA256        string  `json:"sha256"`
	MtimeUnixNano string  `json:"mtimeUnixNano"`
}

type documentPutResponse struct {
	OK            bool   `json:"ok"`
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	MtimeUnixNano string `json:"mtimeUnixNano"`
	Size          int64  `json:"size"`
}

type documentError struct {
	Error         string `json:"error"`
	Code          string `json:"code,omitempty"`
	SHA256        string `json:"sha256,omitempty"`
	MtimeUnixNano string `json:"mtimeUnixNano,omitempty"`
	Size          int64  `json:"size,omitempty"`
}

func (s *Server) handleFileView(w http.ResponseWriter, r *http.Request) {
	files := s.documentFiles()
	if files == nil {
		capabilityUnavailable(w, "files")
		return
	}
	ApplyDocumentRoots(files, s.opts.Workspaces)
	doc, err := files.ReadDocument(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, documentStatus(err), documentError{Error: clipErr(err), Code: "read_failed"})
		return
	}
	block := s.documentWriteBlock(r, doc.Path)
	if doc.Binary {
		block = preferText(block, "binary file")
	} else if doc.Truncated {
		block = preferText(block, "file is too large to edit")
	}
	writeJSON(w, http.StatusOK, documentView{
		Path:          doc.Path,
		Content:       doc.Content,
		SHA256:        doc.SHA256,
		MtimeUnixNano: strconv.FormatInt(doc.MtimeUnixNano, 10),
		Size:          doc.Size,
		Binary:        doc.Binary,
		Truncated:     doc.Truncated,
		Notice:        doc.Notice,
		Writable:      block == "",
		WriteBlock:    block,
	})
}

func (s *Server) handleFilePut(w http.ResponseWriter, r *http.Request) {
	files := s.documentFiles()
	if files == nil {
		capabilityUnavailable(w, "files")
		return
	}
	if !s.hasLive() {
		writeJSON(w, http.StatusForbidden, documentError{Error: "attach_only", Code: "attach_only"})
		return
	}
	sessionID, err := s.admitFileMutation(r, "files.write")
	if err != nil {
		switch {
		case errors.Is(err, errOpsReadOnly):
			writeJSON(w, http.StatusForbidden, documentError{Error: err.Error(), Code: "read_only"})
		case errors.Is(err, errOpsRateLimited):
			writeJSON(w, http.StatusTooManyRequests, documentError{Error: err.Error(), Code: "rate_limited"})
		default:
			writeJSON(w, http.StatusForbidden, documentError{Error: err.Error(), Code: "forbidden"})
		}
		return
	}
	var req documentPutRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, documentError{Error: "validation: " + err.Error(), Code: "validation"})
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		writeJSON(w, http.StatusBadRequest, documentError{Error: "validation: path is required", Code: "validation"})
		return
	}
	if req.Content == nil {
		writeJSON(w, http.StatusBadRequest, documentError{Error: "validation: content is required", Code: "validation"})
		return
	}
	if block := s.documentWriteBlock(r, req.Path); block != "" {
		writeJSON(w, http.StatusForbidden, documentError{Error: block, Code: "permission_denied"})
		return
	}
	mtime, err := parseMtime(req.MtimeUnixNano)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, documentError{Error: "validation: mtimeUnixNano", Code: "validation"})
		return
	}
	ApplyDocumentRoots(files, s.opts.Workspaces)
	res, err := files.WriteDocument(host.DocumentWrite{
		Path:                req.Path,
		Content:             *req.Content,
		ExpectSHA256:        req.SHA256,
		ExpectMtimeUnixNano: mtime,
	})
	if err != nil {
		var conflict *host.DocumentConflict
		if errors.As(err, &conflict) {
			writeJSON(w, http.StatusConflict, documentError{
				Error:         "conflict: file changed",
				Code:          "conflict",
				SHA256:        conflict.SHA256,
				MtimeUnixNano: strconv.FormatInt(conflict.MtimeUnixNano, 10),
				Size:          conflict.Size,
			})
			return
		}
		s.recordServeOpError(r, "http", "files.write", sessionID)
		writeJSON(w, documentStatus(err), documentError{Error: clipErr(err), Code: "write_failed"})
		return
	}
	s.auditOpOK(r, "http", "files.write", sessionID)
	writeJSON(w, http.StatusOK, documentPutResponse{
		OK:            true,
		Path:          res.Path,
		SHA256:        res.SHA256,
		MtimeUnixNano: strconv.FormatInt(res.MtimeUnixNano, 10),
		Size:          res.Size,
	})
}

func (s *Server) documentFiles() host.Files {
	if s.opts.Services == nil {
		return nil
	}
	return s.opts.Services.Files
}

// documentWriteBlock reports why a UI save must be refused.
// Plan mode denies writes. An edit or write rule whose action is deny also
// refuses, including under yolo (deny is not widened). Ask is allowed: the
// Save click is the user's own approval. An explicit allowed-root miss is
// enforced in the files service, not here.
func (s *Server) documentWriteBlock(r *http.Request, path string) string {
	if !s.hasLive() {
		return "attach a live session to edit"
	}
	mode := ""
	if live := s.resolveLive(nil, r); live != nil {
		mode = strings.ToLower(strings.TrimSpace(live.Status().PermissionMode))
	}
	if mode == "plan" {
		return "plan mode denies file writes"
	}
	if s.opts.Services == nil || s.opts.Services.Permissions == nil {
		return ""
	}
	for _, perm := range []string{"edit", "write"} {
		ex := s.opts.Services.Permissions.Explain(perm, path)
		if strings.EqualFold(strings.TrimSpace(ex.Action), "deny") {
			if strings.TrimSpace(ex.Summary) != "" {
				return strings.TrimSpace(ex.Summary)
			}
			return perm + " is denied for this path"
		}
	}
	return ""
}

func parseMtime(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, errInvalidMtime
	}
	return n, nil
}

var errInvalidMtime = errors.New("mtime")

func documentStatus(err error) int {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "not found"):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

func clipErr(err error) string {
	msg := err.Error()
	if len(msg) > 240 {
		return msg[:240] + "…"
	}
	return msg
}

func preferText(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
