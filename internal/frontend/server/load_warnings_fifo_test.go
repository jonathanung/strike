//go:build unix

package server

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestBootstrapLoadWarningsRejectsFIFO guards #1855: the bootstrap rescan
// reports a FIFO skill file instead of blocking the page load on open.
func TestBootstrapLoadWarningsRejectsFIFO(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	work := t.TempDir()
	dir := filepath.Join(work, ".claude", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.md"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	srv, err := New(Options{WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	// The bootstrap handler serves srv.loadWarnings; call it off the test
	// goroutine so a blocking open fails the test instead of hanging it.
	done := make(chan []loadWarning, 1)
	go func() { done <- srv.loadWarnings("") }()
	var got []loadWarning
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("bootstrap rescan blocked on a FIFO")
	}
	for _, w := range got {
		if w.File == "pipe.md" && w.Code == "unreadable" && strings.Contains(w.Message, "not a regular file") {
			return
		}
	}
	t.Fatalf("no unreadable warning for the FIFO: %+v", got)
}
