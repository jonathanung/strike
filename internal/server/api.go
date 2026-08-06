package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/session"
	"github.com/jonathanung/strike-cli/internal/version"
)

type sessionsResponse struct {
	Sessions []sessionListItem `json:"sessions"`
	LiveID   string            `json:"liveId,omitempty"`
}

type sessionListItem struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Mtime int64  `json:"mtime,omitempty"`
}

type agentsResponse struct {
	Agents []AgentInfo `json:"agents"`
}

type opErrorResponse struct {
	Error string `json:"error"`
}

type opOKResponse struct {
	OK bool `json:"ok"`
}

const maxHTTPPayload = 8 << 20

type capabilities struct {
	Live           bool `json:"live"`
	Auth           bool `json:"auth"`
	Catalog        bool `json:"catalog"`
	Settings       bool `json:"settings"`
	History        bool `json:"history"`
	Files          bool `json:"files"`
	Memory         bool `json:"memory"`
	Issues         bool `json:"issues"`
	Plans          bool `json:"plans"`
	Sessions       bool `json:"sessions"`
	Roots          bool `json:"roots"`
	Providers      bool `json:"providers"`
	ProjectInit    bool `json:"projectInit"`
	MCP            bool `json:"mcp"`
	LSP            bool `json:"lsp"`
	Telemetry      bool `json:"telemetry"`
	Workflows      bool `json:"workflows"`
	WorkflowDrafts bool `json:"workflowDrafts"`
	// Timeline is the redacted run-timeline snapshot/export surface
	// (GET /v1/sessions/{id}/timeline[+ /export]). Always on when SessionDir is set.
	Timeline bool `json:"timeline"`
	// Permissions is true when host.Services.Permissions is set (explain + presets).
	Permissions bool `json:"permissions"`
	Sandbox     bool `json:"sandbox"`
	// Diag is true when a live engine can build a prompt/config diagnostic bundle.
	Diag bool `json:"diag"`
}

type bootstrapResponse struct {
	Version      string           `json:"version"`
	AuthRequired bool             `json:"authRequired"`
	AttachOnly   bool             `json:"attachOnly"`
	Capabilities capabilities     `json:"capabilities"`
	Status       *StatusSnapshot  `json:"status,omitempty"`
	Agents       []AgentInfo      `json:"agents"`
	Skills       []map[string]any `json:"skills"`
	ProtocolOps  []string         `json:"protocolOps"`
}

