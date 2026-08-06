// Package server is the experimental HTTP/WebSocket attach surface for strike
// sessions: health/version, SSE JSONL tails, live ops bridge, and cockpit page.
package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/session"
	"github.com/jonathanung/strike-cli/internal/version"
)

// Options configures the attach server.
type Options struct {
	// Addr is the bind address (host:port). Default "127.0.0.1:8787".
	Addr string
	// Auth enables bearer authentication for API routes.
	Auth bool
	// Token is required when Auth is true.
	Token string
	// SessionDir is the JSONL sessions directory. Default session.DefaultDir().
	SessionDir string
	// Static is optional override for the attach page FS (tests). Nil uses embed.
	Static fs.FS
	// PollInterval is how often the SSE tail checks for new JSONL bytes.
	// Default 200ms. Tests may lower it.
	PollInterval time.Duration
	// Live is an optional engine bridge for composer/ops/status. Nil keeps
	// read-only attach (JSONL SSE only). When LiveHub is set, it takes
	// precedence and Live is ignored.
	Live *Live
	// LiveHub is the multi-root active-agent bridge. When set, ops, status,
	// events, and WebSocket are scoped to ?root=<id>. Nil means single-root
	// via Live (or attach-only when both are nil).
	LiveHub *LiveHub
	// Expose enables LAN-oriented CORS (private-network browser origins) in
	// addition to localhost. Set when strike serve --expose is used.
	Expose bool
	// AllowCIDRs, when non-empty, rejects requests whose client IP falls
	// outside the list (all routes including /health).
	AllowCIDRs []*net.IPNet
	// Services exposes optional frontend host capabilities.
	Services *host.Services
	// Sandbox enables the sandbox capability and seeds live roots that do not
	// call Live.SetSandbox themselves. Nil keeps capabilities.sandbox false.
	Sandbox *SandboxSnapshot
}

// Server is an HTTP server for session attach and optional live cockpit.
type Server struct {
	opts   Options
	mux    *http.ServeMux
	http   *http.Server
	static fs.FS

	// paneHost supervises process panes for the web cockpit (#732).
	paneHost *paneHost
}

