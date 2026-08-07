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
	"path/filepath"
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
	ID         string `json:"id"`
	Title      string `json:"title,omitempty"`
	Mtime      int64  `json:"mtime,omitempty"`
	ForkedFrom string `json:"forkedFrom,omitempty"`
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
	Goals          bool `json:"goals"`
	Plugins        bool `json:"plugins"`
	Panes          bool `json:"panes"`
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
	"compact", "context.controls", "inspect.prompt", "interrupt", "permission.reply", "question.reply",
	"rewind", "select.agent", "select.model", "set.autonomy", "set.effort",
	"set.fast", "set.permission_mode", "user.input",
	"workflow.start", "workflow.stop",
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	// Timeline is host-safe and derived from SessionDir JSONL (always available).
	c := capabilities{Live: s.hasLive(), Roots: s.opts.LiveHub != nil, Timeline: true, Sandbox: s.hasSandbox(), Diag: s.hasLive()}
	var skills []map[string]any
	if h := s.opts.Services; h != nil {
		c.Auth, c.Catalog, c.Settings, c.History = h.Auth != nil, h.Catalog != nil, h.Settings != nil, h.History != nil
		c.Files, c.Memory, c.Issues, c.Plans = h.Files != nil, h.Memory != nil, h.Issues != nil, h.Plans != nil
		c.Sessions = h.Sessions != nil
		// Workflow authoring is exposed via /v1/workflows* and /v1/workflow-drafts*.
		c.Workflows, c.WorkflowDrafts = h.Workflows != nil, h.WorkflowDrafts != nil
		c.Goals = h.Goals != nil
		// LSP status + diagnostics are exposed via /v1/lsp and /v1/diagnostics.
		c.LSP = h.LSP != nil
		// Permission explain/presets via /v1/permissions/* (#926).
		c.Permissions = h.Permissions != nil
		// MCP status/control is exposed via /v1/mcp*.
		c.MCP = h.MCP != nil
		// Plugin lifecycle + pane contributions via /v1/plugins* and /v1/panes* (#732).
		c.Plugins = h.Plugins != nil
		c.Panes = h.Panes != nil
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

// settingsResponse is the GET /v1/settings payload (host.UserDefaults wire form).
// Field names match ~/.strike/config JSON keys where applicable.
type settingsResponse struct {
	Provider                     string   `json:"provider,omitempty"`
	Model                        string   `json:"model,omitempty"`
	Agent                        string   `json:"agent,omitempty"`
	Effort                       string   `json:"effort,omitempty"`
	Mode                         string   `json:"mode,omitempty"`
	Sandbox                      string   `json:"sandbox,omitempty"`
	Notify                       string   `json:"notify,omitempty"`
	Autoupdate                   string   `json:"autoupdate,omitempty"`
	LeanCode                     string   `json:"leanCode,omitempty"`
	DeferTools                   string   `json:"deferTools,omitempty"`
	SessionWorktree              string   `json:"sessionWorktree,omitempty"`
	PermissionAutoApproveSeconds int      `json:"permissionAutoApproveSeconds"`
	PermissionAutoApproveExclude []string `json:"permissionAutoApproveExclude,omitempty"`
	MaxChildDepth                int      `json:"maxChildDepth"`
	Theme                        string   `json:"theme,omitempty"`
	VimMode                      string   `json:"vimMode,omitempty"`
	NanoMode                     string   `json:"nanoMode,omitempty"`
	MdReadMode                   string   `json:"mdReadMode,omitempty"`
	CompactionStrategy           string   `json:"compactionStrategy,omitempty"`
	CompactionModel              string   `json:"compactionModel,omitempty"`
	CompactionThreshold          float64  `json:"compactionThreshold,omitempty"`
	CompactionBuffer             int      `json:"compactionBuffer,omitempty"`
	KeepUserTurns                int      `json:"keepUserTurns,omitempty"`
	PruneProtectTokens           int      `json:"pruneProtectTokens,omitempty"`
	PruneMinimumTokens           int      `json:"pruneMinimumTokens,omitempty"`
	PruneKeepUserTurns           int      `json:"pruneKeepUserTurns,omitempty"`
	PruneProtectTools            []string `json:"pruneProtectTools,omitempty"`
}

// settingsPatchBody is the PATCH /v1/settings payload. Empty scalars leave the
// stored value unchanged (same contract as host.Settings). Compaction and
// auto-approve dials use the string vocabulary of host.CompactionDials /
// SaveAutoApproveDials so validation matches the TUI.
type settingsPatchBody struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Agent    string `json:"agent"`
	Effort   string `json:"effort"`
	Mode     string `json:"mode"`

	Theme   string `json:"theme"`
	Sandbox string `json:"sandbox"`
	// IKnow acknowledges yolo+sandbox-off (same gate as PATCH /v1/sandbox).
	IKnow           bool   `json:"iKnow"`
	Notify          string `json:"notify"`
	LeanCode        string `json:"leanCode"`
	DeferTools      string `json:"deferTools"`
	SessionWorktree string `json:"sessionWorktree"`
	Autoupdate      string `json:"autoupdate"`

	// permissionAutoApproveSeconds: off|0|1-60 (string dial, not int).
	PermissionAutoApproveSeconds string `json:"permissionAutoApproveSeconds"`
	// nil = leave exclude list unchanged; non-nil (incl. empty) replaces it.
	PermissionAutoApproveExclude *[]string `json:"permissionAutoApproveExclude"`
	// maxChildDepth: default|0|1-8.
	MaxChildDepth string `json:"maxChildDepth"`

	CompactionStrategy  string `json:"compactionStrategy"`
	CompactionModel     string `json:"compactionModel"`
	CompactionThreshold string `json:"compactionThreshold"`
	CompactionBuffer    string `json:"compactionBuffer"`
	KeepUserTurns       string `json:"keepUserTurns"`
	PruneProtectTokens  string `json:"pruneProtectTokens"`
	PruneMinimumTokens  string `json:"pruneMinimumTokens"`
	PruneKeepUserTurns  string `json:"pruneKeepUserTurns"`
	PruneProtectTools   string `json:"pruneProtectTools"`
}

