package tool

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestReadToolRefusesFIFO(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix fifo")
	}
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	tc := allowAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err := NewRead().Execute(ctx, []byte(`{"filePath":"pipe"}`), tc)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected fifo read error")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("hung %s on %v", elapsed, err)
	}
}

func TestWriteToolRefusesSymlinkLeaf(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "t")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "l")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	tc := allowAll(dir)
	tc.Files = &FileState{}
	_, err := NewWrite().Execute(context.Background(), []byte(`{"filePath":"l","content":"y"}`), tc)
	if err == nil {
		t.Fatal("expected symlink refuse")
	}
	data, _ := os.ReadFile(target)
	if string(data) != "x" {
		t.Fatalf("target mutated: %q", data)
	}
}

func TestPathIdentityGrantMatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix")
	}
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(realDir, "a.go")
	if err := os.WriteFile(f, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Skipf("symlink: %v", err)
	}
	via := filepath.Join(alias, "a.go")
	a, err := pathIdentity(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := pathIdentity(via)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("identity mismatch %q vs %q", a, b)
	}
	if cleanAbs(f) != cleanAbs(via) {
		t.Fatalf("cleanAbs %q vs %q", cleanAbs(f), cleanAbs(via))
	}
}
