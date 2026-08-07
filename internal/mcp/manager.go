package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/admission"
	"github.com/jonathanung/strike-cli/internal/secret"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// Status is one configured server's runtime state for /mcp and diagnostics.
type Status struct {
	Name      string
	Command   string // non-secret endpoint label (command or URL)
	Transport string // stdio|http
	State     string // "up", "down", "error", "disabled", "quarantined"
	ToolCount int
	Error     string // non-secret error summary when down/error/quarantined
	Tools     []string
	// Caps is a short capability summary (tools,prompts,resources).
	Caps string
	// Admission is the last admission action (allow|warn|block|quarantine).
	Admission string
	// AdmissionReason is a short operator-visible reason (redact-safe).
	AdmissionReason string
}

// Manager owns configured MCP servers for a session.
type Manager struct {
	mu       sync.Mutex
	clients  map[string]session
	tools    map[string][]string // server -> strike tool names
	errs     map[string]string
	cfgs     map[string]ServerConfig
	disabled map[string]bool
	// quarantined servers stay connected but tools are not bound.
	quarantined map[string]bool
	admitAction map[string]string
	admitReason map[string]string
	reg         *tool.Registry
	policy      admission.Policy
	// onVerdict is optional; called after each admission decision (tests / timeline).
	onVerdict func(admission.Verdict)
}

// NewManager returns an empty manager.
func NewManager() *Manager {
	return &Manager{
		clients:     make(map[string]session),
		tools:       make(map[string][]string),
		errs:        make(map[string]string),
		cfgs:        make(map[string]ServerConfig),
		disabled:    make(map[string]bool),
		quarantined: make(map[string]bool),
		admitAction: make(map[string]string),
		admitReason: make(map[string]string),
	}
}

// SetAdmissionPolicy installs the register-time admission policy. Zero policy
// allows all (no matrix → Decide treats missing as allow). Call before StartAll.
func (m *Manager) SetAdmissionPolicy(pol admission.Policy) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.policy = pol
	m.mu.Unlock()
}

// SetAdmissionHook registers a callback invoked after each admission verdict
// (including allow). Used to emit timeline/session events.
func (m *Manager) SetAdmissionHook(fn func(admission.Verdict)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.onVerdict = fn
	m.mu.Unlock()
}

// StartAll starts each server, lists tools, runs admission, and registers
// bridge tools on reg when the verdict binds tools. Failures are recorded
// per-server and do not abort other servers.
func (m *Manager) StartAll(ctx context.Context, servers []ServerConfig, reg *tool.Registry) {
	if m == nil || reg == nil {
		return
	}
	m.mu.Lock()
	m.reg = reg
	m.mu.Unlock()
	for _, cfg := range servers {
		m.startOne(ctx, cfg, reg)
	}
}

func (m *Manager) startOne(ctx context.Context, cfg ServerConfig, reg *tool.Registry) {
	m.mu.Lock()
	m.cfgs[cfg.Name] = cfg
	delete(m.disabled, cfg.Name)
	delete(m.quarantined, cfg.Name)
	delete(m.admitAction, cfg.Name)
	delete(m.admitReason, cfg.Name)
	m.mu.Unlock()

	client, err := Start(ctx, cfg)
	if err != nil {
		m.mu.Lock()
		m.errs[cfg.Name] = redactErr(err)
		m.mu.Unlock()
		return
	}

	listCtx, cancel := context.WithTimeout(ctx, defaultInitTimeout)
	tools, err := client.ListTools(listCtx)
	cancel()
	if err != nil {
		_ = client.Close()
		m.mu.Lock()
		m.errs[cfg.Name] = redactErr(err)
		m.mu.Unlock()
		return
	}

	// Admission scan before tools bind into the registry.
	verdict := m.admit(cfg, tools)
	m.recordVerdict(cfg.Name, verdict)
	if m.onVerdict != nil {
		m.onVerdict(verdict)
	}

	switch verdict.Action {
	case admission.ActionBlock:
		_ = client.Close()
		m.mu.Lock()
		m.errs[cfg.Name] = "admission blocked: " + verdict.Reason
		m.mu.Unlock()
		return
	case admission.ActionQuarantine:
		// Keep session for diagnostics but do not bind tools.
		m.mu.Lock()
		m.clients[cfg.Name] = client
		m.tools[cfg.Name] = nil
		m.quarantined[cfg.Name] = true
		m.errs[cfg.Name] = "admission quarantined: " + verdict.Reason
		m.mu.Unlock()
		go m.watch(cfg.Name, client)
		return
	}

	names := make([]string, 0, len(tools)+4)
	if client.Caps().Tools {
		for _, t := range tools {
			bridge := newBridge(client, t)
			reg.Register(bridge)
			names = append(names, bridge.Name())
		}
	}
	// Typed prompt/resource surface tools when capabilities allow.
	names = append(names, registerSurfaceTools(reg, client, client.Caps())...)

	m.mu.Lock()
	m.clients[cfg.Name] = client
	m.tools[cfg.Name] = names
	delete(m.errs, cfg.Name)
	m.mu.Unlock()

	// Dynamic catalog refresh without restarting Strike.
	client.OnNotification(func(method string, _ json.RawMessage) {
		m.handleCatalogNotification(cfg.Name, method)
	})

	// Watch for unexpected exit: mark down (tools error cleanly via client.Closed).
	go m.watch(cfg.Name, client)
}