func settingsFromDefaults(d host.UserDefaults) settingsResponse {
	return settingsResponse{
		Provider:                     d.Provider,
		Model:                        d.Model,
		Agent:                        d.Agent,
		Effort:                       d.Effort,
		Mode:                         d.PermissionMode,
		Sandbox:                      d.Sandbox,
		Notify:                       d.Notify,
		Autoupdate:                   d.Autoupdate,
		LeanCode:                     d.LeanCode,
		DeferTools:                   d.DeferTools,
		SessionWorktree:              d.SessionWorktree,
		PermissionAutoApproveSeconds: d.PermissionAutoApproveSeconds,
		PermissionAutoApproveExclude: append([]string(nil), d.PermissionAutoApproveExclude...),
		MaxChildDepth:                d.MaxChildDepth,
		Theme:                        d.Theme,
		VimMode:                      d.VimMode,
		NanoMode:                     d.NanoMode,
		MdReadMode:                   d.MdReadMode,
		CompactionStrategy:           d.CompactionStrategy,
		CompactionModel:              d.CompactionModel,
		CompactionThreshold:          d.CompactionThreshold,
		CompactionBuffer:             d.CompactionBuffer,
		KeepUserTurns:                d.KeepUserTurns,
		PruneProtectTokens:           d.PruneProtectTokens,
		PruneMinimumTokens:           d.PruneMinimumTokens,
		PruneKeepUserTurns:           d.PruneKeepUserTurns,
		PruneProtectTools:            append([]string(nil), d.PruneProtectTools...),
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Settings == nil {
		capabilityUnavailable(w, "settings")
		return
	}
	settings := s.opts.Services.Settings
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, settingsFromDefaults(settings.Defaults()))
		return
	}
	var body settingsPatchBody
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	// Sandbox dial uses the same yolo+off iKnow gate as PATCH /v1/sandbox.
	if body.Sandbox != "" {
		if err := s.saveSandboxDefault(body.Sandbox, body.IKnow, r); err != nil {
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
			return
		}
		body.Sandbox = "" // already persisted; avoid double-write in applySettingsPatch
	}
	if err := applySettingsPatch(settings, body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, settingsFromDefaults(settings.Defaults()))
}

