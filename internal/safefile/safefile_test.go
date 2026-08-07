package safefile_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/safefile"
)

func TestReadWriteRegular(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := safefile.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := safefile.ReadFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("got %q", data)
	}
}

func TestRefuseSymlinkLeafWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink policy tested on unix")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	err := safefile.WriteFile(link, []byte("y"), 0o644)
	if !safefile.IsCode(err, safefile.CodeSymlink) {
		t.Fatalf("err = %v, want symlink_refused", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "x" {
		t.Fatalf("target mutated: %q", data)
	}
}

func TestRefuseFIFORead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fifo on unix")
	}
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err := safefile.ReadFile(ctx, fifo)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error reading fifo")
	}
	if !safefile.IsCode(err, safefile.CodeSpecialFile) && !safefile.IsCode(err, safefile.CodeTimeout) {
		t.Fatalf("err = %v (%T), want special_file or timeout", err, err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("fifo read hung for %s", elapsed)
	}
}

func TestIdentityStable(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(sub, "f.txt")
	if err := os.WriteFile(f, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := safefile.Identity(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := safefile.Identity(filepath.Join(sub, ".", "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("identity %q vs %q", a, b)
	}
	ok, err := safefile.SameIdentity(f, filepath.Join(dir, "sub", "f.txt"))
	if err != nil || !ok {
		t.Fatalf("SameIdentity = %v, %v", ok, err)
	}
}

func TestIdentitySymlinkDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix")
	}
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(realDir, "f.txt")
	if err := os.WriteFile(f, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(realDir, alias); err != nil {
		t.Skipf("symlink: %v", err)
	}
	viaAlias := filepath.Join(alias, "f.txt")
	ok, err := safefile.SameIdentity(f, viaAlias)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		ia, _ := safefile.Identity(f)
		ib, _ := safefile.Identity(viaAlias)
		t.Fatalf("expected same identity: %q vs %q", ia, ib)
	}
}

func TestCheckLeafMissingOK(t *testing.T) {
	if err := safefile.CheckLeaf(filepath.Join(t.TempDir(), "nope"), true); err != nil {
		t.Fatal(err)
	}
}

func TestOpenReadCanceled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := safefile.OpenRead(ctx, path)
	if err == nil {
		// Fast open may win the race before cancel is observed; allow success.
		return
	}
	if !errors.Is(err, context.Canceled) && !safefile.IsCode(err, safefile.CodeTimeout) {
		t.Fatalf("err = %v", err)
	}
}
