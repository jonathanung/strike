package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/pkg/redact"
)

// Apps are queues the daemon can fill. The web client
// (webui/src/apps.rs, docs/webui-apps.md) lists whatever queueApps returns.
// It does not ship a card the daemon did not register.

type appInfo struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Handoff bool   `json:"handoff"`
}

type appItem struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Subtitle  string   `json:"subtitle,omitempty"`
	Badge     string   `json:"badge,omitempty"`
	UpdatedAt string   `json:"updatedAt,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	URL       string   `json:"url,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	Number    int      `json:"number,omitempty"`
}

type appNote struct {
	Author string `json:"author,omitempty"`
	Body   string `json:"body,omitempty"`
	At     string `json:"at,omitempty"`
}

type appAction struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	NeedsBody bool   `json:"needsBody,omitempty"`
}

type appDetail struct {
	appItem
	Body     string      `json:"body"`
	Comments []appNote   `json:"comments,omitempty"`
	Actions  []appAction `json:"actions"`
}

// queueApp is one purpose-built queue. Add an implementation and register
// it from queueApps. Do not register an app whose data does not exist.
type queueApp interface {
	info() appInfo
	list(ctx context.Context, dir, kind, state string) (repo string, items []appItem, err error)
	get(ctx context.Context, dir, itemID string) (detail appDetail, err error)
	act(ctx context.Context, dir, itemID, action, body string) error
	handoff(ctx context.Context, dir, itemID string) (title, prompt string, err error)
}

type appError struct {
	status int
	msg    string
}

func (e *appError) Error() string { return e.msg }

func appBad(msg string) error  { return &appError{status: http.StatusBadRequest, msg: msg} }
func appFail(msg string) error { return &appError{status: http.StatusBadGateway, msg: msg} }

func (s *Server) queueApps() []queueApp {
	return []queueApp{githubTriageApp{}}
}

func (s *Server) appByID(id string) (queueApp, bool) {
	for _, app := range s.queueApps() {
		if app.info().ID == id {
			return app, true
		}
	}
	return nil, false
}

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	apps := s.queueApps()
	infos := make([]appInfo, 0, len(apps))
	for _, app := range apps {
		infos = append(infos, app.info())
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": infos})
}

func (s *Server) handleAppItems(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appByID(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "unknown app"})
		return
	}
	kind, state, err := normalizeQueueQuery(r.URL.Query().Get("kind"), r.URL.Query().Get("state"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	dir, err := s.appWorkDir()
	if err != nil {
		writeAppErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	repo, items, err := app.list(ctx, dir, kind, state)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	if items == nil {
		items = []appItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"repo": repo, "items": items})
}

func (s *Server) handleAppItem(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appByID(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "unknown app"})
		return
	}
	dir, err := s.appWorkDir()
	if err != nil {
		writeAppErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	detail, err := app.get(ctx, dir, r.PathValue("itemID"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	if detail.Actions == nil {
		detail.Actions = []appAction{}
	}
	writeJSON(w, http.StatusOK, detail)
}

type appActRequest struct {
	Action string `json:"action"`
	Body   string `json:"body,omitempty"`
}

func (s *Server) handleAppAct(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "server is read-only"})
		return
	}
	app, ok := s.appByID(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "unknown app"})
		return
	}
	var body appActRequest
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	dir, err := s.appWorkDir()
	if err != nil {
		writeAppErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := app.act(ctx, dir, r.PathValue("itemID"), body.Action, body.Body); err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAppHandoff(w http.ResponseWriter, r *http.Request) {
	if s.opts.ReadOnly {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "server is read-only"})
		return
	}
	app, ok := s.appByID(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "unknown app"})
		return
	}
	if s.opts.LiveHub == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "live session unavailable"})
		return
	}
	dir, err := s.appWorkDir()
	if err != nil {
		writeAppErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	title, prompt, err := app.handoff(ctx, dir, r.PathValue("itemID"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	created, err := s.opts.LiveHub.Create(ctx)
	if err != nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
		return
	}
	if strings.TrimSpace(title) != "" {
		s.opts.LiveHub.SetTitle(created.ID, title)
	}
	live := s.opts.LiveHub.LiveFor(created.ID)
	if live == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "live session unavailable"})
		return
	}
	if err := live.Submit(ctx, protocol.UserInput{Text: prompt}); err != nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
		return
	}
	if err := s.opts.LiveHub.Activate(created.ID); err != nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok":        true,
		"rootId":    created.ID,
		"sessionId": created.SessionID,
	})
}

func writeAppErr(w http.ResponseWriter, err error) {
	var ae *appError
	if errors.As(err, &ae) {
		writeJSON(w, ae.status, opErrorResponse{Error: redact.String(ae.msg)})
		return
	}
	writeJSON(w, http.StatusBadGateway, opErrorResponse{Error: redact.String(err.Error())})
}

func normalizeQueueQuery(kind, state string) (string, string, error) {
	kind = strings.TrimSpace(kind)
	state = strings.TrimSpace(state)
	if kind == "" {
		kind = "issue"
	}
	if state == "" {
		state = "open"
	}
	switch kind {
	case "issue", "pr", "all":
	default:
		return "", "", appBad("kind must be issue, pr, or all")
	}
	switch state {
	case "open", "closed":
	default:
		return "", "", appBad("state must be open or closed")
	}
	return kind, state, nil
}

func (s *Server) appWorkDir() (string, error) {
	dir := strings.TrimSpace(s.opts.WorkDir)
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", appFail("workspace directory is unknown")
		}
		dir = wd
	}
	if strings.ContainsRune(dir, 0) || strings.HasPrefix(dir, "-") {
		return "", appFail("workspace directory is invalid")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", appFail("workspace directory is invalid")
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", appFail("workspace directory is not available")
	}
	return abs, nil
}

func trimRunes(s string, n int) string {
	if n <= 0 || s == "" || utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for j := range s {
		if i == n {
			return s[:j] + "…"
		}
		i++
	}
	return s
}
