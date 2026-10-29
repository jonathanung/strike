package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jonathanung/strike/harness/sandbox"
	"github.com/jonathanung/strike/internal/frontend/server/terminal"
)

func (s *Server) terminalManager() *terminal.Manager {
	if s == nil {
		return nil
	}
	return s.terms
}

// terminalAllowed reports whether this listener may expose a shell.
// Loopback is on. Any other bind requires Options.TerminalRemote, which
// New rejects unless Auth is also set.
func (s *Server) terminalAllowed() bool {
	if s == nil {
		return false
	}
	if IsLocalhostBind(s.opts.Addr) {
		return true
	}
	return s.opts.TerminalRemote && s.opts.Auth
}

func (s *Server) rejectTerminal(w http.ResponseWriter) bool {
	if s.terminalAllowed() {
		return false
	}
	writeJSON(w, http.StatusForbidden, opErrorResponse{
		Error: "terminal is disabled on this non-loopback listener",
		Code:  "terminal_remote_disabled",
	})
	return true
}

func terminalStreamID(path string) (string, bool) {
	const prefix = "/v1/terminals/"
	const suffix = "/stream"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

// terminalAuthorized allows a normal bearer or cookie, and also a terminal
// stream that presents a ticket subprotocol. A presented ticket is consumed
// even when the cookie already matched, so a logged handshake cannot be
// replayed. A mismatch or replay is unauthorized. Query-string tokens are
// not accepted.
func (s *Server) terminalAuthorized(r *http.Request) bool {
	if terminalTicketHeader(r) != "" {
		return s.terminalTicketOK(r)
	}
	return s.authorized(r)
}

// terminalTicketOK is the only /v1 bypass of the bearer check, and only for
// the terminal stream. The ticket is single-use, short-lived, and bound to
// one terminal. Query-string tokens are not accepted.
func (s *Server) terminalTicketOK(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	id, ok := terminalStreamID(r.URL.Path)
	if !ok || s.terms == nil {
		return false
	}
	if r.URL.Query().Get("token") != "" || r.URL.Query().Get("ticket") != "" {
		return false
	}
	return s.terms.ConsumeTicket(id, terminalTicketHeader(r))
}

func terminalTicketHeader(r *http.Request) string {
	for _, part := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "ticket.") {
			return strings.TrimPrefix(part, "ticket.")
		}
	}
	return ""
}

func (s *Server) handleTerminalsList(w http.ResponseWriter, r *http.Request) {
	if s.rejectTerminal(w) {
		return
	}
	if s.terms == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "terminal unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"terminals": s.terms.List()})
}

type terminalCreateBody struct {
	WorkspaceID  string `json:"workspaceId"`
	SessionID    string `json:"sessionId"`
	CWD          string `json:"cwd"`
	Cols         int    `json:"cols"`
	Rows         int    `json:"rows"`
	OnSessionEnd string `json:"onSessionEnd"`
}

func (s *Server) handleTerminalCreate(w http.ResponseWriter, r *http.Request) {
	if s.rejectTerminal(w) {
		return
	}
	if s.terminalShellDenied() {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "read-only attach does not allow a shell", Code: "read_only"})
		return
	}
	if s.opts.Workspaces == nil || s.terms == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "workspaces unavailable"})
		return
	}
	var body terminalCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid json"})
		return
	}
	ws, ok := s.lookupWorkspace(strings.TrimSpace(body.WorkspaceID))
	if !ok {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "no workspace", Code: "no_workspace"})
		return
	}
	sessionID := strings.TrimSpace(body.SessionID)
	// Workspace plan and the resolved live session are both locks. A
	// non-plan live mode must not clear a workspace plan lock, and an
	// omitted id resolves to the active root.
	if strings.TrimSpace(ws.PermissionMode) == "plan" || s.terminalPermissionMode(sessionID) == "plan" {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "plan mode does not allow a shell", Code: "plan_mode"})
		return
	}
	root, err := terminal.ResolveCWD(ws.Path, "")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	policy, err := s.sandboxForTerminal(sessionID, root)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: err.Error()})
		return
	}
	info, err := s.terms.Create(terminal.CreateSpec{
		WorkspaceID:   ws.ID,
		WorkspacePath: ws.Path,
		CWD:           body.CWD,
		SessionID:     sessionID,
		Cols:          body.Cols,
		Rows:          body.Rows,
		OnSessionEnd:  body.OnSessionEnd,
		Sandbox:       &policy,
	})
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "unavailable") || strings.Contains(err.Error(), "limit") {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, opErrorResponse{Error: err.Error()})
		return
	}
	s.auditOpOK(r, "http", "terminal.create", sessionID)
	writeJSON(w, http.StatusCreated, info)
}

