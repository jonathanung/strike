package main

import (
	"fmt"
	"os"
	"time"

	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/trust/audit"
	"github.com/jonathanung/strike/internal/trust/usageaudit"
)

// auditBoundStore wraps a session store and fans security events into an audit sink.
type auditBoundStore struct {
	inner sessionStore
	path  string
	sink  *audit.Sink
}

func (a *auditBoundStore) Append(ev protocol.Event) error {
	if a == nil || a.inner == nil {
		return nil
	}
	return a.inner.Append(ev)
}

func (a *auditBoundStore) ObserveAudit(ev protocol.Event) {
	if a != nil && a.sink != nil {
		_ = a.sink.Observe(ev)
	}
	noteSensitive(ev)
}

// noteSensitive appends one protocol event to the process usage-audit log.
func noteSensitive(ev protocol.Event) {
	usageaudit.NoteEvent(ev)
}

// appendChildEvent records a delegated child's event. Child logs are not
// written through ObserveAudit, and the parent drain does not re-emit
// permission.decided or shell tool.begin.
func appendChildEvent(write func(string, protocol.Event) error, childID string, ev protocol.Event) error {
	noteSensitive(ev)
	if write == nil {
		return nil
	}
	return write(childID, ev)
}

func (a *auditBoundStore) Close() error {
	if a == nil {
		return nil
	}
	var err error
	if a.inner != nil {
		err = a.inner.Close()
	}
	if a.sink != nil {
		if cerr := a.sink.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	return err
}

// Path returns the session log path when known (for notices).
func (a *auditBoundStore) Path() string {
	if a == nil {
		return ""
	}
	if a.path != "" {
		return a.path
	}
	type pather interface{ Path() string }
	if p, ok := a.inner.(pather); ok {
		return p.Path()
	}
	return ""
}

func openAuditSink(cfg config.Config) (*audit.Sink, error) {
	dir := audit.DefaultDir()
	ret := audit.Retention{}
	if cfg.Session.AuditRetentionMaxEvents > 0 {
		ret.MaxEvents = cfg.Session.AuditRetentionMaxEvents
	}
	if cfg.Session.AuditRetentionMaxAgeDays > 0 {
		ret.MaxAge = time.Duration(cfg.Session.AuditRetentionMaxAgeDays) * 24 * time.Hour
	}
	// Defaults when unset: keep last 10k events / 90 days.
	if ret.MaxEvents == 0 && ret.MaxAge == 0 {
		ret.MaxEvents = 10000
		ret.MaxAge = 90 * 24 * time.Hour
	}
	return audit.Open(audit.Options{Dir: dir, Retention: ret})
}

// bindAudit wraps store so runSession tees security events into the audit sink.
// Failures opening the sink are non-fatal (session continues without audit).
// When sink is non-nil it is reused (same instance as engine.Options.Audit).
func bindAudit(store sessionStore, cfg config.Config) sessionStore {
	return bindAuditSink(store, cfg, nil)
}

// bindAuditSink is bindAudit with an optional pre-opened sink (shared with engine).
func bindAuditSink(store sessionStore, cfg config.Config, sink *audit.Sink) sessionStore {
	if store == nil {
		return store
	}
	// TUI, ACP, and RPC share this path with serve. Opening here is what
	// makes NoteEvent persist outside strike serve. Close stays with the
	// process that called usageaudit.Open first (serve's defer); a second
	// Open returns the same log and must not be closed per session.
	if _, err := usageaudit.Open(usageaudit.DefaultDir()); err != nil {
		fmt.Fprintf(os.Stderr, "strike: usage audit log unavailable: %v\n", err)
	}
	if sink == nil {
		var err error
		sink, err = openAuditSink(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "strike: audit sink unavailable: %v\n", err)
			sink = nil
		}
	}
	path := ""
	type pather interface{ Path() string }
	if p, ok := store.(pather); ok {
		path = p.Path()
	}
	return &auditBoundStore{inner: store, path: path, sink: sink}
}
