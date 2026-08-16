package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host"
)

type fakeLSP struct {
	statuses   []host.LSPServerStatus
	diags      []host.Diagnostic
	retryErr   error
	disableErr error
	retried    []string
	disabled   []string
}

func (f *fakeLSP) Statuses() []host.LSPServerStatus { return f.statuses }

func (f *fakeLSP) Retry(name string) error {
	f.retried = append(f.retried, name)
	return f.retryErr
}

func (f *fakeLSP) Disable(name string) error {
	f.disabled = append(f.disabled, name)
	return f.disableErr
}

func (f *fakeLSP) Diagnostics() []host.Diagnostic { return f.diags }

func TestLSPCommandListsServers(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.LSP = &fakeLSP{statuses: []host.LSPServerStatus{
		{Name: "go", State: "up", Command: "gopls", Extensions: []string{".go"}, OpenDocs: 2},
		{Name: "python", State: "error", Command: "pylsp", Extensions: []string{".py"}, Error: "exec: not found"},
	}}
	next, _ := m.handleCommand("/lsp")
	nm := next.(Model)
	if !strings.Contains(nm.notice, "go") || !strings.Contains(nm.notice, "up") {
		t.Fatalf("notice = %q", nm.notice)
	}
	if !strings.Contains(nm.notice, "gopls") || !strings.Contains(nm.notice, ".go") {
		t.Fatalf("notice missing go details: %q", nm.notice)
	}
	if !strings.Contains(nm.notice, "docs=2") {
		t.Fatalf("notice missing open docs: %q", nm.notice)
	}
	if !strings.Contains(nm.notice, "not found") {
		t.Fatalf("notice missing error: %q", nm.notice)
	}
	if !strings.Contains(nm.notice, "/lsp retry") || !strings.Contains(nm.notice, "/diagnostics") {
		t.Fatalf("notice missing hints: %q", nm.notice)
	}
}

func TestLSPCommandEmpty(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.LSP = nil
	next, _ := m.handleCommand("/lsp")
	nm := next.(Model)
	if !strings.Contains(nm.notice, "no language servers") {
		t.Fatalf("notice = %q", nm.notice)
	}
}

func TestLSPCommandRetryDisable(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fake := &fakeLSP{statuses: []host.LSPServerStatus{
		{Name: "go", State: "down", Command: "gopls"},
	}}
	m.services.LSP = fake

	next, _ := m.handleCommand("/lsp retry go")
	nm := next.(Model)
	if len(fake.retried) != 1 || fake.retried[0] != "go" {
		t.Fatalf("retried = %v notice=%q", fake.retried, nm.notice)
	}
	if !strings.Contains(nm.notice, "retried go") {
		t.Fatalf("notice = %q", nm.notice)
	}

	next, _ = nm.handleCommand("/lsp disable go")
	nm = next.(Model)
	if len(fake.disabled) != 1 || fake.disabled[0] != "go" {
		t.Fatalf("disabled = %v", fake.disabled)
	}
	if !strings.Contains(nm.notice, "disabled go") {
		t.Fatalf("notice = %q", nm.notice)
	}

	fake.disableErr = fmt.Errorf("lsp: unknown server")
	next, _ = nm.handleCommand("/lsp disable missing")
	nm = next.(Model)
	if !nm.noticeErr || !strings.Contains(nm.notice, "unknown") {
		t.Fatalf("notice = %q err=%v", nm.notice, nm.noticeErr)
	}
}

func TestLSPCommandUsage(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.LSP = &fakeLSP{statuses: []host.LSPServerStatus{{Name: "a", State: "up"}}}
	next, _ := m.handleCommand("/lsp disable")
	nm := next.(Model)
	if !strings.Contains(nm.notice, "usage:") {
		t.Fatalf("notice = %q", nm.notice)
	}
}