func (s *Server) lookupWorkspace(id string) (workspacePath, bool) {
	list, active := s.opts.Workspaces.List()
	if id == "" {
		id = active
	}
	if id == "" {
		return workspacePath{}, false
	}
	for _, w := range list {
		if w.ID == id {
			return workspacePath{ID: w.ID, Path: w.Path, PermissionMode: w.PermissionMode}, true
		}
	}
	return workspacePath{}, false
}

type workspacePath struct {
	ID             string
	Path           string
	PermissionMode string
}

func (s *Server) terminalPermissionMode(sessionID string) string {
	live := s.liveForTerminal(sessionID)
	if live == nil {
		return ""
	}
	return strings.TrimSpace(live.Status().PermissionMode)
}

func (s *Server) liveForTerminal(sessionID string) *Live {
	if s.opts.LiveHub != nil {
		return s.opts.LiveHub.LiveFor(sessionID)
	}
	if s.opts.Live == nil {
		return nil
	}
	if sessionID == "" || s.opts.Live.SessionID() == sessionID {
		return s.opts.Live
	}
	return nil
}

// terminalShellDenied refuses a new shell on --read-only and on attach-only,
// which never loads a sandbox dial. A zero policy is not treated as sandbox off
// unless TerminalSandboxLoaded says a live serve produced it.
func (s *Server) terminalShellDenied() bool {
	if s == nil {
		return true
	}
	if s.opts.ReadOnly {
		return true
	}
	return s.opts.TerminalSandboxFor == nil && !s.opts.TerminalSandboxLoaded
}

// sandboxForTerminal compiles the OS policy for this workspace. A missing
// live engine with a non-off dial fails closed instead of starting an
// unsandboxed shell from the startup copy.
func (s *Server) sandboxForTerminal(sessionID, workDir string) (sandbox.Policy, error) {
	if s.opts.TerminalSandboxFor != nil {
		p, ok := s.opts.TerminalSandboxFor(sessionID, workDir)
		if !ok {
			if !s.opts.TerminalSandboxLoaded || s.opts.TerminalSandbox.Mode != sandbox.ModeOff {
				return sandbox.Policy{}, errors.New("OS sandbox is required for this shell and is unavailable")
			}
			p = s.opts.TerminalSandbox
		}
		p.WorkDir = workDir
		return p, nil
	}
	p := s.opts.TerminalSandbox
	p.WorkDir = workDir
	return p, nil
}

