package local

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/lsp"
)

func TestNewLSPNil(t *testing.T) {
	if NewLSP(nil) != nil {
		t.Fatal("nil manager should yield nil host.LSP")
	}
}

func TestLSPAdapterStatusesRetryDisable(t *testing.T) {
	dir := t.TempDir()
	mgr := lsp.NewManager(dir)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Missing binary → per-server error status (crash isolation).
	mgr.StartAll(ctx, []lsp.ServerConfig{{
		Name:       "go",
		Command:    "strike-lsp-missing-binary-557",
		RootDir:    dir,
		Extensions: []string{".go"},
	}})

	adapter := NewLSP(mgr)
	if adapter == nil {
		t.Fatal("adapter")
	}
	sts := adapter.Statuses()
	if len(sts) != 1 || sts[0].Name != "go" {
		t.Fatalf("statuses = %#v", sts)
	}
	if sts[0].State != "error" && sts[0].State != "down" {
		t.Fatalf("state = %s err=%s", sts[0].State, sts[0].Error)
	}
	if sts[0].Command != "strike-lsp-missing-binary-557" {
		t.Fatalf("command = %q", sts[0].Command)
	}
	if len(sts[0].Extensions) != 1 || sts[0].Extensions[0] != ".go" {
		t.Fatalf("extensions = %#v", sts[0].Extensions)
	}
	if len(adapter.Diagnostics()) != 0 {
		t.Fatalf("expected no diagnostics from dead server")
	}

	if err := adapter.Disable("go"); err != nil {
		t.Fatal(err)
	}
	sts = adapter.Statuses()
	if sts[0].State != "disabled" {
		t.Fatalf("after disable = %#v", sts[0])
	}

	// Retry unknown name errors.
	if err := adapter.Retry("missing"); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("retry missing = %v", err)
	}
}

func TestFormatDiagCode(t *testing.T) {
	if got := formatDiagCode(nil); got != "" {
		t.Fatalf("nil = %q", got)
	}
	if got := formatDiagCode("U1000"); got != "U1000" {
		t.Fatalf("string = %q", got)
	}
	if got := formatDiagCode(float64(42)); got != "42" {
		t.Fatalf("float = %q", got)
	}
}
