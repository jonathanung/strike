package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/term"
	"github.com/jonathanung/strike-cli/internal/protocol"
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
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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

func TestNanoTakeoverModeMissingBinary(t *testing.T) {
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-bin"))

	m, ops := newAppTestModel(nil, nil)
	m.nanoMode = VimModeTakeover
	m.workDir = t.TempDir()
	m.composer.SetValue("/nano")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("unexpected msg %#v", msg)
	}
	assertNoAppOp(t, ops)
	if !m.noticeErr || !strings.Contains(m.notice, "nano not found") {
		t.Errorf("notice = %q err=%v", m.notice, m.noticeErr)
	}
	if tw, _, ok := findTerminalWindow(m.windows); ok && tw.isRunning() {
		t.Error("takeover mode started an embedded session")
	}
}

func TestNanoPaneModeEmbedsNano(t *testing.T) {
	if _, err := exec.LookPath("nano"); err != nil {
		t.Skip("nano not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("nano-embed-marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// /nano must ignore $VISUAL/$EDITOR and launch nano.
	t.Setenv("VISUAL", "nvim")
	t.Setenv("EDITOR", "nvim")

	m, ops := newAppTestModel(nil, nil)
	m.nanoMode = VimModePane
	m.workDir = dir
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.composer.SetValue("/nano note.txt")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)

	deadline := time.After(5 * time.Second)
	for {
		tw, _, ok := findTerminalWindow(m.windows)
		if ok && tw.isRunning() && m.focus == focusRight {
			if tw.label != "nano" {
				t.Fatalf("editor label = %q, want nano", tw.label)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("embedded nano did not start; focus=%v notice=%q", m.focus, m.notice)
		default:
		}
		if cmd == nil {
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
	if !strings.Contains(m.notice, "nano") {
		t.Errorf("notice should mention nano: %q", m.notice)
	}
	m.closeEmbeddedSessions()
}

func TestEditorLabel(t *testing.T) {
	if got := editorLabel("/usr/bin/nvim", "vim"); got != "nvim" {
		t.Errorf("editorLabel nvim = %q", got)
	}
	if got := editorLabel("", "vim"); got != "vim" {
		t.Errorf("editorLabel empty = %q", got)
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
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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

func TestEmbeddedEditorCapturesGlobalKeysBeforeAppRouting(t *testing.T) {
	editorCmd := exec.Command("sh", "-c", "IFS= read -r line; if [ \"$line\" = \"$(printf '\\f')\" ]; then printf CTRL_L; else printf WRONG; fi; IFS= read -r _")
	sess, err := term.Start(editorCmd, 40, 10)
	if err != nil {
		t.Fatal(err)
	}

	m, _ := newAppTestModel(nil, nil)
	tw, _ := newTerminalWindow().attach(sess, "", "", fileMeta{}, false, "vim")
	m.windows = windowRegistry{windows: []window{tw}}
	m.focus = focusRight
	t.Cleanup(func() { m.closeEmbeddedSessions() })

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("typing returned %T, want nil", msg)
	}
	updated, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("enter returned %T, want nil", msg)
	}

	received := false
	deadline := time.After(3 * time.Second)
	for !received {
		select {
		case <-deadline:
			t.Fatalf("embedded editor did not receive ctrl+l; screen=%q", sess.Terminal().String())
		case <-sess.Notify():
			received = strings.Contains(sess.Terminal().String(), "CTRL_L")
		case <-sess.Done():
			t.Fatalf("embedded editor exited early: %v", sess.WaitErr())
		}
	}

	updated, cmd = m.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("ctrl+g returned %T, want nil", msg)
	}
	if m.focus != focusLeft {
		t.Errorf("focus after ctrl+g = %v, want left", m.focus)
	}
	if tw, _, ok := findTerminalWindow(m.windows); !ok || !tw.isRunning() {
		t.Error("ctrl+g stopped the embedded editor")
	}
	if _, err := sess.Write([]byte("\r")); err != nil {
		t.Fatalf("stop embedded editor: %v", err)
	}
	select {
	case <-sess.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("embedded editor did not exit after final input")
	}
}

func TestEmbeddedEditorForwardsShiftedPrintableInput(t *testing.T) {
	editorCmd := exec.Command("sh", "-c", "IFS= read -r line; if [ \"$line\" = 'A!' ]; then printf SHIFTED_INPUT; else printf 'WRONG:%s' \"$line\"; fi; IFS= read -r _")
	sess, err := term.Start(editorCmd, 40, 10)
	if err != nil {
		t.Fatal(err)
	}

	m, _ := newAppTestModel(nil, nil)
	tw, _ := newTerminalWindow().attach(sess, "", "", fileMeta{}, false, "vim")
	m.windows = windowRegistry{windows: []window{tw}}
	m.focus = focusRight
	t.Cleanup(func() { m.closeEmbeddedSessions() })

	for _, msg := range []tea.KeyPressMsg{
		{Code: 'A', Text: "A", Mod: tea.ModShift},
		{Code: '!', Text: "!", Mod: tea.ModShift},
		{Code: tea.KeyEnter},
	} {
		updated, cmd := m.Update(msg)
		m = updated.(Model)
		if got := runAppCmd(t, cmd); got != nil {
			t.Errorf("typing returned %T, want nil", got)
		}
	}

	received := false
	deadline := time.After(3 * time.Second)
	for !received {
		select {
		case <-deadline:
			t.Fatalf("embedded editor did not receive shifted input; screen=%q", sess.Terminal().String())
		case <-sess.Notify():
			received = strings.Contains(sess.Terminal().String(), "SHIFTED_INPUT")
		case <-sess.Done():
			t.Fatalf("embedded editor exited early: %v; screen=%q", sess.WaitErr(), sess.Terminal().String())
		}
	}

	if _, err := sess.Write([]byte("\r")); err != nil {
		t.Fatalf("stop embedded editor: %v", err)
	}
	select {
	case <-sess.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("embedded editor did not exit after final input")
	}
}

func TestEmbeddedEditorDefersToActiveModal(t *testing.T) {
	editorCmd := exec.Command("sh", "-c", "IFS= read -r _")
	sess, err := term.Start(editorCmd, 40, 10)
	if err != nil {
		t.Fatal(err)
	}

	m, ops := newAppTestModel(nil, nil)
	tw, _ := newTerminalWindow().attach(sess, "", "", fileMeta{}, false, "vim")
	m.windows = windowRegistry{windows: []window{tw}}
	m.focus = focusRight
	m.modal = newPermissionModal(protocol.PermissionAsked{RequestID: "permission", Permission: "bash"}, ops, m.th)
	t.Cleanup(func() { m.closeEmbeddedSessions() })

	updated, cmd := m.Update(tea.KeyPressMsg{Text: "y"})
	m = updated.(Model)
	if msg := runAppCmd(t, cmd); msg != nil {
		t.Errorf("permission reply returned %T, want nil", msg)
	}
	if m.modal != nil {
		t.Fatalf("modal = %T after allow once, want nil", m.modal)
	}
	if got := receiveAppOp(t, ops); got != (protocol.PermissionReply{RequestID: "permission", Decision: protocol.DecisionOnce}) {
		t.Errorf("operation = %#v, want allow-once reply", got)
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