func (s *Server) handleTerminalDelete(w http.ResponseWriter, r *http.Request) {
	if s.rejectTerminal(w) {
		return
	}
	if s.terms == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "terminal unavailable"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if _, ok := s.terms.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "terminal not found"})
		return
	}
	s.terms.Remove(id)
	s.auditOpOK(r, "http", "terminal.kill", "")
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleTerminalResize(w http.ResponseWriter, r *http.Request) {
	if s.rejectTerminal(w) {
		return
	}
	if s.terminalShellDenied() {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "read-only attach does not allow a shell", Code: "read_only"})
		return
	}
	if s.terms == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "terminal unavailable"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var body struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid json"})
		return
	}
	info, err := s.terms.Resize(id, body.Cols, body.Rows)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleTerminalTicket(w http.ResponseWriter, r *http.Request) {
	if s.rejectTerminal(w) {
		return
	}
	if s.terms == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "terminal unavailable"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	tok, exp, err := s.terms.MintTicket(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ticket":    tok,
		"expiresAt": exp.UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleTerminalStream(w http.ResponseWriter, r *http.Request) {
	if s.rejectTerminal(w) {
		return
	}
	if r.URL.Query().Get("token") != "" || r.URL.Query().Get("ticket") != "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.terminalShellDenied() {
		writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "read-only attach does not allow a shell", Code: "read_only"})
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && !originAllowed(origin) {
		http.Error(w, "websocket origin not allowed", http.StatusForbidden)
		return
	}
	if s.terms == nil {
		writeJSON(w, http.StatusServiceUnavailable, opErrorResponse{Error: "terminal unavailable"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if _, ok := s.terms.Get(id); !ok {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "terminal not found"})
		return
	}
	ws, err := upgradeWebSocket(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer ws.Close()

	chunk, pos, info, err := s.terms.Read(id, 0)
	if err != nil {
		return
	}
	hello := map[string]any{
		"type":            "hello",
		"id":              info.ID,
		"cwd":             info.CWD,
		"cols":            info.Cols,
		"rows":            info.Rows,
		"owner":           info.Owner,
		"onSessionEnd":    info.OnSessionEnd,
		"sharedWithAgent": info.SharedWithAgent,
		"sameDirectory":   info.SameDirectory,
		"agentBusy":       info.AgentBusy,
		"agentCwd":        info.AgentCWD,
		"permissionMode":  info.PermissionMode,
		"sandboxApplied":  info.SandboxApplied,
		"alive":           info.Alive,
		"exitReason":      info.ExitReason,
		"scrollback":      base64.StdEncoding.EncodeToString(chunk),
		"encoding":        "base64",
	}
	if b, err := json.Marshal(hello); err == nil {
		_ = ws.WriteText(string(b))
	}
	if !info.Alive {
		_ = writeTerminalExit(ws, info.ExitReason)
		return
	}

	ctx := r.Context()
	errCh := make(chan struct{}, 1)
	go func() {
		for {
			text, err := ws.ReadText()
			if err != nil {
				errCh <- struct{}{}
				return
			}
			var msg struct {
				Type string `json:"type"`
				Data string `json:"data"`
				Cols int    `json:"cols"`
				Rows int    `json:"rows"`
			}
			if err := json.Unmarshal([]byte(text), &msg); err != nil {
				continue
			}
			switch msg.Type {
			case "input":
				if len(msg.Data) > 16*1024 {
					continue
				}
				_ = s.terms.Write(id, []byte(msg.Data))
			case "resize":
				_, _ = s.terms.Resize(id, msg.Cols, msg.Rows)
			}
		}
	}()

	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	statusEvery := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-errCh:
			return
		case <-ticker.C:
			chunk, next, info, err := s.terms.Read(id, pos)
			if err != nil {
				return
			}
			pos = next
			if len(chunk) > 0 {
				payload, _ := json.Marshal(map[string]any{
					"type":     "output",
					"encoding": "base64",
					"data":     base64.StdEncoding.EncodeToString(chunk),
				})
				if err := ws.WriteText(string(payload)); err != nil {
					return
				}
			}
			statusEvery++
			if statusEvery%30 == 0 {
				st, _ := json.Marshal(map[string]any{
					"type":            "status",
					"sharedWithAgent": info.SharedWithAgent,
					"sameDirectory":   info.SameDirectory,
					"agentBusy":       info.AgentBusy,
					"agentCwd":        info.AgentCWD,
					"permissionMode":  info.PermissionMode,
					"cwd":             info.CWD,
					"alive":           info.Alive,
				})
				_ = ws.WriteText(string(st))
			}
			if !info.Alive {
				_ = writeTerminalExit(ws, info.ExitReason)
				return
			}
		}
	}
}

func writeTerminalExit(ws interface{ WriteText(string) error }, reason string) error {
	if reason == "" {
		reason = terminal.ReasonExited
	}
	b, err := json.Marshal(map[string]any{"type": "exit", "reason": reason})
	if err != nil {
		return err
	}
	return ws.WriteText(string(b))
}