// New validates options and builds a Server. Does not listen.
func New(opts Options) (*Server, error) {
	opts.Token = strings.TrimSpace(opts.Token)
	if opts.Auth && opts.Token == "" {
		return nil, errors.New("server: token is required when auth is enabled")
	}
	if !opts.Auth && opts.Token != "" {
		return nil, errors.New("server: token requires auth")
	}
	if strings.TrimSpace(opts.Addr) == "" {
		opts.Addr = "127.0.0.1:8787"
	}
	if strings.TrimSpace(opts.SessionDir) == "" {
		opts.SessionDir = session.DefaultDir()
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 200 * time.Millisecond
	}
	static := opts.Static
	if static == nil {
		static = staticFS
	}
	s := &Server{opts: opts, static: static, mux: http.NewServeMux()}
	s.routes()
	s.http = &http.Server{
		Addr:              opts.Addr,
		Handler:           s.withMiddleware(s.mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s, nil
}

// Addr returns the configured bind address.
func (s *Server) Addr() string { return s.opts.Addr }

// Token returns the configured auth token.
func (s *Server) Token() string { return s.opts.Token }

// Handler returns the root HTTP handler (for httptest).
func (s *Server) Handler() http.Handler { return s.http.Handler }

// ListenAndServe binds and serves until the server is closed.
func (s *Server) ListenAndServe() error {
	return s.http.ListenAndServe()
}

// Serve serves on ln until closed.
func (s *Server) Serve(ln net.Listener) error {
	s.http.Addr = ln.Addr().String()
	return s.http.Serve(ln)
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// MintToken returns a random 32-byte hex token suitable for --token.
func MintToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// IsLocalhostBind reports whether addr targets loopback only. Empty host
// (e.g. ":8787") binds all interfaces and is not localhost.
func IsLocalhostBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /v1/bootstrap", s.handleBootstrap)
	s.mux.HandleFunc("GET /v1/providers", s.handleProviders)
	s.mux.HandleFunc("POST /v1/auth/key", s.handleAuthKey)
	s.mux.HandleFunc("DELETE /v1/auth/{provider}", s.handleAuthLogout)
	s.mux.HandleFunc("GET /v1/models", s.handleModels)
	s.mux.HandleFunc("GET /v1/history", s.handleHistory)
	s.mux.HandleFunc("GET /v1/settings", s.handleSettings)
	s.mux.HandleFunc("PATCH /v1/settings", s.handleSettings)
	s.mux.HandleFunc("GET /v1/sandbox", s.handleSandboxGet)
	s.mux.HandleFunc("PATCH /v1/sandbox", s.handleSandboxPatch)
	s.mux.HandleFunc("GET /v1/sessions/{id}/children", s.handleSessionChildren)
	s.mux.HandleFunc("POST /v1/sessions/{id}/fork", s.handleSessionFork)
	s.mux.HandleFunc("PATCH /v1/sessions/{id}", s.handleSessionRename)
	s.mux.HandleFunc("DELETE /v1/sessions/{id}", s.handleSessionDelete)
	s.mux.HandleFunc("GET /v1/files", s.handleFiles)
	s.mux.HandleFunc("GET /v1/changed-files", s.handleChangedFiles)
	s.mux.HandleFunc("GET /v1/file", s.handleFile)
	s.mux.HandleFunc("GET /v1/memory", s.handleMemory)
	s.mux.HandleFunc("GET /v1/memory/export", s.handleMemoryExport)
	s.mux.HandleFunc("POST /v1/memory/import", s.handleMemoryImport)
	s.mux.HandleFunc("PUT /v1/memory/{key}", s.handleMemoryPut)
	s.mux.HandleFunc("DELETE /v1/memory/{key}", s.handleMemoryDelete)
	s.mux.HandleFunc("POST /v1/issues", s.handleIssueCreate)
	s.mux.HandleFunc("GET /v1/issues/export", s.handleIssuesExport)
	s.mux.HandleFunc("POST /v1/issues/import", s.handleIssuesImport)
	s.mux.HandleFunc("POST /v1/issues/{id}/close", s.handleIssueClose)
	s.mux.HandleFunc("GET /v1/issues", s.handleIssues)
	s.mux.HandleFunc("GET /v1/permissions/explain", s.handlePermissionExplain)
	s.mux.HandleFunc("GET /v1/permissions/presets", s.handlePermissionPresets)
	s.mux.HandleFunc("GET /v1/plans", s.handlePlansList)
	s.mux.HandleFunc("POST /v1/plans", s.handlePlanCreate)
	s.mux.HandleFunc("GET /v1/plans/{id}", s.handlePlanGet)
	s.mux.HandleFunc("PATCH /v1/plans/{id}", s.handlePlanUpdateTitle)
	s.mux.HandleFunc("POST /v1/plans/{id}/sections", s.handlePlanAddSection)
	s.mux.HandleFunc("PATCH /v1/plans/{id}/sections/{sectionID}", s.handlePlanUpdateSection)
	s.mux.HandleFunc("POST /v1/plans/{id}/status", s.handlePlanSetStatus)
	s.mux.HandleFunc("POST /v1/plans/{id}/reopen", s.handlePlanReopen)
	s.mux.HandleFunc("GET /v1/goals", s.handleGoalsList)
	s.mux.HandleFunc("POST /v1/goals", s.handleGoalsSet)
	s.mux.HandleFunc("GET /v1/goals/{id}", s.handleGoalGet)
	s.mux.HandleFunc("POST /v1/goals/{id}/run", s.handleGoalRun)
	s.mux.HandleFunc("POST /v1/goals/{id}/pause", s.handleGoalPause)
	s.mux.HandleFunc("POST /v1/goals/{id}/resume", s.handleGoalResume)
	s.mux.HandleFunc("POST /v1/goals/{id}/abort", s.handleGoalAbort)
	s.mux.HandleFunc("GET /v1/goals/{id}/log", s.handleGoalLog)
	s.mux.HandleFunc("GET /v1/mcp", s.handleMCPList)
	s.mux.HandleFunc("POST /v1/mcp/retry", s.handleMCPRetry)
	s.mux.HandleFunc("POST /v1/mcp/disable", s.handleMCPDisable)
	// Plugin lifecycle + pane contributions (web parity #732).
	// Action paths are body-id based (POST /v1/plugins/disable) so static
	// segments never collide with plugin ids that contain dots/slashes.
	s.mux.HandleFunc("GET /v1/plugins/outdated", s.handlePluginOutdated)
	s.mux.HandleFunc("POST /v1/plugins/search", s.handlePluginSearch)
	s.mux.HandleFunc("POST /v1/plugins/install", s.handlePluginInstall)
	s.mux.HandleFunc("POST /v1/plugins/enable", s.handlePluginEnable)
	s.mux.HandleFunc("POST /v1/plugins/disable", s.handlePluginDisable)
	s.mux.HandleFunc("POST /v1/plugins/remove", s.handlePluginRemove)
	s.mux.HandleFunc("POST /v1/plugins/trust", s.handlePluginTrust)
	s.mux.HandleFunc("POST /v1/plugins/untrust", s.handlePluginUntrust)
	s.mux.HandleFunc("POST /v1/plugins/preview-update", s.handlePluginPreviewUpdate)
	s.mux.HandleFunc("POST /v1/plugins/update", s.handlePluginUpdate)
	s.mux.HandleFunc("GET /v1/plugins", s.handlePluginsList)
	s.mux.HandleFunc("GET /v1/plugins/{id}/trust-preview", s.handlePluginTrustPreview)
	s.mux.HandleFunc("GET /v1/plugins/{id}", s.handlePluginGet)
	s.mux.HandleFunc("GET /v1/panes", s.handlePanesList)
	s.mux.HandleFunc("GET /v1/panes/{id}/snapshot", s.handlePaneSnapshot)
	s.mux.HandleFunc("POST /v1/panes/{id}/mount", s.handlePaneMount)
	s.mux.HandleFunc("POST /v1/panes/{id}/unmount", s.handlePaneUnmount)
	s.mux.HandleFunc("POST /v1/panes/{id}/input", s.handlePaneInput)
	s.mux.HandleFunc("POST /v1/panes/{id}/resize", s.handlePaneResize)
	s.mux.HandleFunc("GET /v1/panes/{id}", s.handlePaneGet)
	s.mux.HandleFunc("GET /v1/lsp", s.handleLSP)
	s.mux.HandleFunc("POST /v1/lsp/retry", s.handleLSPRetry)
	s.mux.HandleFunc("POST /v1/lsp/{name}/disable", s.handleLSPDisable)
	s.mux.HandleFunc("GET /v1/diagnostics", s.handleDiagnostics)
	s.mux.HandleFunc("GET /v1/workflows", s.handleWorkflows)
	s.mux.HandleFunc("GET /v1/workflows/{name}", s.handleWorkflowGet)
	s.mux.HandleFunc("GET /v1/workflows/{name}/document", s.handleWorkflowDocument)
	s.mux.HandleFunc("POST /v1/workflows/{name}/start", s.handleWorkflowStart)
	s.mux.HandleFunc("POST /v1/workflows/stop", s.handleWorkflowStop)
	s.mux.HandleFunc("POST /v1/workflows/scaffold", s.handleWorkflowScaffold)
	s.mux.HandleFunc("POST /v1/workflows/validate", s.handleWorkflowValidate)
	s.mux.HandleFunc("POST /v1/workflows/format", s.handleWorkflowFormat)
	s.mux.HandleFunc("POST /v1/workflows/phase-grants", s.handleWorkflowPhaseGrants)
	s.mux.HandleFunc("POST /v1/workflows/save", s.handleWorkflowSave)
	s.mux.HandleFunc("POST /v1/workflow-drafts/review", s.handleWorkflowDraftReview)
	s.mux.HandleFunc("POST /v1/workflow-drafts/save", s.handleWorkflowDraftSave)
	s.mux.HandleFunc("GET /v1/sessions/{id}/events", s.handleSessionEvents)
	s.mux.HandleFunc("GET /v1/sessions/{id}/timeline", s.handleSessionTimeline)
	s.mux.HandleFunc("GET /v1/sessions/{id}/timeline/export", s.handleSessionTimelineExport)
	s.mux.HandleFunc("GET /v1/sessions", s.handleSessions)
	s.mux.HandleFunc("GET /v1/roots", s.handleRoots)
	s.mux.HandleFunc("POST /v1/roots", s.handleRootCreate)
	s.mux.HandleFunc("POST /v1/roots/{id}/activate", s.handleRootActivate)
	s.mux.HandleFunc("POST /v1/roots/{id}/resume", s.handleRootResume)
	s.mux.HandleFunc("DELETE /v1/roots/{id}", s.handleRootClose)
	s.mux.HandleFunc("GET /v1/status", s.handleStatus)
	s.mux.HandleFunc("GET /v1/agents", s.handleAgents)
	s.mux.HandleFunc("GET /v1/diag", s.handleDiag)
	s.mux.HandleFunc("POST /v1/diag", s.handleDiag)
	s.mux.HandleFunc("POST /v1/ops", s.handleOps)
	s.mux.HandleFunc("GET /v1/live/events", s.handleLiveEvents)
	s.mux.HandleFunc("GET /v1/ws", s.handleWS)
	s.mux.HandleFunc("GET /{$}", s.handleAttach)
	s.mux.HandleFunc("GET /attach", s.handleAttach)
	s.mux.HandleFunc("GET /assets/{path...}", s.handleAsset)
}

func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.applySecurityHeaders(w)
		if len(s.opts.AllowCIDRs) > 0 {
			ip := ClientIP(r.RemoteAddr)
			if !IPAllowed(ip, s.opts.AllowCIDRs) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		s.applyCORS(w, r)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if stateChanging(r.Method) {
			if origin := r.Header.Get("Origin"); origin != "" && !originAllowed(origin, s.opts.Expose) {
				http.Error(w, "origin not allowed", http.StatusForbidden)
				return
			}
		}
		// Public: health + attach shell. Session event streams require a token.
		if s.opts.Auth && requiresToken(r.URL.Path) && !s.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rootParam extracts ?root= from the request. Empty means use active.
func rootParam(r *http.Request) string {
	return strings.TrimSpace(r.URL.Query().Get("root"))
}

// resolveLive returns the Live bridge for the request: LiveHub-scoped when hub is
// set, otherwise the single Live. Returns nil + writes error response when no
// live bridge is available.
func (s *Server) resolveLive(w http.ResponseWriter, r *http.Request) *Live {
	if s.opts.LiveHub != nil {
		rootID := rootParam(r)
		live := s.opts.LiveHub.LiveFor(rootID)
		if live != nil {
			s.opts.LiveHub.MarkActive(live.SessionID())
		}
		return live
	}
	return s.opts.Live
}

func (s *Server) hasLive() bool {
	if s.opts.LiveHub != nil {
		return s.opts.LiveHub.Active() != nil
	}
	return s.opts.Live != nil
}

func stateChanging(method string) bool {
	return method == http.MethodPost || method == http.MethodPatch || method == http.MethodDelete || method == http.MethodPut
}

func (s *Server) applySecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; img-src 'self' data: blob:; style-src 'self'; script-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}

const authCookieName = "strike_serve_token"

func requiresToken(path string) bool {
	return strings.HasPrefix(path, "/v1/")
}

func (s *Server) authorized(r *http.Request) bool {
	want := s.opts.Token
	if want == "" {
		return false
	}
	// Any valid source is enough: Bearer header, HttpOnly cookie from attach
	// handoff, or ?token= for EventSource/WebSocket clients that cannot set
	// headers. Empty candidates are ignored so a missing Bearer still allows
	// cookie/query auth.
	if tokenEqual(bearerToken(r.Header.Get("Authorization")), want) {
		return true
	}
	if tokenEqual(cookieToken(r), want) {
		return true
	}
	if tokenEqual(strings.TrimSpace(r.URL.Query().Get("token")), want) {
		return true
	}
	return false
}

// bearerToken extracts a Bearer credential. Scheme match is case-insensitive
// per RFC 7235.
func bearerToken(auth string) string {
	auth = strings.TrimSpace(auth)
	const prefix = "bearer "
	if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(auth[len(prefix):])
}

func cookieToken(r *http.Request) string {
	c, err := r.Cookie(authCookieName)
	if err != nil || c == nil {
		return ""
	}
	return strings.TrimSpace(c.Value)
}

func tokenEqual(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// maybeHandoffToken consumes ?token= on the attach shell: valid tokens become
// an HttpOnly SameSite=Strict cookie and the URL is redirected without the
// secret so subsequent same-origin fetch/EventSource/WebSocket calls
// authenticate via cookie. Invalid tokens leave the page unauthenticated.
func (s *Server) maybeHandoffToken(w http.ResponseWriter, r *http.Request) bool {
	if !s.opts.Auth {
		return false
	}
	tok := strings.TrimSpace(r.URL.Query().Get("token"))
	if tok == "" {
		return false
	}
	if !tokenEqual(tok, s.opts.Token) {
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    s.opts.Token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// Secure omitted: strike serve is cleartext by design (loopback / --expose).
	})
	q := r.URL.Query()
	q.Del("token")
	target := r.URL.Path
	if target == "" {
		target = "/"
	}
	if enc := q.Encode(); enc != "" {
		target += "?" + enc
	}
	http.Redirect(w, r, target, http.StatusFound)
	return true
}

func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if originAllowed(origin, s.opts.Expose) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	}
}