var browserProtocolOps = []string{
	"compact", "inspect.prompt", "interrupt", "permission.reply", "question.reply",
	"rewind", "select.agent", "select.model", "set.autonomy", "set.effort",
	"set.fast", "set.permission_mode", "user.input",
	"workflow.start", "workflow.stop",
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	// Timeline is host-safe and derived from SessionDir JSONL (always available).
	c := capabilities{Live: s.hasLive(), Roots: s.opts.LiveHub != nil, Timeline: true, Sandbox: s.hasSandbox()}
	var skills []map[string]any
	if h := s.opts.Services; h != nil {
		c.Auth, c.Catalog, c.Settings, c.History = h.Auth != nil, h.Catalog != nil, h.Settings != nil, h.History != nil
		c.Files, c.Memory, c.Issues, c.Plans = h.Files != nil, h.Memory != nil, h.Issues != nil, h.Plans != nil
		c.Sessions = h.Sessions != nil
		// Workflow authoring is exposed via /v1/workflows* and /v1/workflow-drafts*.
		c.Workflows, c.WorkflowDrafts = h.Workflows != nil, h.WorkflowDrafts != nil
		// LSP status + diagnostics are exposed via /v1/lsp and /v1/diagnostics.
		c.LSP = h.LSP != nil
		// Permission explain/presets via /v1/permissions/* (#926).
		c.Permissions = h.Permissions != nil
		// MCP status/control is exposed via /v1/mcp*.
		c.MCP = h.MCP != nil
		// Capabilities describe browser surfaces, not merely host interfaces.
		// Roots, custom providers, project init, and telemetry remain false
		// until this server exposes their service operations.
		for _, skill := range h.Skills {
			skills = append(skills, map[string]any{"name": skill.Name, "description": skill.Description})
		}
	}
	var status *StatusSnapshot
	var agents []AgentInfo
	// Bootstrap uses a non-tracking resolve to avoid marking active on page load.
	if s.opts.LiveHub != nil {
		live := s.opts.LiveHub.LiveFor(rootParam(r))
		if live != nil {
			v := live.Status()
			status, agents = &v, live.Agents()
		}
	} else if s.opts.Live != nil {
		v := s.opts.Live.Status()
		status, agents = &v, s.opts.Live.Agents()
	}
	var protocolOps []string
	if s.hasLive() {
		protocolOps = browserProtocolOps
	}
	writeJSON(w, http.StatusOK, bootstrapResponse{Version: version.Version, AuthRequired: s.opts.Auth, AttachOnly: !s.hasLive(), Capabilities: c, Status: status, Agents: agents, Skills: skills, ProtocolOps: protocolOps})
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Auth == nil {
		capabilityUnavailable(w, "auth")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": s.opts.Services.Auth.Statuses()})
}

func (s *Server) handleAuthKey(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Auth == nil {
		capabilityUnavailable(w, "auth")
		return
	}
	var body struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if strings.TrimSpace(body.Provider) == "" || strings.TrimSpace(body.Key) == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "provider and key are required"})
		return
	}
	if err := s.opts.Services.Auth.SetAPIKey(body.Provider, body.Key); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Auth == nil {
		capabilityUnavailable(w, "auth")
		return
	}
	provider := strings.TrimSpace(r.PathValue("provider"))
	if provider == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "provider is required"})
		return
	}
	if err := s.opts.Services.Auth.Logout(provider); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Catalog == nil {
		capabilityUnavailable(w, "catalog")
		return
	}
	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var (
		models []host.ModelInfo
		err    error
	)
	if provider != "" {
		models, err = s.opts.Services.Catalog.Models(ctx, provider)
	} else {
		// No provider filter: list models from every authenticated provider
		// (same multi-provider set as the TUI /model picker).
		providers := authenticatedProviders(s.opts.Services.Auth)
		if len(providers) == 0 {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "provider is required (no authenticated providers)"})
			return
		}
		models, err = s.opts.Services.Catalog.ModelsForProviders(ctx, providers)
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// authenticatedProviders returns Authed provider names from host.Auth.
func authenticatedProviders(auth host.Auth) []string {
	if auth == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, s := range auth.Statuses() {
		name := strings.TrimSpace(s.Name)
		if name == "" || !s.Authed || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.History == nil {
		capabilityUnavailable(w, "history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": s.opts.Services.History.Entries()})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Settings == nil {
		capabilityUnavailable(w, "settings")
		return
	}
	var body struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		Agent    string `json:"agent"`
		Effort   string `json:"effort"`
		Mode     string `json:"mode"`
		// Sandbox is the OS sandbox dial default (off|read-only|workspace-write).
		// Empty leaves the stored default unchanged. Prefer PATCH /v1/sandbox when
		// also sending iKnow for the yolo+off safety gate.
		Sandbox string `json:"sandbox"`
		// IKnow is the --i-know equivalent when sandbox is set to off while
		// permission mode default (or live) is yolo.
		IKnow bool `json:"iKnow"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if err := s.opts.Services.Settings.SaveDefaults(body.Provider, body.Model, body.Agent, body.Effort, body.Mode); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if strings.TrimSpace(body.Sandbox) != "" {
		if err := s.saveSandboxDefault(body.Sandbox, body.IKnow, r); err != nil {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) hasSandbox() bool {
	if s.opts.Sandbox != nil {
		return true
	}
	// Live roots may carry sandbox chrome even when Options.Sandbox is unset.
	if s.opts.LiveHub != nil {
		if live := s.opts.LiveHub.Active(); live != nil && strings.TrimSpace(live.Status().Sandbox) != "" {
			return true
		}
	}
	if s.opts.Live != nil && strings.TrimSpace(s.opts.Live.Status().Sandbox) != "" {
		return true
	}
	return false
}

func (s *Server) handleSandboxGet(w http.ResponseWriter, r *http.Request) {
	if !s.hasSandbox() {
		capabilityUnavailable(w, "sandbox")
		return
	}
	writeJSON(w, http.StatusOK, s.sandboxSnapshot(r))
}

func (s *Server) handleSandboxPatch(w http.ResponseWriter, r *http.Request) {
	if !s.hasSandbox() {
		capabilityUnavailable(w, "sandbox")
		return
	}
	if s.opts.Services == nil || s.opts.Services.Settings == nil {
		capabilityUnavailable(w, "settings")
		return
	}
	var body struct {
		Mode  string `json:"mode"`
		IKnow bool   `json:"iKnow"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if strings.TrimSpace(body.Mode) == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "mode is required (off|read-only|workspace-write)"})
		return
	}
	if err := s.saveSandboxDefault(body.Mode, body.IKnow, r); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.sandboxSnapshot(r))
}