func (m *Manager) admit(cfg ServerConfig, tools []toolInfo) admission.Verdict {
	m.mu.Lock()
	pol := m.policy
	m.mu.Unlock()
	sub := admission.MCPSubject{
		Name:      cfg.Name,
		Transport: cfg.transport(),
		Endpoint:  cfg.displayEndpoint(),
		Tools:     make([]admission.MCPTool, 0, len(tools)),
	}
	for _, t := range tools {
		sub.Tools = append(sub.Tools, admission.MCPTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return admission.AdmitMCP(pol, sub)
}

func (m *Manager) recordVerdict(name string, v admission.Verdict) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.admitAction[name] = string(v.Action)
	m.admitReason[name] = v.Reason
}

func (m *Manager) watch(name string, client session) {
	for !client.Closed() {
		time.Sleep(200 * time.Millisecond)
	}
	m.mu.Lock()
	if m.clients[name] == client {
		if !m.disabled[name] {
			m.errs[name] = "server exited"
		}
	}
	m.mu.Unlock()
}

// handleCatalogNotification refreshes tools/prompts/resources on list_changed.
func (m *Manager) handleCatalogNotification(name, method string) {
	if m == nil {
		return
	}
	switch method {
	case notifyToolsListChanged, notifyPromptsListChanged, notifyResourcesListChanged:
		// coalesce: refresh full surface for this server
		m.refreshServerCatalog(name)
	default:
		// ignore unknown notifications (malformed or future)
	}
}

// refreshServerCatalog re-lists capabilities and rebinds registry tools in place.
func (m *Manager) refreshServerCatalog(name string) {
	m.mu.Lock()
	client := m.clients[name]
	reg := m.reg
	disabled := m.disabled[name] || m.quarantined[name]
	m.mu.Unlock()
	if client == nil || reg == nil || disabled || client.Closed() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultInitTimeout)
	defer cancel()

	caps := client.Caps()
	var tools []toolInfo
	var err error
	if caps.Tools {
		tools, err = client.ListTools(ctx)
		if err != nil {
			m.mu.Lock()
			m.errs[name] = redactErr(err)
			m.mu.Unlock()
			return
		}
	}

	// Unregister previous names then rebind.
	m.mu.Lock()
	oldNames := append([]string(nil), m.tools[name]...)
	m.mu.Unlock()
	if reg != nil {
		for _, n := range oldNames {
			reg.Unregister(n)
		}
	}

	names := make([]string, 0, len(tools)+4)
	if caps.Tools {
		for _, t := range tools {
			bridge := newBridge(client, t)
			reg.Register(bridge)
			names = append(names, bridge.Name())
		}
	}
	names = append(names, registerSurfaceTools(reg, client, caps)...)

	m.mu.Lock()
	// Only apply if same client still live.
	if m.clients[name] == client {
		m.tools[name] = names
		delete(m.errs, name)
	}
	m.mu.Unlock()
}

