package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/harness/tool"
)

// SetRegistry sets the registry bridge tools bind into, for servers attached
// after start (Powers). StartAll also sets it.
func (m *Manager) SetRegistry(reg *tool.Registry) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.reg = reg
	m.mu.Unlock()
}

// AttachServer starts one server on demand and binds its tools, running the
// same admission scan as StartAll. attached is false when a server with that
// name is already configured and up (the caller does not own it, and must
// not detach it). A server that fails to come up is forgotten again, so it
// leaves no stale /mcp row, and the error is returned.
func (m *Manager) AttachServer(ctx context.Context, cfg ServerConfig) (attached bool, err error) {
	if m == nil {
		return false, fmt.Errorf("mcp: no manager")
	}
	m.mu.Lock()
	reg := m.reg
	_, known := m.cfgs[cfg.Name]
	up := known && m.isUpLocked(cfg.Name)
	m.mu.Unlock()
	if reg == nil {
		return false, fmt.Errorf("mcp: not started")
	}
	if up {
		return false, nil
	}
	if known {
		return false, fmt.Errorf("mcp %s: a configured server with this name is not up", cfg.Name)
	}
	m.startOne(ctx, cfg, reg)
	m.mu.Lock()
	up = m.isUpLocked(cfg.Name)
	errMsg := m.errs[cfg.Name]
	m.mu.Unlock()
	if up {
		return true, nil
	}
	m.DetachServer(cfg.Name)
	if errMsg == "" {
		errMsg = "server did not come up"
	}
	return false, fmt.Errorf("mcp %s: %s", cfg.Name, errMsg)
}

// DetachServer stops a server attached with AttachServer, unregisters its
// tools, and forgets its configuration.
func (m *Manager) DetachServer(name string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	reg := m.reg
	m.mu.Unlock()
	m.detach(name, reg)
	m.mu.Lock()
	delete(m.cfgs, name)
	delete(m.errs, name)
	delete(m.disabled, name)
	delete(m.admitAction, name)
	delete(m.admitReason, name)
	m.mu.Unlock()
}

// ToolServer reports which server's bridge registered the registry tool
// name. Bridges register before the manager records them, so a tool can be
// briefly in the registry and unknown here.
func (m *Manager) ToolServer(name string) (string, bool) {
	if m == nil {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for srv, names := range m.tools {
		for _, n := range names {
			if n == name {
				return srv, true
			}
		}
	}
	return "", false
}

// PackConnector implements powers.Connector over a Manager. The manager and
// its tool registry are process-wide, so a server is attached once and
// shared by every session that activates a pack naming it; it is detached
// when the last session releases it. Servers configured outside Powers are
// borrowed, never detached.
type PackConnector struct {
	resolve func(server string) (ServerConfig, bool)
	attach  func(ctx context.Context, cfg ServerConfig) (bool, error)
	detach  func(name string)
	timeout time.Duration

	// toolServer maps a registry tool to its server (Manager.ToolServer).
	toolServer func(tool string) (string, bool)

	mu        sync.Mutex
	locks     map[string]*sync.Mutex // server -> serializes attach/detach
	refs      map[string]int         // server -> sessions holding it
	owned     map[string]bool        // server -> attached by us
	attaching map[string]bool        // server -> attach or detach in flight
}

// NewPackConnector builds a connector. resolve maps a pack server name to
// its configuration.
func NewPackConnector(m *Manager, resolve func(server string) (ServerConfig, bool)) *PackConnector {
	c := newPackConnector(resolve, m.AttachServer, m.DetachServer)
	c.toolServer = m.ToolServer
	return c
}

func newPackConnector(resolve func(string) (ServerConfig, bool), attach func(context.Context, ServerConfig) (bool, error), detach func(string)) *PackConnector {
	return &PackConnector{
		resolve:   resolve,
		attach:    attach,
		detach:    detach,
		timeout:   45 * time.Second,
		locks:     map[string]*sync.Mutex{},
		refs:      map[string]int{},
		owned:     map[string]bool{},
		attaching: map[string]bool{},
	}
}

// ScopedTool reports whether the registry tool name belongs to a server
// Powers attached (or is attaching or detaching), and which one. Those tools are session
// scoped (#1943); a server configured outside Powers is shared and never
// reported. When the manager has not recorded the tool yet it falls back to
// the mcp_<server>_ name prefix, so a tool that appears mid-attach is
// treated as scoped rather than leaking to every session.
func (c *PackConnector) ScopedTool(name string) (string, bool) {
	c.mu.Lock()
	scoped := make([]string, 0, len(c.owned)+len(c.attaching))
	for srv, own := range c.owned {
		if own {
			scoped = append(scoped, srv)
		}
	}
	for srv := range c.attaching {
		if !c.owned[srv] {
			scoped = append(scoped, srv)
		}
	}
	c.mu.Unlock()
	if len(scoped) == 0 {
		return "", false
	}
	if c.toolServer != nil {
		if srv, ok := c.toolServer(name); ok {
			for _, s := range scoped {
				if s == srv {
					return srv, true
				}
			}
			return "", false
		}
	}
	for _, s := range scoped {
		if strings.HasPrefix(name, "mcp_"+sanitizeName(s)+"_") {
			return s, true
		}
	}
	return "", false
}

func (c *PackConnector) serverLock(server string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.locks[server]
	if l == nil {
		l = &sync.Mutex{}
		c.locks[server] = l
	}
	return l
}

// Connect attaches server for session (once per process).
func (c *PackConnector) Connect(_ string, server string) error {
	server = strings.TrimSpace(server)
	l := c.serverLock(server)
	l.Lock()
	defer l.Unlock()
	c.mu.Lock()
	if c.refs[server] > 0 {
		c.refs[server]++
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	cfg, ok := c.resolve(server)
	if !ok {
		return fmt.Errorf("mcp %s: no configuration for this server", server)
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	c.mu.Lock()
	c.attaching[server] = true
	c.mu.Unlock()
	attached, err := c.attach(ctx, cfg)
	c.mu.Lock()
	delete(c.attaching, server)
	if err == nil {
		c.refs[server] = 1
		c.owned[server] = attached
	}
	c.mu.Unlock()
	return err
}

// Disconnect releases session's hold on server, detaching it when no
// session holds it any more and Powers attached it.
func (c *PackConnector) Disconnect(_ string, server string) {
	server = strings.TrimSpace(server)
	l := c.serverLock(server)
	l.Lock()
	defer l.Unlock()
	c.mu.Lock()
	if c.refs[server] <= 0 {
		c.mu.Unlock()
		return
	}
	c.refs[server]--
	last := c.refs[server] == 0
	owned := c.owned[server]
	if last {
		delete(c.refs, server)
		delete(c.owned, server)
		if owned {
			// Still scoped until detach unregisters its tools, so they
			// never become visible to every session on the way out.
			c.attaching[server] = true
		}
	}
	c.mu.Unlock()
	if last && owned {
		c.detach(server)
		c.mu.Lock()
		delete(c.attaching, server)
		c.mu.Unlock()
	}
}
