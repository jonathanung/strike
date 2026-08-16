package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/term"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

// TestPTYRenderEmbeddedInTerminalModal is the highest-risk Charm v2 surface:
// vt10x → lipgloss v2 Render → terminal modal chrome must stay coherent.
func TestPTYRenderEmbeddedInTerminalModal(t *testing.T) {
	script := `#!/bin/sh
printf 'PTY-GOLDEN-MARKER line one\n'
printf '\033[1;32mGREEN\033[0m plain\n'
sleep 30
`
	dir := t.TempDir()
	path := filepath.Join(dir, "paint.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path)
	sess, err := term.Start(cmd, 40, 12)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	deadline := time.Now().Add(3 * time.Second)
	for {
		if strings.Contains(sess.Terminal().String(), "PTY-GOLDEN-MARKER") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("PTY never painted marker; screen=%q", sess.Terminal().String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Direct renderer still shows text + styling under lipgloss v2.
	direct := term.Render(sess, 40, 12)
	plainDirect := ansi.Strip(direct)
	if !strings.Contains(plainDirect, "PTY-GOLDEN-MARKER") {
		t.Fatalf("term.Render missing marker: %q", plainDirect)
	}
	if direct == plainDirect {
		t.Fatal("term.Render lost ANSI styling (GREEN cell)")
	}

	modal := newTerminalModal(sess, path, "paint.sh", fileMeta{}, false, "sh")
	modal.setHostSize(100, 30)
	view := modal.view(80, theme.Default())
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "sh paint.sh") {
		t.Fatalf("modal chrome title missing: %q", plain)
	}
	if !strings.Contains(plain, "PTY-GOLDEN-MARKER") {
		t.Fatalf("modal body missing PTY content: %q", plain)
	}
	if !strings.Contains(plain, "ctrl+g close") {
		t.Fatalf("modal footer missing: %q", plain)
	}
	// Key forwarding still encodes under bubbletea v2 KeyPressMsg.
	if b := term.EncodeKey(tea.KeyPressMsg{Code: tea.KeyEnter}); string(b) != "\r" {
		t.Fatalf("EncodeKey enter = %q", b)
	}
	if b := term.EncodeKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); string(b) != "\x03" {
		t.Fatalf("EncodeKey ctrl+c = %q", b)
	}
}

// TestPTYRenderGolden snapshots a fixed vt10x→lipgloss paint for regression
// detection on the embedded editor path (UPDATE_GOLDEN=1 to refresh).
func TestPTYRenderGolden(t *testing.T) {
	script := `#!/bin/sh
printf 'LINE-A\n'
printf '\033[31mRED\033[0m\n'
printf 'LINE-C\n'
sleep 30
`
	dir := t.TempDir()
	path := filepath.Join(dir, "g.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path)
	sess, err := term.Start(cmd, 16, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	deadline := time.Now().Add(3 * time.Second)
	for {
		if strings.Contains(sess.Terminal().String(), "LINE-C") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("PTY never painted; screen=%q", sess.Terminal().String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Plain snapshot only — color profile can vary; structure must not.
	got := normalizeFrameGolden(term.Render(sess, 16, 4))
	goldenDir := filepath.Join(moduleRoot(t), "internal", "frontend", "tui", "app", "testdata", "pty")
	goldenPath := filepath.Join(goldenDir, "render-16x4.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		t.Logf("updated %s", goldenPath)
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v (run UPDATE_GOLDEN=1)", err)
	}
	if got != string(want) {
		t.Fatalf("pty golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