// sandboxModes is the canonical dial list for cockpit UIs.
var sandboxModes = []string{"off", "read-only", "workspace-write"}

func (s *Server) sandboxSnapshot(r *http.Request) SandboxSnapshot {
	out := SandboxSnapshot{
		Modes:            append([]string(nil), sandboxModes...),
		CanChangeDefault: s.opts.Services != nil && s.opts.Services.Settings != nil,
		Note:             "Active mode is session-scoped (config/CLI at start). Changing the default applies to new sessions only — same as TUI /settings.",
	}
	if base := s.opts.Sandbox; base != nil {
		out.Mode = base.Mode
		out.Backend = base.Backend
		out.Available = base.Available
		out.NetworkAllow = append([]string(nil), base.NetworkAllow...)
		out.Explain = base.Explain
	}
	// Prefer live root chrome when present (per-root seed via Live.SetSandbox).
	if live := s.liveForRequest(r); live != nil {
		st := live.Status()
		if st.Sandbox != "" {
			out.Mode = st.Sandbox
			out.Backend = st.SandboxBackend
			out.Available = st.SandboxAvailable
			out.NetworkAllow = append([]string(nil), st.NetworkAllow...)
		}
		if expl := live.SandboxExplain(); expl != "" {
			out.Explain = expl
		}
		out.PermissionMode = st.PermissionMode
	}
	if out.Mode == "" {
		out.Mode = "workspace-write"
	}
	if s.opts.Services != nil && s.opts.Services.Settings != nil {
		d := s.opts.Services.Settings.Defaults()
		if d.Sandbox != "" {
			out.DefaultMode = d.Sandbox
		} else {
			out.DefaultMode = "workspace-write"
		}
		if out.PermissionMode == "" && d.PermissionMode != "" {
			out.PermissionMode = d.PermissionMode
		}
	}
	if out.Explain == "" {
		out.Explain = "sandbox explain unavailable (no compiled profile)\nmode: " + out.Mode
	}
	return out
}

func (s *Server) liveForRequest(r *http.Request) *Live {
	if s.opts.LiveHub != nil {
		return s.opts.LiveHub.LiveFor(rootParam(r))
	}
	return s.opts.Live
}