func originAllowed(origin string, expose bool) bool {
	if origin == "" {
		return false
	}
	o := strings.ToLower(origin)
	if !strings.HasPrefix(o, "http://") && !strings.HasPrefix(o, "https://") {
		return false
	}
	host := originHost(o)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	// With --expose, allow private/LAN browser origins (e.g. Vite on another
	// device). Public internet origins stay denied.
	return expose && IsPrivateOrLoopbackIP(ip)
}

func originHost(origin string) string {
	rest := origin
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	host := rest
	if i := strings.Index(rest, "/"); i >= 0 {
		host = rest[:i]
	}
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	return strings.Trim(h, "[]")
}

type healthResponse struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		OK:      true,
		Version: version.Version,
		Commit:  version.Commit,
	})
}

func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	if s.maybeHandoffToken(w, r) {
		return
	}
	data, err := fs.ReadFile(s.static, "index.html")
	if err != nil {
		http.Error(w, "attach page missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	path := "assets/" + strings.TrimPrefix(r.PathValue("path"), "/")
	data, err := fs.ReadFile(s.static, path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch filepath.Ext(path) {
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(data)
}

func (s *Server) handleSessionEvents(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := validateSessionID(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	path := session.LogPath(s.opts.SessionDir, id)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	var offset int64
	// Initial backlog.
	n, err := s.writeEventsFrom(ctx, w, flusher, path, 0)
	if err != nil {
		return
	}
	offset = n

	ticker := time.NewTicker(s.opts.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := s.writeEventsFrom(ctx, w, flusher, path, offset)
			if err != nil {
				return
			}
			if n > offset {
				offset = n
			}
		}
	}
}

// writeEventsFrom reads JSONL from path starting at byte offset and writes each
// complete line as an SSE data event. Returns the new file offset.
func (s *Server) writeEventsFrom(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, path string, offset int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return offset, err
	}
	if st.Size() < offset {
		// Truncated/rewound log — restart from beginning.
		offset = 0
	}
	return s.writeEventsRangeFile(ctx, w, flusher, f, offset, st.Size())
}

func (s *Server) writeEventsRange(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, path string, offset, boundary int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer f.Close()
	return s.writeEventsRangeFile(ctx, w, flusher, f, offset, boundary)
}

func (s *Server) writeEventsRangeFile(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, f *os.File, offset, boundary int64) (int64, error) {
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}

	reader := bufio.NewReaderSize(io.LimitReader(f, boundary-offset), 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return offset, err
		}
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			// Only emit complete lines (trailing \n).
			if line[len(line)-1] != '\n' {
				// Incomplete trailing line; wait for more bytes.
				break
			}
			offset += int64(len(line))
			payload := bytes.TrimSpace(line)
			if len(payload) == 0 {
				continue
			}
			if !json.Valid(payload) {
				// Skip malformed lines rather than killing the stream.
				continue
			}
			// Skip session log schema header (#803); clients expect protocol events.
			if isSessionLogHeader(payload) {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return offset, err
			}
			flusher.Flush()
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return offset, nil
			}
			return offset, err
		}
	}
	return offset, nil
}

// isSessionLogHeader reports whether payload is the optional first-line
// session.header schema marker written by internal/session (#803).
func isSessionLogHeader(payload []byte) bool {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return false
	}
	return probe.Type == "session.header"
}

func validateSessionID(id string) error {
	if id == "" {
		return errors.New("session id is empty")
	}
	if strings.Contains(id, string(filepath.Separator)) || strings.Contains(id, "/") || strings.Contains(id, "\\") {
		return errors.New("invalid session id")
	}
	if strings.Contains(id, "..") {
		return errors.New("invalid session id")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}
