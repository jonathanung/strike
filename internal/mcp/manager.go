package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/tool"
)

// Status is one configured server's runtime state for /mcp and diagnostics.
type Status struct {
	Name      string
	Command   string // non-secret endpoint label (command or URL)
	Transport string // stdio|http
	State     string // "up", "down", "error", "disabled"
	ToolCount int
	Error     string // non-secret error summary when down/error
	Tools     []string
}

// Manager owns configured MCP servers for a session.
type Manager struct {
	mu       sync.Mutex
	clients  map[string]session
	tools    map[string][]string // server -> strike tool names
	errs     map[string]string
	cfgs     map[string]ServerConfig
	disabled map[string]bool
	reg      *tool.Registry
}

// NewManager returns an empty manager.
func NewManager() *Manager {
	return &Manager{
		clients:  make(map[string]session),
		tools:    make(map[string][]string),
		errs:     make(map[string]string),
		cfgs:     make(map[string]ServerConfig),
		disabled: make(map[string]bool),
	}
}

// StartAll starts each server, lists tools, and registers bridge tools on reg.
// Failures are recorded per-server and do not abort other servers.
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

	names := make([]string, 0, len(tools))
	for _, t := range tools {
		bridge := newBridge(client, t)
		reg.Register(bridge)
		names = append(names, bridge.Name())
	}

	m.mu.Lock()
	m.clients[cfg.Name] = client
	m.tools[cfg.Name] = names
	delete(m.errs, cfg.Name)
	m.mu.Unlock()

	// Watch for unexpected exit: mark down (tools error cleanly via client.Closed).
	go m.watch(cfg.Name, client)
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
	if m.disabled[name] {
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
			Name:      name,
			Command:   cfg.displayEndpoint(),
			Transport: cfg.transport(),
			Tools:     append([]string(nil), m.tools[name]...),
		}
		st.ToolCount = len(st.Tools)
		if m.disabled[name] {
			st.State = "disabled"
			st.Error = "disabled"
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
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	upper := strings.ToUpper(msg)
	lower := strings.ToLower(msg)
	if strings.Contains(upper, "KEY=") || strings.Contains(upper, "TOKEN=") ||
		strings.Contains(lower, "bearer ") || strings.Contains(lower, "authorization") ||
		strings.Contains(lower, "api-key") || strings.Contains(lower, "api_key") {
		return "start failed"
	}
	return msg
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
