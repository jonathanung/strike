package lsp

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status is one configured language server's runtime state.
type Status struct {
	Name       string
	Command    string
	State      string // "up", "down", "error", "disabled"
	Extensions []string
	Error      string
	OpenDocs   int
}

// ServerConfig is one language server (stdio command) plus the extensions it owns.
type ServerConfig struct {
	// Name is the config key.
	Name string
	// Command is the executable (required).
	Command string
	Args    []string
	// Env overlays process environment; values are never logged.
	Env map[string]string
	// WorkDir is the subprocess working directory (empty = inherit).
	WorkDir string
	// RootDir is the workspace root sent in initialize (file URI).
	RootDir string
	// Extensions are file extensions this server handles (e.g. ".go", "ts").
	Extensions []string
}

// ServerConfigFields is the JSON-facing server entry (without Name).
type ServerConfigFields struct {
	Command    string            `json:"command,omitempty"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Extensions []string          `json:"extensions,omitempty"`
}

// Manager owns configured language servers for a session.
// Dead servers degrade to no diagnostics; failures never panic the host.
type Manager struct {
	mu       sync.Mutex
	clients  map[string]*Client
	errs     map[string]string
	cfgs     map[string]ServerConfig
	disabled map[string]bool
	// extIndex maps normalized extension → server name (first registration wins).
	extIndex map[string]string
	rootDir  string

	// diagnostics is a manager-level cache (uri path → diags) updated by clients.
	diagMu      sync.Mutex
	diagnostics map[string][]Diagnostic // keyed by absolute path
}

// NewManager returns an empty manager. rootDir is the workspace root for initialize.
func NewManager(rootDir string) *Manager {
	return &Manager{
		clients:     make(map[string]*Client),
		errs:        make(map[string]string),
		cfgs:        make(map[string]ServerConfig),
		disabled:    make(map[string]bool),
		extIndex:    make(map[string]string),
		rootDir:     rootDir,
		diagnostics: make(map[string][]Diagnostic),
	}
}

// StartAll starts each server. Failures are recorded per-server and do not abort others.
func (m *Manager) StartAll(ctx context.Context, servers []ServerConfig) {
	if m == nil {
		return
	}
	for _, cfg := range servers {
		m.startOne(ctx, cfg)
	}
}

func (m *Manager) startOne(ctx context.Context, cfg ServerConfig) {
	cfg = normalizeConfig(cfg)
	if cfg.Name == "" || cfg.Command == "" {
		return
	}
	if cfg.RootDir == "" {
		cfg.RootDir = m.rootDir
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = m.rootDir
	}

	m.mu.Lock()
	m.cfgs[cfg.Name] = cfg
	delete(m.disabled, cfg.Name)
	for _, ext := range cfg.Extensions {
		if _, taken := m.extIndex[ext]; !taken {
			m.extIndex[ext] = cfg.Name
		}
	}
	m.mu.Unlock()

	client, err := Start(ctx, cfg)
	if err != nil {
		m.mu.Lock()
		m.errs[cfg.Name] = redactErr(err)
		m.mu.Unlock()
		return
	}

	name := cfg.Name
	client.onDiagnostic = func(uri string, diags []Diagnostic) {
		m.cacheDiagnostics(uri, diags)
	}

	m.mu.Lock()
	// Replace any prior client for this name.
	old := m.clients[cfg.Name]
	m.clients[cfg.Name] = client
	delete(m.errs, cfg.Name)
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	go m.watch(name, client)
}

func (m *Manager) cacheDiagnostics(uri string, diags []Diagnostic) {
	path := URIToPath(uri)
	if path == "" {
		return
	}
	m.diagMu.Lock()
	if len(diags) == 0 {
		delete(m.diagnostics, path)
	} else {
		cp := make([]Diagnostic, len(diags))
		copy(cp, diags)
		m.diagnostics[path] = cp
	}
	m.diagMu.Unlock()
}

func (m *Manager) watch(name string, client *Client) {
	for !client.Closed() {
		time.Sleep(200 * time.Millisecond)
	}
	m.mu.Lock()
	stillCurrent := m.clients[name] == client
	if stillCurrent {
		if !m.disabled[name] {
			m.errs[name] = "server exited"
		}
	}
	m.mu.Unlock()
	// Crash isolation: drop cached diagnostics from a dead server so callers
	// never see stale errors after the process exits.
	if stillCurrent {
		m.clearClientDiagnostics(client)
	}
}

// clearClientDiagnostics removes manager-level cache entries for docs the
// client had open (and any URI still held on the client store).
func (m *Manager) clearClientDiagnostics(client *Client) {
	if m == nil || client == nil {
		return
	}
	paths := make(map[string]struct{})
	client.docMu.Lock()
	for uri := range client.openDocs {
		if p := URIToPath(uri); p != "" {
			paths[p] = struct{}{}
		}
	}
	client.docMu.Unlock()
	client.diagMu.Lock()
	for uri := range client.diagnostics {
		if p := URIToPath(uri); p != "" {
			paths[p] = struct{}{}
		}
		delete(client.diagnostics, uri)
	}
	client.diagMu.Unlock()

	m.diagMu.Lock()
	for p := range paths {
		delete(m.diagnostics, p)
	}
	m.diagMu.Unlock()
}

// NotifyFile drives didOpen/didChange/didClose from a file tool mutation.
// deleted closes the document; otherwise content is the full new text.
// Safe on nil manager, unknown extensions, and dead servers (no-op).
func (m *Manager) NotifyFile(ctx context.Context, absPath, content string, deleted bool) {
	if m == nil || absPath == "" {
		return
	}
	// Never let LSP failures escape to the tool path.
	defer func() { _ = recover() }()

	client := m.clientForPath(absPath)
	if client == nil {
		if deleted {
			m.diagMu.Lock()
			delete(m.diagnostics, absPath)
			m.diagMu.Unlock()
		}
		return
	}
	if deleted {
		_ = client.DidClose(ctx, absPath)
		return
	}
	_ = client.DidOpenOrChange(ctx, absPath, content)
}

// Diagnostics returns the latest diagnostics for an absolute path.
// Empty when no server, server dead, or no publishDiagnostics yet.
// Never returns stale diagnostics from a dead language server.
func (m *Manager) Diagnostics(absPath string) []Diagnostic {
	if m == nil || absPath == "" {
		return nil
	}
	// Live client only — do not serve manager cache when the server is down
	// (crash isolation: dead LS → no diagnostics).
	c := m.clientForPath(absPath)
	if c == nil || c.Closed() {
		return nil
	}
	if diags := c.Diagnostics(absPath); len(diags) > 0 {
		return diags
	}
	// Manager cache is updated by the same client callback; use it when the
	// client store was cleared but the notification already landed here.
	m.diagMu.Lock()
	defer m.diagMu.Unlock()
	src := m.diagnostics[absPath]
	if len(src) == 0 {
		return nil
	}
	out := make([]Diagnostic, len(src))
	copy(out, src)
	return out
}

// AllDiagnostics returns a copy of diagnostics from live servers only,
// keyed by absolute path.
func (m *Manager) AllDiagnostics() map[string][]Diagnostic {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	live := make([]*Client, 0, len(m.clients))
	for name, c := range m.clients {
		if m.disabled[name] || c == nil || c.Closed() {
			continue
		}
		if _, bad := m.errs[name]; bad && c.Closed() {
			continue
		}
		live = append(live, c)
	}
	m.mu.Unlock()

	out := make(map[string][]Diagnostic)
	for _, c := range live {
		for path, diags := range c.AllDiagnostics() {
			if len(diags) == 0 {
				continue
			}
			cp := make([]Diagnostic, len(diags))
			copy(cp, diags)
			out[path] = cp
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ServerForExt returns the configured server name for a file extension, if any.
func (m *Manager) ServerForExt(ext string) (name string, ok bool) {
	if m == nil {
		return "", false
	}
	ext = normalizeExt(ext)
	m.mu.Lock()
	defer m.mu.Unlock()
	name, ok = m.extIndex[ext]
	return name, ok
}

func (m *Manager) clientForPath(absPath string) *Client {
	ext := normalizeExt(filepath.Ext(absPath))
	if ext == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.disabled != nil {
		// look up by ext
	}
	name, ok := m.extIndex[ext]
	if !ok {
		return nil
	}
	if m.disabled[name] {
		return nil
	}
	if _, bad := m.errs[name]; bad {
		c := m.clients[name]
		if c == nil || c.Closed() {
			return nil
		}
	}
	c := m.clients[name]
	if c == nil || c.Closed() {
		return nil
	}
	return c
}

// Retry reconnects servers. Empty name retries every configured server that is
// not currently up (including disabled). Named retry clears disabled.
func (m *Manager) Retry(ctx context.Context, name string) error {
	if m == nil {
		return fmt.Errorf("lsp: no manager")
	}
	m.mu.Lock()
	var names []string
	if name != "" {
		name = strings.TrimSpace(name)
		if _, ok := m.cfgs[name]; !ok {
			m.mu.Unlock()
			return fmt.Errorf("lsp: unknown server %q", name)
		}
		names = []string{name}
	} else {
		for n := range m.cfgs {
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
		if err := m.reconnect(ctx, cfg); err != nil && first == nil {
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

func (m *Manager) reconnect(ctx context.Context, cfg ServerConfig) error {
	m.detach(cfg.Name)
	m.startOne(ctx, cfg)
	m.mu.Lock()
	errMsg := m.errs[cfg.Name]
	up := m.isUpLocked(cfg.Name)
	m.mu.Unlock()
	if !up {
		if errMsg == "" {
			errMsg = "retry failed"
		}
		return fmt.Errorf("lsp %s: %s", cfg.Name, errMsg)
	}
	return nil
}

// Disable stops a server. Status becomes "disabled".
func (m *Manager) Disable(name string) error {
	if m == nil {
		return fmt.Errorf("lsp: no manager")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("lsp: disable requires a server name")
	}
	m.mu.Lock()
	if _, ok := m.cfgs[name]; !ok {
		m.mu.Unlock()
		return fmt.Errorf("lsp: unknown server %q", name)
	}
	m.disabled[name] = true
	m.errs[name] = "disabled"
	m.mu.Unlock()

	m.detach(name)
	m.mu.Lock()
	m.disabled[name] = true
	m.errs[name] = "disabled"
	m.mu.Unlock()
	return nil
}

func (m *Manager) detach(name string) {
	m.mu.Lock()
	client := m.clients[name]
	delete(m.clients, name)
	m.mu.Unlock()
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
			Name:       name,
			Command:    strings.TrimSpace(cfg.Command),
			Extensions: append([]string(nil), cfg.Extensions...),
		}
		if c := m.clients[name]; c != nil {
			c.docMu.Lock()
			st.OpenDocs = len(c.openDocs)
			c.docMu.Unlock()
		}
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

// Close shuts down every language server session.
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
// Invalid entries are skipped. Names are sorted for stability.
func ConfigsFromMap(servers map[string]ServerConfigFields, rootDir string) []ServerConfig {
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
		cmd := strings.TrimSpace(f.Command)
		if cmd == "" {
			continue
		}
		exts := normalizeExtensions(f.Extensions)
		if len(exts) == 0 {
			continue
		}
		out = append(out, ServerConfig{
			Name:       name,
			Command:    cmd,
			Args:       append([]string(nil), f.Args...),
			Env:        copyEnv(f.Env),
			WorkDir:    rootDir,
			RootDir:    rootDir,
			Extensions: exts,
		})
	}
	return out
}

func normalizeConfig(cfg ServerConfig) ServerConfig {
	cfg.Name = strings.TrimSpace(cfg.Name)
	cfg.Command = strings.TrimSpace(cfg.Command)
	cfg.Extensions = normalizeExtensions(cfg.Extensions)
	return cfg
}

func normalizeExtensions(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, e := range in {
		e = normalizeExt(e)
		if e == "" {
			continue
		}
		if _, ok := seen[e]; ok {
			continue
		}
		seen[e] = struct{}{}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
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
	r := rune(name[0])
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func validateServerConfig(cfg ServerConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("lsp: empty server name")
	}
	if cfg.Command == "" {
		return fmt.Errorf("lsp %s: empty command", cfg.Name)
	}
	return nil
}

// redactErr returns a short error string without env values or tokens.
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

// FormatStatuses is a multi-line human summary for /lsp (E2.3).
func FormatStatuses(statuses []Status) string {
	if len(statuses) == 0 {
		return "no language servers configured (add lsp.servers in config)"
	}
	var b strings.Builder
	for i, st := range statuses {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s  %s  %s", st.Name, st.State, st.Command)
		if len(st.Extensions) > 0 {
			fmt.Fprintf(&b, "  %s", strings.Join(st.Extensions, ","))
		}
		if st.OpenDocs > 0 {
			fmt.Fprintf(&b, "  docs=%d", st.OpenDocs)
		}
		if st.Error != "" && st.State != "disabled" {
			fmt.Fprintf(&b, "  (%s)", st.Error)
		}
	}
	return b.String()
}
