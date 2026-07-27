package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestParseVimMode(t *testing.T) {
	tests := []struct {
		in   string
		want VimMode
		ok   bool
	}{
		{in: "", want: VimModePane, ok: true},
		{in: "pane", want: VimModePane, ok: true},
		{in: "embedded", want: VimModePane, ok: true},
		{in: "OVERLAY", want: VimModeOverlay, ok: true},
		{in: "modal", want: VimModeOverlay, ok: true},
		{in: "takeover", want: VimModeTakeover, ok: true},
		{in: "nope", ok: false},
	}
	for _, tt := range tests {
		got, ok := ParseVimMode(tt.in)
		if ok != tt.ok || (tt.ok && got != tt.want) {
			t.Errorf("ParseVimMode(%q) = %q,%v want %q,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestVimModePresentation(t *testing.T) {
	tests := []struct {
		mode VimMode
		want SurfacePresentation
		ok   bool
	}{
		{mode: "", want: PresentationEmbedded, ok: true},
		{mode: VimModePane, want: PresentationEmbedded, ok: true},
		{mode: VimModeOverlay, want: PresentationModal, ok: true},
		{mode: VimModeTakeover, ok: false},
	}
	for _, tt := range tests {
		got, ok := tt.mode.Presentation()
		if ok != tt.ok || (tt.ok && got != tt.want) {
			t.Errorf("%q.Presentation() = %q,%v want %q,%v", tt.mode, got, ok, tt.want, tt.ok)
		}
	}
}

func TestParseSurfacePresentation(t *testing.T) {
	tests := []struct {
		in   string
		want SurfacePresentation
		ok   bool
	}{
		{in: "", want: PresentationEmbedded, ok: true},
		{in: "embedded", want: PresentationEmbedded, ok: true},
		{in: "pane", want: PresentationEmbedded, ok: true},
		{in: "MODAL", want: PresentationModal, ok: true},
		{in: "overlay", want: PresentationModal, ok: true},
		{in: "takeover", ok: false},
		{in: "nope", ok: false},
	}
	for _, tt := range tests {
		got, ok := ParseSurfacePresentation(tt.in)
		if ok != tt.ok || (tt.ok && got != tt.want) {
			t.Errorf("ParseSurfacePresentation(%q) = %q,%v want %q,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestLargeModalOuterWidth(t *testing.T) {
	if got := largeModalOuterWidth(120); got != 116 {
		t.Errorf("largeModalOuterWidth(120) = %d, want 116", got)
	}
	if got := largeModalOuterWidth(30); got != 40 {
		t.Errorf("largeModalOuterWidth(30) = %d, want floor 40", got)
	}
}

func TestVimTakeoverModeUsesExecProcess(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-bin"))

	m, ops := newAppTestModel(nil, nil)
	m.vimMode = VimModeTakeover
	m.workDir = t.TempDir()
	m.composer.SetValue("/vim")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("unexpected msg %#v", msg)
	}
	assertNoAppOp(t, ops)
	if !m.noticeErr || !strings.Contains(m.notice, "no editor found") {
		t.Errorf("notice = %q err=%v", m.notice, m.noticeErr)
	}
	// Ensure takeover mode did not open the embedded pane session.
	if tw, _, ok := findTerminalWindow(m.windows); ok && tw.isRunning() {
		t.Error("takeover mode started an embedded session")
	}
}

func TestVimPaneModeEmbedsNvim(t *testing.T) {
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("nvim not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("pane-embed-marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Prefer nvim over whatever VISUAL is set in the environment.
	t.Setenv("VISUAL", "nvim")
	t.Setenv("EDITOR", "nvim")

	m, ops := newAppTestModel(nil, nil)
	m.vimMode = VimModePane
	m.workDir = dir
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.composer.SetValue("/vim note.txt")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	// Drain listen cmds until terminal is running or we time out.
	deadline := time.After(5 * time.Second)
	for {
		tw, _, ok := findTerminalWindow(m.windows)
		if ok && tw.isRunning() && m.focus == focusRight {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("embedded editor did not start; focus=%v notice=%q", m.focus, m.notice)
		default:
		}
		if cmd == nil {
			// Pump a redraw in case listen already fired.
			time.Sleep(20 * time.Millisecond)
			continue
		}
		msg := runAppCmd(t, cmd)
		if msg == nil {
			cmd = nil
			continue
		}
		updated, cmd = m.Update(msg)
		m = updated.(Model)
	}
	assertNoAppOp(t, ops)

	// Close the session cleanly via the window.
	tw, _, _ := findTerminalWindow(m.windows)
	_, _ = tw.sess.Write([]byte(":q!\r"))
	// Wait for exit message.
	deadline = time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			m.closeEmbeddedSessions()
			t.Fatal("timed out waiting for embedded nvim exit")
		default:
		}
		if cmd == nil {
			// Re-arm listen if needed.
			if tw, _, ok := findTerminalWindow(m.windows); ok && tw.isRunning() {
				cmd = tw.listenCmd()
			} else {
				time.Sleep(20 * time.Millisecond)
				continue
			}
		}
		msg := runAppCmd(t, cmd)
		if msg == nil {
			cmd = nil
			continue
		}
		updated, cmd = m.Update(msg)
		m = updated.(Model)
		if _, ok := msg.(terminalExitMsg); ok {
			break
		}
		if tw, _, ok := findTerminalWindow(m.windows); ok && !tw.isRunning() {
			break
		}
	}
}

func TestPrefersTakeoverForGUIEditors(t *testing.T) {
	if !prefersTakeover("code") || !prefersTakeover("/usr/bin/subl") {
		t.Error("expected GUI editors to prefer takeover")
	}
	if prefersTakeover("nvim") || prefersTakeover("vim") {
		t.Error("terminal editors should embed")
	}
}
