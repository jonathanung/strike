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
	Command   string
	State     string // "up", "down", "error"
	ToolCount int
	Error     string // non-secret error summary when down/error
	Tools     []string
}

// Manager owns configured MCP servers for a session.
type Manager struct {
	mu      sync.Mutex
	clients map[string]*Client
	tools   map[string][]string // server -> strike tool names
	errs    map[string]string
	cfgs    map[string]ServerConfig
}

// NewManager returns an empty manager.
func NewManager() *Manager {
	return &Manager{
		clients: make(map[string]*Client),
		tools:   make(map[string][]string),
		errs:    make(map[string]string),
		cfgs:    make(map[string]ServerConfig),
	}
}

// StartAll starts each server, lists tools, and registers bridge tools on reg.
// Failures are recorded per-server and do not abort other servers.
func (m *Manager) StartAll(ctx context.Context, servers []ServerConfig, reg *tool.Registry) {
	if m == nil || reg == nil {
		return
	}
	for _, cfg := range servers {
		m.startOne(ctx, cfg, reg)
	}
}

func (m *Manager) startOne(ctx context.Context, cfg ServerConfig, reg *tool.Registry) {
	m.mu.Lock()
	m.cfgs[cfg.Name] = cfg
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

func (m *Manager) watch(name string, client *Client) {
	for !client.Closed() {
		time.Sleep(200 * time.Millisecond)
	}
	m.mu.Lock()
	if m.clients[name] == client {
		m.errs[name] = "server exited"
	}
	m.mu.Unlock()
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
			Name:    name,
			Command: cfg.Command,
			Tools:   append([]string(nil), m.tools[name]...),
		}
		st.ToolCount = len(st.Tools)
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

// Close shuts down every server process.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clients = make(map[string]*Client)
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
// Invalid entries (empty command) are skipped. Names are sorted for stability.
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
		if name == "" || strings.TrimSpace(f.Command) == "" {
			continue
		}
		if !validServerName(name) {
			continue
		}
		out = append(out, ServerConfig{
			Name:    name,
			Command: strings.TrimSpace(f.Command),
			Args:    append([]string(nil), f.Args...),
			Env:     copyEnv(f.Env),
			WorkDir: workDir,
		})
	}
	return out
}

// ServerConfigFields is the JSON-facing server entry (without Name).
type ServerConfigFields struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
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

// redactErr returns a short error string without env values or paths that may
// embed secrets. Prefer the error type/message as-is when short.
func redactErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	// Strip obvious KEY=value fragments.
	if i := strings.Index(msg, "="); i > 0 {
		// keep generic message when it looks like leaked env
		if strings.Contains(strings.ToUpper(msg), "KEY=") || strings.Contains(strings.ToUpper(msg), "TOKEN=") {
			return "start failed"
		}
	}
	return msg
}

// FormatStatuses is a multi-line human summary for /mcp.
func FormatStatuses(statuses []Status) string {
	if len(statuses) == 0 {
		return "no MCP servers configured (add mcp.servers in ~/.strike/config or ./.strike/config)"
	}
	var b strings.Builder
	for i, st := range statuses {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s  %s  tools=%d", st.Name, st.State, st.ToolCount)
		if st.Command != "" {
			fmt.Fprintf(&b, "  %s", st.Command)
		}
		if st.Error != "" {
			fmt.Fprintf(&b, "  (%s)", st.Error)
		}
		if len(st.Tools) > 0 {
			b.WriteByte('\n')
			b.WriteString("  ")
			b.WriteString(strings.Join(st.Tools, ", "))
		}
	}
	return b.String()
}
