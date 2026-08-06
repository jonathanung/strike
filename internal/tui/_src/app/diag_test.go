package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/pkg/diag"
)

func TestHandleDiagCommandSendsInspectAndExports(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.sessionID = "sess-diag"
	m.workDir = t.TempDir()
	path := filepath.Join(m.workDir, "bundle.json")

	next, cmd := m.handleCommand("/diag export " + path)
	if cmd == nil {
		t.Fatal("expected inspect cmd")
	}
	nm, ok := next.(Model)
	if !ok {
		t.Fatalf("next type %T", next)
	}
	if nm.pendingDiagExportPath != path {
		t.Fatalf("pending path = %q, want %q", nm.pendingDiagExportPath, path)
	}
	// Drain the op send.
	_ = cmd()
	got := receiveAppOp(t, ops)
	if got != (protocol.InspectDiagnosticBundle{}) {
		t.Fatalf("op = %#v, want InspectDiagnosticBundle", got)
	}

	secret := "sk-ant-api03-TUIDIAGLEAKVALUE99"
	ev := protocol.DiagnosticBundle{
		Correlation:     protocol.Correlation{SessionID: "sess-diag"},
		SchemaVersion:   diag.SchemaVersion,
		ProtocolVersion: protocol.Version,
		ExportedAt:      time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
		Redacted:        true,
		Session: protocol.DiagnosticSession{
			SessionID: "sess-diag", RootSessionID: "sess-diag",
		},
		Prompt: protocol.DiagnosticPrompt{
			Precedence:  []string{protocol.PromptLayerShared},
			Layers:      []protocol.PromptLayerInfo{{Kind: protocol.PromptLayerShared, Source: "builtin:shared", Mode: protocol.PromptLayerAppend, Chars: 20, Preview: "hi key=" + secret}},
			LayerCount:  1,
			SystemChars: 20,
		},
		Config: protocol.DiagnosticConfig{
			Provider: "echo", Model: "echo", Agent: "build",
			Digests: map[string]string{"effective": "abc"},
		},
	}
	exportCmd := nm.applyDiagnosticBundle(ev)
	if exportCmd == nil {
		t.Fatal("expected export cmd")
	}
	if nm.pendingDiagExportPath != "" {
		t.Fatal("pending path should clear after apply")
	}
	msg := exportCmd()
	finished, ok := msg.(diagFinishedMsg)
	if !ok {
		t.Fatalf("msg type %T", msg)
	}
	if finished.err != nil {
		t.Fatal(finished.err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "sk-ant-api03-") {
		t.Fatalf("secret leaked:\n%s", raw)
	}
	var b diag.Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if b.SchemaVersion != diag.SchemaVersion || b.Prompt.LayerCount != 1 {
		t.Fatalf("bundle = %+v", b)
	}
	_, _ = nm.applyDiagFinished(finished)
}

func TestHandleDiagBareUsesDefaultPath(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.sessionID = "sess-default"
	m.workDir = t.TempDir()
	next, cmd := m.handleCommand("/diag")
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	nm := next.(Model)
	if !strings.Contains(nm.pendingDiagExportPath, ".strike") || !strings.HasSuffix(nm.pendingDiagExportPath, ".json") {
		t.Fatalf("default path = %q", nm.pendingDiagExportPath)
	}
	_ = cmd()
	got := receiveAppOp(t, ops)
	if got != (protocol.InspectDiagnosticBundle{}) {
		t.Fatalf("op = %#v", got)
	}
}

func TestHandleDiagAlias(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.workDir = t.TempDir()
	_, cmd := m.handleCommand("/diagnostic export " + filepath.Join(m.workDir, "x.json"))
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	_ = cmd()
	got := receiveAppOp(t, ops)
	if got != (protocol.InspectDiagnosticBundle{}) {
		t.Fatalf("op = %#v", got)
	}
}