// applySettingsPatch writes non-empty dial groups through host.Settings.
// Validation errors match TUI /settings (unknown tokens rejected without a
// successful write for that dial group). Groups are applied in order; a later
// failure may leave earlier groups persisted (same as sequential TUI saves).
func applySettingsPatch(settings host.Settings, body settingsPatchBody) error {
	if body.Provider != "" || body.Model != "" || body.Agent != "" || body.Effort != "" || body.Mode != "" {
		if err := settings.SaveDefaults(body.Provider, body.Model, body.Agent, body.Effort, body.Mode); err != nil {
			return err
		}
	}
	if body.Theme != "" {
		if err := settings.SaveTheme(body.Theme); err != nil {
			return err
		}
	}
	if body.Sandbox != "" || body.Notify != "" || body.LeanCode != "" || body.DeferTools != "" || body.SessionWorktree != "" || body.Autoupdate != "" {
		if err := settings.SaveConfigDials(body.Sandbox, body.Notify, body.LeanCode, body.DeferTools, body.SessionWorktree, body.Autoupdate); err != nil {
			return err
		}
	}
	if body.PermissionAutoApproveSeconds != "" || body.PermissionAutoApproveExclude != nil || body.MaxChildDepth != "" {
		if err := settings.SaveAutoApproveDials(body.PermissionAutoApproveSeconds, body.PermissionAutoApproveExclude, body.MaxChildDepth); err != nil {
			return err
		}
	}
	compaction := host.CompactionDials{
		Strategy:           body.CompactionStrategy,
		Model:              body.CompactionModel,
		Threshold:          body.CompactionThreshold,
		Buffer:             body.CompactionBuffer,
		KeepUserTurns:      body.KeepUserTurns,
		PruneProtectTokens: body.PruneProtectTokens,
		PruneMinimumTokens: body.PruneMinimumTokens,
		PruneKeepUserTurns: body.PruneKeepUserTurns,
		PruneProtectTools:  body.PruneProtectTools,
	}
	if compaction != (host.CompactionDials{}) {
		if err := settings.SaveCompactionDials(compaction); err != nil {
			return err
		}
	}
	return nil
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
			ID:         info.ID,
			Title:      info.Title,
			Mtime:      info.UpdatedAt.Unix(),
			ForkedFrom: info.ForkedFrom,
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
// Query: permission (required), pattern (optional; empty means "*"),
// preset (optional; dry-run alternate shipped preset without mutating session).
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
	preset := strings.TrimSpace(r.URL.Query().Get("preset"))
	var ex host.PermissionExplanation
	if preset != "" {
		ex = s.opts.Services.Permissions.ExplainPreset(perm, pattern, preset)
	} else {
		ex = s.opts.Services.Permissions.Explain(perm, pattern)
	}
	writeJSON(w, http.StatusOK, ex)
}

// handlePermissionDiff compares two shipped permission presets.
// Query: left and right preset ids (required).
func (s *Server) handlePermissionDiff(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Permissions == nil {
		capabilityUnavailable(w, "permissions")
		return
	}
	left := strings.TrimSpace(r.URL.Query().Get("left"))
	right := strings.TrimSpace(r.URL.Query().Get("right"))
	if left == "" || right == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "left and right preset ids are required"})
		return
	}
	d, err := s.opts.Services.Permissions.DiffPresets(left, right)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, d)
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

type hostImportBody struct {
	Path    string          `json:"path"`
	Replace bool            `json:"replace"`
	Data    json.RawMessage `json:"data"`
}

func (s *Server) requireMutable(w http.ResponseWriter) bool {
	if s.hasLive() {
		return true
	}
	writeJSON(w, http.StatusForbidden, opErrorResponse{Error: "read-only attach mode does not allow mutations"})
	return false
}

func (s *Server) memoryService(w http.ResponseWriter) host.Memory {
	if s.opts.Services == nil || s.opts.Services.Memory == nil {
		capabilityUnavailable(w, "memory")
		return nil
	}
	return s.opts.Services.Memory
}

