package main

import (
	"fmt"
	"os"
	"time"

	"github.com/jonathanung/strike-cli/internal/product/config"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/trust/audit"
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
	if a == nil || a.sink == nil {
		return
	}
	_ = a.sink.Observe(ev)
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
	if sink == nil {
		var err error
		sink, err = openAuditSink(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "strike: audit sink unavailable: %v\n", err)
			return store
		}
	}
	path := ""
	type pather interface{ Path() string }
	if p, ok := store.(pather); ok {
		path = p.Path()
	}
	return &auditBoundStore{inner: store, path: path, sink: sink}
}
