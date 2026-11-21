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

	mu    sync.Mutex
	locks map[string]*sync.Mutex // server -> serializes attach/detach
	refs  map[string]int         // server -> sessions holding it
	owned map[string]bool        // server -> attached by us
}

// NewPackConnector builds a connector. resolve maps a pack server name to
// its configuration.
func NewPackConnector(m *Manager, resolve func(server string) (ServerConfig, bool)) *PackConnector {
	return newPackConnector(resolve, m.AttachServer, m.DetachServer)
}

func newPackConnector(resolve func(string) (ServerConfig, bool), attach func(context.Context, ServerConfig) (bool, error), detach func(string)) *PackConnector {
	return &PackConnector{
		resolve: resolve,
		attach:  attach,
		detach:  detach,
		timeout: 45 * time.Second,
		locks:   map[string]*sync.Mutex{},
		refs:    map[string]int{},
		owned:   map[string]bool{},
	}
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
	attached, err := c.attach(ctx, cfg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.refs[server] = 1
	c.owned[server] = attached
	c.mu.Unlock()
	return nil
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
	}
	c.mu.Unlock()
	if last && owned {
		c.detach(server)
	}
}