// saveSandboxDefault persists the OS sandbox dial default. When mode is off and
// the effective permission posture is yolo, iKnow must be true (--i-know gate).
func (s *Server) saveSandboxDefault(mode string, iKnow bool, r *http.Request) error {
	mode = strings.TrimSpace(mode)
	canonical := ""
	for _, want := range sandboxModes {
		if strings.EqualFold(mode, want) {
			canonical = want
			break
		}
	}
	// Accept common aliases via a small normalize (match config ParseMode tokens).
	if canonical == "" {
		switch strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(mode, "_", "-"), " ", "")) {
		case "off", "none", "disable", "disabled", "false", "0", "no":
			canonical = "off"
		case "read-only", "readonly", "ro", "read":
			canonical = "read-only"
		case "workspace-write", "workspacewrite", "write", "ws-write", "workspace":
			canonical = "workspace-write"
		default:
			return fmt.Errorf("unknown sandbox %q (want %s)", mode, strings.Join(sandboxModes, "|"))
		}
	}
	// Gate when live posture or persisted default is yolo (startup CheckYoloSandbox).
	yolo := false
	if live := s.liveForRequest(r); live != nil {
		yolo = strings.EqualFold(strings.TrimSpace(live.Status().PermissionMode), "yolo")
	}
	if !yolo && s.opts.Services != nil && s.opts.Services.Settings != nil {
		yolo = strings.EqualFold(strings.TrimSpace(s.opts.Services.Settings.Defaults().PermissionMode), "yolo")
	}
	if canonical == "off" && yolo && !iKnow {
		return fmt.Errorf("permissionMode yolo with sandbox off requires iKnow (OS isolation disabled)")
	}
	if s.opts.Services == nil || s.opts.Services.Settings == nil {
		return fmt.Errorf("settings capability unavailable on this host")
	}
	return s.opts.Services.Settings.SaveConfigDials(canonical, "", "", "", "", "")
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	live := s.resolveLive(w, r)
	if live == nil {
		http.Error(w, "live session unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, live.Status())
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	live := s.resolveLive(w, r)
	if live == nil {
		writeJSON(w, http.StatusOK, agentsResponse{Agents: nil})
		return
	}
	writeJSON(w, http.StatusOK, agentsResponse{Agents: live.Agents()})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	items, err := listSessionFiles(s.opts.SessionDir)
	if err != nil {
		http.Error(w, "session list unavailable", http.StatusInternalServerError)
		return
	}
	liveID := ""
	if s.opts.LiveHub != nil {
		live := s.opts.LiveHub.Active()
		if live != nil {
			liveID = live.SessionID()
		}
	} else if s.opts.Live != nil {
		liveID = s.opts.Live.SessionID()
	}
	writeJSON(w, http.StatusOK, sessionsResponse{Sessions: items, LiveID: liveID})
}

func listSessionFiles(dir string) ([]sessionListItem, error) {
	mgr := session.NewManager(dir)
	list, err := mgr.List()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]sessionListItem, 0, len(list))
	for _, info := range list {
		if info.ParentSessionID != "" {
			continue // roots only in switcher
		}
		out = append(out, sessionListItem{
			ID:    info.ID,
			Title: info.Title,
			Mtime: info.UpdatedAt.Unix(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mtime != out[j].Mtime {
			return out[i].Mtime > out[j].Mtime
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Server) handleSessionChildren(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Sessions == nil {
		capabilityUnavailable(w, "sessions")
		return
	}
	items, err := s.opts.Services.Sessions.Children(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
}

func (s *Server) handleSessionFork(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Sessions == nil {
		capabilityUnavailable(w, "sessions")
		return
	}
	item, err := s.opts.Services.Sessions.Fork(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) handleSessionRename(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Sessions == nil {
		capabilityUnavailable(w, "sessions")
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	id := r.PathValue("id")
	item, err := s.opts.Services.Sessions.Rename(id, body.Title)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	// Keep ACTIVE rail identity in sync when the renamed session is live.
	if s.opts.LiveHub != nil {
		s.opts.LiveHub.SetTitle(id, body.Title)
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Sessions == nil {
		capabilityUnavailable(w, "sessions")
		return
	}
	force := r.URL.Query().Get("force") == "true"
	if err := s.opts.Services.Sessions.Delete(r.PathValue("id"), force); err != nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Files == nil {
		capabilityUnavailable(w, "files")
		return
	}
	items, err := s.opts.Services.Files.ListDir(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": items})
}

type changedFileItem struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Diff    string `json:"diff"`
}

func (s *Server) handleChangedFiles(w http.ResponseWriter, r *http.Request) {
	cwd := strings.TrimSpace(s.currentCWD(r))
	if cwd == "" {
		capabilityUnavailable(w, "changed files")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "diff", "--numstat", "--").Output()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "changed files unavailable: " + err.Error()})
		return
	}
	var items []changedFileItem
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		added, _ := strconv.Atoi(fields[0])
		deleted, _ := strconv.Atoi(fields[1])
		path := strings.TrimSpace(fields[2])
		if path == "" || strings.Contains(path, "\x00") {
			continue
		}
		diff, _ := exec.CommandContext(ctx, "git", "-C", cwd, "diff", "--", path).Output()
		items = append(items, changedFileItem{Path: path, Added: added, Deleted: deleted, Diff: string(diff)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": items})
}

func (s *Server) currentCWD(r *http.Request) string {
	if s.opts.LiveHub != nil {
		if live := s.opts.LiveHub.LiveFor(rootParam(r)); live != nil {
			return live.Status().CWD
		}
	}
	if s.opts.Live != nil {
		return s.opts.Live.Status().CWD
	}
	return ""
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Files == nil {
		capabilityUnavailable(w, "files")
		return
	}
	item, err := s.opts.Services.Files.ReadScoped(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Memory == nil {
		capabilityUnavailable(w, "memory")
		return
	}
	items, err := s.opts.Services.Memory.List(r.URL.Query().Get("tag"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": items})
}

func (s *Server) handleIssues(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Issues == nil {
		capabilityUnavailable(w, "issues")
		return
	}
	items, err := s.opts.Services.Issues.List(r.URL.Query().Get("status"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": items})
}

// handlePermissionExplain returns last-match-wins detail for a sample tool call.
// Query: permission (required), pattern (optional; empty means "*").
// Host-safe DTO only — no TUI types cross this boundary.
func (s *Server) handlePermissionExplain(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Permissions == nil {
		capabilityUnavailable(w, "permissions")
		return
	}
	perm := strings.TrimSpace(r.URL.Query().Get("permission"))
	if perm == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "permission is required"})
		return
	}
	pattern := strings.TrimSpace(r.URL.Query().Get("pattern"))
	ex := s.opts.Services.Permissions.Explain(perm, pattern)
	writeJSON(w, http.StatusOK, ex)
}

// handlePermissionPresets lists shipped named permission rulesets.
func (s *Server) handlePermissionPresets(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Permissions == nil {
		capabilityUnavailable(w, "permissions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"presets": s.opts.Services.Permissions.Presets()})
}

func capabilityUnavailable(w http.ResponseWriter, name string) {
	writeJSON(w, http.StatusNotImplemented, opErrorResponse{Error: name + " capability unavailable on this host"})
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxHTTPPayload)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return ensureEOF(dec)
}

func (s *Server) handleOps(w http.ResponseWriter, r *http.Request) {
	live := s.resolveLive(w, r)
	if live == nil {
		http.Error(w, "live session unavailable", http.StatusServiceUnavailable)
		return
	}
	var env protocol.OpEnvelope
	r.Body = http.MaxBytesReader(w, r.Body, maxHTTPPayload)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "invalid op envelope: " + err.Error()})
		return
	}
	if err := ensureEOF(dec); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	op, err := env.Decode()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := live.Submit(ctx, op); err != nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func ensureEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("request body must contain one JSON value")
		}
		return fmt.Errorf("invalid trailing data: %w", err)
	}
	return nil
}

func (s *Server) handleLiveEvents(w http.ResponseWriter, r *http.Request) {
	live := s.resolveLive(w, r)
	if live == nil {
		http.Error(w, "live session unavailable", http.StatusServiceUnavailable)
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
	id := live.SessionID()
	path := session.LogPath(s.opts.SessionDir, id)
	var offset int64
	if st, err := os.Stat(path); err == nil {
		boundary := st.Size()
		var err error
		offset, err = s.writeEventsRange(ctx, w, flusher, path, 0, boundary)
		if err != nil {
			return
		}
	}

	ticker := time.NewTicker(s.opts.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := s.writeEventsFrom(ctx, w, flusher, path, offset)
			if err == nil && n > offset {
				offset = n
			}
		}
	}
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	live := s.resolveLive(w, r)
	if live == nil {
		http.Error(w, "live session unavailable", http.StatusServiceUnavailable)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && !originAllowed(origin, s.opts.Expose) {
		http.Error(w, "websocket origin not allowed", http.StatusForbidden)
		return
	}
	ws, err := upgradeWebSocket(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer ws.Close()

	ctx := r.Context()
	// Hello: push current status as a synthetic control message.
	statusEnv := map[string]any{
		"type": "status",
		"data": live.Status(),
	}
	if b, err := json.Marshal(statusEnv); err == nil {
		_ = ws.WriteText(string(b))
	}

	// Replay to a fixed byte boundary, then tail strictly after that boundary.
	id := live.SessionID()
	path := session.LogPath(s.opts.SessionDir, id)
	var offset int64
	if st, err := os.Stat(path); err == nil {
		boundary := st.Size()
		var err error
		offset, err = s.writeWSRange(ctx, ws, path, 0, boundary)
		if err != nil {
			return
		}
	}

	errCh := make(chan error, 1)
	go func() {
		for {
			text, err := ws.ReadText()
			if err != nil {
				errCh <- err
				return
			}
			var env protocol.OpEnvelope
			if err := json.Unmarshal([]byte(text), &env); err != nil {
				_ = ws.WriteText(`{"type":"error","data":{"message":"invalid json"}}`)
				continue
			}
			// Allow status request control message.
			if env.Type == "status.get" {
				b, _ := json.Marshal(map[string]any{"type": "status", "data": live.Status()})
				_ = ws.WriteText(string(b))
				continue
			}
			op, err := env.Decode()
			if err != nil {
				msg, _ := json.Marshal(map[string]any{"type": "error", "data": map[string]string{"message": err.Error()}})
				_ = ws.WriteText(string(msg))
				continue
			}
			submitCtx, submitCancel := context.WithTimeout(ctx, 5*time.Second)
			err = live.Submit(submitCtx, op)
			submitCancel()
			if err != nil {
				msg, _ := json.Marshal(map[string]any{"type": "error", "data": map[string]string{"message": err.Error()}})
				_ = ws.WriteText(string(msg))
			}
		}
	}()

	ticker := time.NewTicker(s.opts.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-errCh:
			return
		case <-ticker.C:
			st, err := os.Stat(path)
			if err != nil || st.Size() <= offset {
				continue
			}
			boundary := st.Size()
			n, err := s.writeWSRange(ctx, ws, path, offset, boundary)
			if err != nil {
				return
			}
			offset = n
		}
	}
}

type wsTextWriter interface {
	WriteText(string) error
}

func (s *Server) writeWSRange(ctx context.Context, ws wsTextWriter, path string, offset, boundary int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	if boundary <= offset {
		return offset, nil
	}
	reader := bufio.NewReaderSize(io.LimitReader(f, boundary-offset), 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return offset, err
		}
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			offset += int64(len(line))
			payload := bytes.TrimSpace(line)
			if len(payload) > 0 && json.Valid(payload) && !isSessionLogHeader(payload) {
				if err := ws.WriteText(string(payload)); err != nil {
					return offset, err
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return offset, nil
			}
			return offset, err
		}
	}
}

// --- root API ---

type rootsResponse struct {
	Roots    []RootSummary `json:"roots"`
	ActiveID string        `json:"activeId,omitempty"`
}

func (s *Server) handleRoots(w http.ResponseWriter, r *http.Request) {
	if s.opts.LiveHub == nil {
		http.Error(w, "multi-root unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, rootsResponse{
		Roots:    s.opts.LiveHub.List(),
		ActiveID: s.opts.LiveHub.ActiveID(),
	})
}

func (s *Server) handleRootCreate(w http.ResponseWriter, r *http.Request) {
	if s.opts.LiveHub == nil {
		http.Error(w, "multi-root unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := s.opts.LiveHub.Create(ctx)
	if err != nil {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) handleRootActivate(w http.ResponseWriter, r *http.Request) {
	if s.opts.LiveHub == nil {
		http.Error(w, "multi-root unavailable", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.opts.LiveHub.Activate(id); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}

func (s *Server) handleRootResume(w http.ResponseWriter, r *http.Request) {
	if s.opts.LiveHub == nil {
		http.Error(w, "multi-root unavailable", http.StatusServiceUnavailable)
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("id"))
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result, err := s.opts.LiveHub.Resume(ctx, sessionID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleRootClose(w http.ResponseWriter, r *http.Request) {
	if s.opts.LiveHub == nil {
		http.Error(w, "multi-root unavailable", http.StatusServiceUnavailable)
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "root id is empty"})
		return
	}
	if s.opts.LiveHub.LiveFor(id) == nil {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: fmt.Sprintf("root %q is not active", id)})
		return
	}
	s.opts.LiveHub.Remove(id)
	writeJSON(w, http.StatusOK, opOKResponse{OK: true})
}