// Retry reconnects servers. Empty name retries every configured server that is
// not currently up (including disabled). Named retry clears disabled.
func (m *Manager) Retry(ctx context.Context, name string) error {
	if m == nil {
		return fmt.Errorf("mcp: no manager")
	}
	m.mu.Lock()
	reg := m.reg
	if reg == nil {
		m.mu.Unlock()
		return fmt.Errorf("mcp: not started")
	}
	var names []string
	if name != "" {
		name = strings.TrimSpace(name)
		if _, ok := m.cfgs[name]; !ok {
			m.mu.Unlock()
			return fmt.Errorf("mcp: unknown server %q", name)
		}
		names = []string{name}
	} else {
		for n, cfg := range m.cfgs {
			_ = cfg
			if m.isUpLocked(n) {
				continue
			}
			names = append(names, n)
		}
		sort.Strings(names)
	}
	cfgs := make([]ServerConfig, 0, len(names))
	for _, n := range names {
		cfgs = append(cfgs, m.cfgs[n])
	}
	m.mu.Unlock()

	if len(cfgs) == 0 {
		return nil
	}
	var first error
	for _, cfg := range cfgs {
		if err := m.reconnect(ctx, cfg, reg); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (m *Manager) isUpLocked(name string) bool {
	if m.disabled[name] || m.quarantined[name] {
		return false
	}
	if _, bad := m.errs[name]; bad {
		return false
	}
	c, ok := m.clients[name]
	return ok && c != nil && !c.Closed()
}

func (m *Manager) reconnect(ctx context.Context, cfg ServerConfig, reg *tool.Registry) error {
	m.detach(cfg.Name, reg)
	m.startOne(ctx, cfg, reg)
	m.mu.Lock()
	errMsg := m.errs[cfg.Name]
	up := m.isUpLocked(cfg.Name)
	m.mu.Unlock()
	if !up {
		if errMsg == "" {
			errMsg = "retry failed"
		}
		return fmt.Errorf("mcp %s: %s", cfg.Name, errMsg)
	}
	return nil
}

// Disable stops a server and unregisters its tools. Status becomes "disabled".
func (m *Manager) Disable(name string) error {
	if m == nil {
		return fmt.Errorf("mcp: no manager")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("mcp: disable requires a server name")
	}
	m.mu.Lock()
	if _, ok := m.cfgs[name]; !ok {
		m.mu.Unlock()
		return fmt.Errorf("mcp: unknown server %q", name)
	}
	reg := m.reg
	m.disabled[name] = true
	m.errs[name] = "disabled"
	m.mu.Unlock()

	m.detach(name, reg)
	m.mu.Lock()
	m.disabled[name] = true
	m.errs[name] = "disabled"
	m.mu.Unlock()
	return nil
}

// detach closes the live session and unregisters tools (caller handles flags).
func (m *Manager) detach(name string, reg *tool.Registry) {
	m.mu.Lock()
	client := m.clients[name]
	toolNames := append([]string(nil), m.tools[name]...)
	delete(m.clients, name)
	delete(m.tools, name)
	delete(m.quarantined, name)
	m.mu.Unlock()

	if reg != nil && len(toolNames) > 0 {
		reg.Unregister(toolNames...)
	}
	if client != nil {
		_ = client.Close()
	}
}

// Statuses returns stable-ordered server status snapshots.
func (m *Manager) Statuses() []Status {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	names := make([]string, 0, len(m.cfgs))
	for name := range m.cfgs {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]Status, 0, len(names))
	for _, name := range names {
		cfg := m.cfgs[name]
		st := Status{
			Name:            name,
			Command:         cfg.displayEndpoint(),
			Transport:       cfg.transport(),
			Tools:           append([]string(nil), m.tools[name]...),
			Admission:       m.admitAction[name],
			AdmissionReason: m.admitReason[name],
		}
		st.ToolCount = len(st.Tools)
		if c := m.clients[name]; c != nil {
			st.Caps = formatCaps(c.Caps())
		}
		if m.disabled[name] {
			st.State = "disabled"
			st.Error = "disabled"
			out = append(out, st)
			continue
		}
		if m.quarantined[name] {
			st.State = "quarantined"
			st.Error = m.errs[name]
			if st.Error == "" {
				st.Error = "admission quarantined"
			}
			out = append(out, st)
			continue
		}
		if errMsg, bad := m.errs[name]; bad {
			st.State = "error"
			st.Error = errMsg
			if c, ok := m.clients[name]; ok && c.Closed() {
				st.State = "down"
			}
		} else if c, ok := m.clients[name]; ok {
			if c.Closed() {
				st.State = "down"
				st.Error = "server exited"
			} else {
				st.State = "up"
			}
		} else {
			st.State = "down"
			st.Error = "not started"
		}
		out = append(out, st)
	}
	return out
}

// Close shuts down every server session.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	clients := make([]session, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clients = make(map[string]session)
	m.mu.Unlock()

	var first error
	for _, c := range clients {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// ConfigsFromMap converts config map entries to ServerConfig values.
// Invalid entries are skipped. Names are sorted for stability.
func ConfigsFromMap(servers map[string]ServerConfigFields, workDir string) []ServerConfig {
	if len(servers) == 0 {
		return nil
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ServerConfig, 0, len(names))
	for _, name := range names {
		f := servers[name]
		name = strings.TrimSpace(name)
		if name == "" || !validServerName(name) {
			continue
		}
		transport := normalizeTransport(f.Type)
		// Infer http when url is set and type omitted.
		if strings.TrimSpace(f.Type) == "" && strings.TrimSpace(f.URL) != "" {
			transport = TransportHTTP
		}
		cfg := ServerConfig{
			Name:      name,
			Transport: transport,
			Command:   strings.TrimSpace(f.Command),
			Args:      append([]string(nil), f.Args...),
			Env:       copyEnv(f.Env),
			WorkDir:   workDir,
			URL:       strings.TrimSpace(f.URL),
			Headers:   copyEnv(f.Headers),
			OAuth:     cloneOAuth(f.OAuth),
		}
		switch transport {
		case TransportHTTP:
			if cfg.URL == "" {
				continue
			}
		default:
			if cfg.Command == "" {
				continue
			}
		}
		out = append(out, cfg)
	}
	return out
}

// ServerConfigFields is the JSON-facing server entry (without Name).
type ServerConfigFields struct {
	Type    string            `json:"type,omitempty"` // stdio (default) | http
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	OAuth   *OAuthConfig      `json:"oauth,omitempty"`
}

func cloneOAuth(in *OAuthConfig) *OAuthConfig {
	if in == nil {
		return nil
	}
	cp := *in
	return &cp
}

func copyEnv(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if strings.TrimSpace(k) == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func validServerName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for i, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		if r == '_' || r == '-' {
			if i == 0 {
				return false
			}
			continue
		}
		return false
	}
	// Must start with a letter.
	r := rune(name[0])
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// redactErr returns a short error string without env values, headers, or tokens.
func redactErr(err error) string {
	return secret.RedactError(err)
}

// FormatStatuses is a multi-line human summary for /mcp.
func FormatStatuses(statuses []Status) string {
	if len(statuses) == 0 {
		return "no MCP servers configured (add servers in ~/.strike/mcp.jsonc or ./.strike/mcp.jsonc)"
	}
	var b strings.Builder
	for i, st := range statuses {
		if i > 0 {
			b.WriteByte('\n')
		}
		transport := st.Transport
		if transport == "" {
			transport = TransportStdio
		}
		fmt.Fprintf(&b, "%s  %s  %s  tools=%d", st.Name, st.State, transport, st.ToolCount)
		if st.Command != "" {
			fmt.Fprintf(&b, "  %s", st.Command)
		}
		if st.Admission != "" && st.Admission != "allow" {
			fmt.Fprintf(&b, "  admission=%s", st.Admission)
		}
		if st.Error != "" && st.State != "disabled" {
			fmt.Fprintf(&b, "  (%s)", st.Error)
		}
		if len(st.Tools) > 0 {
			b.WriteByte('\n')
			b.WriteString("  ")
			b.WriteString(strings.Join(st.Tools, ", "))
		}
	}
	b.WriteString("\n(/mcp retry [name]  |  /mcp disable <name>)")
	return b.String()
}

func formatCaps(c ServerCaps) string {
	var parts []string
	if c.Tools {
		parts = append(parts, "tools")
	}
	if c.Prompts {
		parts = append(parts, "prompts")
	}
	if c.Resources {
		parts = append(parts, "resources")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ",")
}
