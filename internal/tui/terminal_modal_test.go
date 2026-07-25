package tui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/term"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestTerminalModalNilSession(t *testing.T) {
	m := newTerminalModal(nil, "/tmp/x", "x.txt", fileMeta{}, false)
	if cmd := m.listenCmd(); cmd != nil {
		t.Fatal("listenCmd with nil session should be nil")
	}

	next, cmd := m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if next == nil {
		t.Fatal("key update closed nil-session modal")
	}
	if cmd != nil {
		t.Fatal("key update should not emit cmd without session")
	}

	next, cmd = m.update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if next != nil {
		t.Fatal("ctrl+g should close modal")
	}
	msg := runAppCmd(t, cmd)
	exit, ok := msg.(terminalExitMsg)
	if !ok {
		t.Fatalf("ctrl+g msg = %#v, want terminalExitMsg", msg)
	}
	if exit.path != "/tmp/x" || exit.display != "x.txt" || exit.hadPath {
		t.Fatalf("exit payload = %+v", exit)
	}

	view := m.view(40, theme.Default())
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "vim x.txt") {
		t.Fatalf("view title missing: %q", plain)
	}
	if !strings.Contains(plain, "ctrl+g close") {
		t.Fatalf("view footer missing: %q", plain)
	}
}

func TestTerminalModalSetHostSizeAndDefaultTitle(t *testing.T) {
	m := newTerminalModal(nil, "", "", fileMeta{}, false)
	m.setHostSize(100, 40)
	if m.hostW != 100 || m.hostH != 40 {
		t.Fatalf("host size = %dx%d", m.hostW, m.hostH)
	}
	view := m.view(80, theme.Default())
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "vim") {
		t.Fatalf("default title missing: %q", plain)
	}
	// Host height should expand the panel beyond the width-only fallback.
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	if len(lines) < 20 {
		t.Fatalf("expected tall host-sized panel, got %d lines", len(lines))
	}
}

func TestTerminalModalViewFallbackDimensions(t *testing.T) {
	m := newTerminalModal(nil, "", "note", fileMeta{}, true)
	// No host size: falls back to width and default height 20.
	view := m.view(50, theme.Default())
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "vim note") {
		t.Fatalf("view = %q", plain)
	}
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	if len(lines) < 8 {
		t.Fatalf("fallback height too small: %d lines", len(lines))
	}
}

func TestTerminalModalForwardsKeysAndCtrlGCloses(t *testing.T) {
	script := `#!/bin/sh
printf 'READY\n'
IFS= read -r line
printf 'GOT:%s\n' "$line"
sleep 30
`
	dir := t.TempDir()
	path := dir + "/echo.sh"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path)
	sess, err := term.Start(cmd, 40, 12)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()

	waitTermContains(t, sess, "READY", 3*time.Second)

	before := fileMeta{exists: true, size: 1}
	m := newTerminalModal(sess, path, "echo.sh", before, true)
	m.setHostSize(80, 24)

	// Forward printable keys + enter.
	for _, r := range []rune{'p', 'i', 'n', 'g'} {
		next, c := m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if next == nil {
			t.Fatal("forwarded key closed modal")
		}
		if c != nil {
			t.Fatal("key forward should not return cmd")
		}
	}
	next, c := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next == nil || c != nil {
		t.Fatal("enter should stay open without cmd")
	}

	waitTermContains(t, sess, "GOT:ping", 3*time.Second)

	view := m.view(60, theme.Default())
	if !strings.Contains(ansi.Strip(view), "GOT:ping") {
		t.Fatalf("modal view missing session paint: %q", ansi.Strip(view))
	}

	// ctrl+g closes session and emits exit with snapshot fields.
	next, c = m.update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if next != nil {
		t.Fatal("ctrl+g should close modal")
	}
	msg := runAppCmd(t, c)
	exit, ok := msg.(terminalExitMsg)
	if !ok {
		t.Fatalf("msg = %#v", msg)
	}
	if exit.path != path || exit.display != "echo.sh" || !exit.hadPath || !exit.before.exists {
		t.Fatalf("exit = %+v", exit)
	}
	select {
	case <-sess.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("session not done after ctrl+g close")
	}
}

func TestTerminalModalListenCmdOutputAndExit(t *testing.T) {
	script := `#!/bin/sh
printf 'LISTEN-MARK\n'
sleep 30
`
	dir := t.TempDir()
	path := dir + "/listen.sh"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path)
	sess, err := term.Start(cmd, 30, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()

	m := newTerminalModal(sess, path, "listen.sh", fileMeta{}, true)
	listen := m.listenCmd()
	if listen == nil {
		t.Fatal("listenCmd nil with live session")
	}

	// First notify should yield output (or exit if already done after paint).
	msg := runAppCmd(t, listen)
	switch msg.(type) {
	case terminalOutputMsg:
		// Re-arm and force exit via Close.
	case terminalExitMsg:
		return
	default:
		t.Fatalf("listen msg = %#v", msg)
	}

	go func() { _ = sess.Close() }()
	msg = runAppCmd(t, m.listenCmd())
	exit, ok := msg.(terminalExitMsg)
	if !ok {
		// Drain one more notify-then-done race.
		if _, isOut := msg.(terminalOutputMsg); isOut {
			msg = runAppCmd(t, m.listenCmd())
			exit, ok = msg.(terminalExitMsg)
		}
	}
	if !ok {
		t.Fatalf("after close listen msg = %#v", msg)
	}
	if exit.path != path || exit.display != "listen.sh" || !exit.hadPath {
		t.Fatalf("exit = %+v", exit)
	}
}

func TestTerminalModalListenCmdDoneDirect(t *testing.T) {
	// Process exits immediately so Done wins the select.
	cmd := exec.Command("sh", "-c", "exit 0")
	sess, err := term.Start(cmd, 20, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()

	m := newTerminalModal(sess, "/done", "done", fileMeta{}, false)
	deadline := time.After(3 * time.Second)
	var last tea.Msg
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for exit; last=%#v", last)
		default:
		}
		msg := runAppCmd(t, m.listenCmd())
		last = msg
		if exit, ok := msg.(terminalExitMsg); ok {
			if exit.path != "/done" || exit.display != "done" || exit.hadPath {
				t.Fatalf("exit = %+v", exit)
			}
			return
		}
	}
}

func waitTermContains(t *testing.T, s *term.Session, needle string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %q; screen=%q", needle, s.Terminal().String())
		case <-s.Notify():
			if strings.Contains(s.Terminal().String(), needle) {
				return
			}
		case <-s.Done():
			text := s.Terminal().String()
			if strings.Contains(text, needle) {
				return
			}
			t.Fatalf("exited before %q: %v screen=%q", needle, s.WaitErr(), text)
		}
	}
}
