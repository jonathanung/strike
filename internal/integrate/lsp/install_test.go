package lsp

import (
	"strings"
	"testing"
)

func TestMissingInstallHints(t *testing.T) {
	m := NewManager(t.TempDir())
	m.mu.Lock()
	m.cfgs["go"] = ServerConfig{Name: "go", Command: "gopls-definitely-missing-xyz", Extensions: []string{".go"}}
	m.cfgs["present"] = ServerConfig{Name: "present", Command: "true", Extensions: []string{".x"}}
	m.mu.Unlock()

	hints := m.MissingInstallHints()
	if len(hints) != 1 || hints[0].Server != "go" {
		t.Fatalf("%+v", hints)
	}
	if !hints[0].Missing || hints[0].Guidance == "" {
		t.Fatalf("%+v", hints[0])
	}
	text := FormatInstallHints(hints)
	if !strings.Contains(text, "gopls") && !strings.Contains(text, "never auto-installs") {
		t.Fatalf("%q", text)
	}
}

func TestLookPathTrue(t *testing.T) {
	if !LookPath("true") {
		t.Fatal("true should be on PATH")
	}
	if LookPath("this-binary-should-not-exist-strike-1043") {
		t.Fatal("missing binary reported present")
	}
}

func TestStatusesMarksMissing(t *testing.T) {
	m := NewManager(t.TempDir())
	m.mu.Lock()
	m.cfgs["rust"] = ServerConfig{Name: "rust", Command: "no-such-rust-analyzer-bin", Extensions: []string{".rs"}}
	m.errs["rust"] = "start failed"
	m.mu.Unlock()
	st := m.Statuses()
	if len(st) != 1 || !st[0].Missing || st[0].InstallGuidance == "" {
		t.Fatalf("%+v", st)
	}
}
