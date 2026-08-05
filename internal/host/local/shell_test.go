package local

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/host"
)

func TestShellRunPwd(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	sh := NewShell(root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := sh.Run(ctx, "pwd")
	if err != nil {
		t.Fatalf("Run pwd: %v", err)
	}
	got := strings.TrimSpace(res.Output)
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		want = root
	}
	gotReal, err := filepath.EvalSymlinks(got)
	if err != nil {
		gotReal = got
	}
	if gotReal != want {
		t.Fatalf("pwd = %q (real %q), want %q", got, gotReal, want)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0", res.ExitCode)
	}
	if res.Command != "pwd" {
		t.Fatalf("Command = %q", res.Command)
	}
}

func TestShellRunEmpty(t *testing.T) {
	sh := NewShell(t.TempDir())
	_, err := sh.Run(context.Background(), "   ")
	if err == nil {
		t.Fatal("empty command: want error")
	}
}

func TestShellSandboxBlocksOutsideRm(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep-me")
	if err := os.WriteFile(marker, []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh := NewShell(root)
	res, err := sh.Run(context.Background(), "rm -rf "+outside)
	if err == nil {
		t.Fatalf("outside rm: want error, got %#v", res)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("outside marker removed despite path guard: %v", statErr)
	}
	if !strings.Contains(err.Error(), "escapes workspace") && !strings.Contains(res.Output, "escapes workspace") {
		t.Fatalf("err/output should mention workspace escape: err=%v out=%q", err, res.Output)
	}
}

func TestSetShellWorkDir(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	a := t.TempDir()
	b := t.TempDir()
	if err := os.WriteFile(filepath.Join(b, "marker"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh := NewShell(a)
	SetShellWorkDir(sh, b)
	res, err := sh.Run(context.Background(), "cat marker")
	if err != nil {
		t.Fatalf("Run after SetShellWorkDir: %v", err)
	}
	if strings.TrimSpace(res.Output) != "b" {
		t.Fatalf("output = %q, want b", res.Output)
	}
}

func TestSetShellWorkDirIgnoresForeign(t *testing.T) {
	SetShellWorkDir(nil, t.TempDir())
	SetShellWorkDir(foreignShell{}, t.TempDir())
}

type foreignShell struct{}

func (foreignShell) Run(context.Context, string) (host.ShellResult, error) {
	return host.ShellResult{}, nil
}
