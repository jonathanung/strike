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

	"github.com/jonathanung/strike-cli/internal/session"
	"github.com/jonathanung/strike-cli/internal/version"
)

// Options configures the attach server.
type Options struct {
	// Addr is the bind address (host:port). Default "127.0.0.1:8787".
	Addr string
	// Token is required on all routes except /health. Empty rejects New.
	Token string
	// SessionDir is the JSONL sessions directory. Default session.DefaultDir().
	SessionDir string
	// Static is optional override for the attach page FS (tests). Nil uses embed.
	Static fs.FS
	// PollInterval is how often the SSE tail checks for new JSONL bytes.
	// Default 200ms. Tests may lower it.
	PollInterval time.Duration
	// Live is an optional engine bridge for composer/ops/status. Nil keeps
	// read-only attach (JSONL SSE only).
	Live *Live
}

// Server is an HTTP server for session attach and optional live cockpit.
type Server struct {
	opts   Options
	mux    *http.ServeMux
	http   *http.Server
	static fs.FS
}

// New validates options and builds a Server. Does not listen.
func New(opts Options) (*Server, error) {
	opts.Token = strings.TrimSpace(opts.Token)
	if opts.Token == "" {
		return nil, errors.New("server: token is required")
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
	s.mux.HandleFunc("GET /v1/sessions/{id}/events", s.handleSessionEvents)
	s.mux.HandleFunc("GET /v1/sessions", s.handleSessions)
	s.mux.HandleFunc("GET /v1/status", s.handleStatus)
	s.mux.HandleFunc("GET /v1/agents", s.handleAgents)
	s.mux.HandleFunc("POST /v1/ops", s.handleOps)
	s.mux.HandleFunc("GET /v1/live/events", s.handleLiveEvents)
	s.mux.HandleFunc("GET /v1/ws", s.handleWS)
	s.mux.HandleFunc("GET /{$}", s.handleAttach)
	s.mux.HandleFunc("GET /attach", s.handleAttach)
}

func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.applyCORS(w, r)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Public: health + attach shell. Session event streams require a token.
		if requiresToken(r.URL.Path) && !s.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requiresToken(path string) bool {
	return strings.HasPrefix(path, "/v1/")
}

func (s *Server) authorized(r *http.Request) bool {
	want := s.opts.Token
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		got := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
	}
	if got := strings.TrimSpace(r.URL.Query().Get("token")); got != "" {
		return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
	}
	return false
}

func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if originAllowed(origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	}
}

func originAllowed(origin string) bool {
	if origin == "" {
		return false
	}
	// Strict localhost / 127.0.0.1 / [::1] with any port.
	o := strings.ToLower(origin)
	switch {
	case strings.HasPrefix(o, "http://localhost"):
		return originHostIsLocal(o)
	case strings.HasPrefix(o, "http://127.0.0.1"):
		return originHostIsLocal(o)
	case strings.HasPrefix(o, "http://[::1]"):
		return originHostIsLocal(o)
	case strings.HasPrefix(o, "https://localhost"):
		return originHostIsLocal(o)
	case strings.HasPrefix(o, "https://127.0.0.1"):
		return originHostIsLocal(o)
	case strings.HasPrefix(o, "https://[::1]"):
		return originHostIsLocal(o)
	default:
		return false
	}
}

func originHostIsLocal(origin string) bool {
	// Strip scheme.
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
	h = strings.Trim(h, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
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
	data, err := fs.ReadFile(s.static, "index.html")
	if err != nil {
		http.Error(w, "attach page missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
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
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}

	reader := bufio.NewReaderSize(f, 64*1024)
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