func (s *Server) issuesService(w http.ResponseWriter) host.Issues {
	if s.opts.Services == nil || s.opts.Services.Issues == nil {
		capabilityUnavailable(w, "issues")
		return nil
	}
	return s.opts.Services.Issues
}

func (s *Server) writeHostExport(w http.ResponseWriter, filename string, export func(path string) error) {
	dir, err := os.MkdirTemp("", "strike-export-*")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, filename)
	if err := export(path); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) runHostImport(w http.ResponseWriter, r *http.Request, importFn func(path string, replace bool) (int, error)) (int, error) {
	var body hostImportBody
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return 0, err
	}
	path := strings.TrimSpace(body.Path)
	if path == "" && len(body.Data) == 0 {
		err := fmt.Errorf("path or data is required")
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return 0, err
	}
	if len(body.Data) > 0 {
		if !json.Valid(body.Data) {
			err := fmt.Errorf("data must be valid JSON")
			writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
			return 0, err
		}
		dir, err := os.MkdirTemp("", "strike-import-*")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
			return 0, err
		}
		defer os.RemoveAll(dir)
		path = filepath.Join(dir, "import.json")
		if err := os.WriteFile(path, body.Data, 0o600); err != nil {
			writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
			return 0, err
		}
	}
	n, err := importFn(path, body.Replace)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return 0, err
	}
	return n, nil
}

func (s *Server) handleMemoryPut(w http.ResponseWriter, r *http.Request) {
	mem := s.memoryService(w)
	if mem == nil {
		return
	}
	if !s.requireMutable(w) {
		return
	}
	key := strings.TrimSpace(r.PathValue("key"))
	if key == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "key is required"})
		return
	}
	var body struct {
		Value string   `json:"value"`
		Tags  []string `json:"tags"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if err := mem.Put(key, body.Value, body.Tags); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	entry, ok, err := mem.Get(key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusInternalServerError, opErrorResponse{Error: "memory entry missing after put"})
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	mem := s.memoryService(w)
	if mem == nil {
		return
	}
	if !s.requireMutable(w) {
		return
	}
	key := strings.TrimSpace(r.PathValue("key"))
	if key == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "key is required"})
		return
	}
	if err := mem.Delete(key); err != nil {
		status := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, opErrorResponse{Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMemoryExport(w http.ResponseWriter, r *http.Request) {
	mem := s.memoryService(w)
	if mem == nil {
		return
	}
	s.writeHostExport(w, "strike-memory.json", mem.Export)
}

func (s *Server) handleMemoryImport(w http.ResponseWriter, r *http.Request) {
	mem := s.memoryService(w)
	if mem == nil {
		return
	}
	if !s.requireMutable(w) {
		return
	}
	n, err := s.runHostImport(w, r, mem.Import)
	if err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"imported": n})
}

func (s *Server) handleIssueCreate(w http.ResponseWriter, r *http.Request) {
	issues := s.issuesService(w)
	if issues == nil {
		return
	}
	if !s.requireMutable(w) {
		return
	}
	var body struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "title is required"})
		return
	}
	item, err := issues.Create(body.Title, body.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) handleIssueClose(w http.ResponseWriter, r *http.Request) {
	issues := s.issuesService(w)
	if issues == nil {
		return
	}
	if !s.requireMutable(w) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 1 {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "id must be a positive integer"})
		return
	}
	item, err := issues.Close(id)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			status = http.StatusNotFound
		}
		writeJSON(w, status, opErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) handleIssuesExport(w http.ResponseWriter, r *http.Request) {
	issues := s.issuesService(w)
	if issues == nil {
		return
	}
	s.writeHostExport(w, "strike-issues.json", issues.Export)
}

func (s *Server) handleIssuesImport(w http.ResponseWriter, r *http.Request) {
	issues := s.issuesService(w)
	if issues == nil {
		return
	}
	if !s.requireMutable(w) {
		return
	}
	n, err := s.runHostImport(w, r, issues.Import)
	if err != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"imported": n})
}
