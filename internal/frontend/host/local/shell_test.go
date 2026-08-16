package local

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/sandbox"
	"github.com/jonathanung/strike-cli/internal/frontend/host"
)

func testShellPolicy(workDir string) sandbox.Policy {
	// AllowDegrade so CI without bwrap/sandbox-exec still exercises shell Run.
	return sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WorkDir: workDir, AllowDegrade: true}
}

func TestShellRunPwd(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	sh := NewShell(root, testShellPolicy(root))
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
	root := t.TempDir()
	sh := NewShell(root, testShellPolicy(root))
	_, err := sh.Run(context.Background(), "   ")
	if err == nil {
		t.Fatal("empty command: want error")
	}
}

func TestShellGuardBlocksOutsideRm(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	// Outside must not sit under shared writable roots (/tmp, ~/.cache, …).
	home, err := os.UserHomeDir()
	if err != nil || home == "" || sandbox.IsSharedWritablePath(home) {
		t.Skip("need a non-shared-writable home for outside fixture")
	}
	outside, err := os.MkdirTemp(home, ".strike-shell-guard-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	if sandbox.IsSharedWritablePath(outside) {
		t.Skipf("outside fixture %q is shared-writable", outside)
	}
	marker := filepath.Join(outside, "keep-me")
	if err := os.WriteFile(marker, []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	sh := NewShell(root, testShellPolicy(root))
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
	sh := NewShell(a, testShellPolicy(a))
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
