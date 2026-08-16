package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func newTestKeybindEditor() *keybindEditor {
	return newKeybindEditor(defaultKeyMap(), nil, nil)
}

func TestKeybindEditorOpenClose(t *testing.T) {
	m := newTestKeybindEditor()
	if m == nil {
		t.Fatal("newKeybindEditor returned nil")
	}
	next, cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if next != nil {
		t.Error("esc did not close modal")
	}
	if cmd != nil {
		t.Error("expected nil cmd on close")
	}
}

func TestKeybindEditorCloseQ(t *testing.T) {
	m := newTestKeybindEditor()
	next, _ := m.update(tea.KeyPressMsg{Code: 'q', Text: string([]rune{'q'})})
	if next != nil {
		t.Error("q did not close modal")
	}
}

func TestKeybindEditorFilter(t *testing.T) {
	m := newTestKeybindEditor()
	next, cmd := m.update(tea.KeyPressMsg{Text: string([]rune{'q', 'u', 'i', 't'})})
	if next == nil {
		t.Fatal("filter keystroke closed modal")
	}
	if cmd != nil {
		t.Error("filter keystroke should not produce cmd")
	}
	if m.filter != "quit" {
		t.Errorf("filter = %q, want %q", m.filter, "quit")
	}
	if len(m.filtered) == 0 {
		t.Error("filtered list is empty, expected at least quit entry")
	}
}

func TestKeybindEditorCaptureAndRebind(t *testing.T) {
	m := newTestKeybindEditor()
	var jumpIdx int
	found := false
	for i, e := range m.filtered {
		if e.ID == "nav.jump-bottom" {
			jumpIdx = i
			found = true
			break
		}
	}
	if !found {
		t.Fatal("nav.jump-bottom not found in catalog")
	}
	m.cursor = jumpIdx

	next, cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if next == nil {
		t.Fatal("enter on row closed modal")
	}
	if !m.capturing {
		t.Fatal("expected capturing=true after enter")
	}
	if m.captureID != "nav.jump-bottom" {
		t.Errorf("captureID = %q, want %q", m.captureID, "nav.jump-bottom")
	}
	if cmd != nil {
		t.Error("enter should not have a cmd")
	}

	next, cmd = m.update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if next == nil {
		t.Fatal("capturing keystroke closed modal")
	}
	if m.capturing {
		t.Fatal("expected capturing=false after capture")
	}
	if cmd == nil {
		t.Fatal("expected cmd (rebindAppliedMsg)")
	}
	msg := cmd()
	ram, ok := msg.(rebindAppliedMsg)
	if !ok {
		t.Fatalf("expected rebindAppliedMsg, got %T", msg)
	}
	if ram.ID != "nav.jump-bottom" {
		t.Errorf("rebind ID = %q, want %q", ram.ID, "nav.jump-bottom")
	}
	if len(ram.Chords) != 1 || ram.Chords[0] != "ctrl+b" {
		t.Errorf("rebind Chords = %v, want [ctrl+b]", ram.Chords)
	}
}

func TestKeybindEditorCaptureEscCancels(t *testing.T) {
	m := newTestKeybindEditor()
	m.capturing = true
	m.captureID = "nav.jump-bottom"

	next, cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if next == nil {
		t.Fatal("esc during capture should not close modal")
	}
	if m.capturing {
		t.Fatal("expected capturing=false after esc cancel")
	}
	if m.captureID != "" {
		t.Fatal("expected captureID cleared after esc cancel")
	}
	if cmd != nil {
		t.Error("expected nil cmd after esc cancel")
	}
}

func TestKeybindEditorResetOverride(t *testing.T) {
	m := newTestKeybindEditor()
	m.pending["nav.jump-bottom"] = []string{"ctrl+b"}
	for i, e := range m.filtered {
		if e.ID == "nav.jump-bottom" {
			m.cursor = i
			break
		}
	}

	next, cmd := m.update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if next == nil {
		t.Fatal("ctrl+d closed modal")
	}
	if _, ok := m.pending["nav.jump-bottom"]; ok {
		t.Error("expected pending to be cleared after ctrl+d")
	}
	if _, ok := m.saved["nav.jump-bottom"]; ok {
		t.Error("expected saved to be cleared after ctrl+d")
	}
	if cmd == nil {
		t.Fatal("expected cmd after ctrl+d reset")
	}
	msg := cmd()
	ram, ok := msg.(rebindAppliedMsg)
	if !ok {
		t.Fatalf("expected rebindAppliedMsg, got %T", msg)
	}
	if ram.ID != "nav.jump-bottom" {
		t.Errorf("rebind ID = %q, want %q", ram.ID, "nav.jump-bottom")
	}
	if ram.Chords != nil {
		t.Errorf("expected nil chords (reset), got %v", ram.Chords)
	}
}

func TestKeybindEditorResetClearsSaved(t *testing.T) {
	m := newTestKeybindEditor()
	m.saved["nav.jump-bottom"] = []string{"ctrl+b"}
	m.pending["nav.jump-bottom"] = []string{"ctrl+b"}
	for i, e := range m.filtered {
		if e.ID == "nav.jump-bottom" {
			m.cursor = i
			break
		}
	}

	next, cmd := m.update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if next == nil {
		t.Fatal("ctrl+d closed modal")
	}
	if _, ok := m.pending["nav.jump-bottom"]; ok {
		t.Error("expected pending cleared after ctrl+d")
	}
	if _, ok := m.saved["nav.jump-bottom"]; ok {
		t.Error("expected saved cleared after ctrl+d")
	}
	if cmd == nil {
		t.Fatal("expected cmd after ctrl+d reset")
	}
}

func TestKeybindEditorSavePending(t *testing.T) {
	m := newTestKeybindEditor()
	m.pending["nav.jump-bottom"] = []string{"ctrl+b"}

	next, cmd := m.update(tea.KeyPressMsg{Code: 's', Mod: tea.ModAlt})
	if next == nil {
		t.Fatal("alt+s closed modal")
	}
	if cmd == nil {
		t.Fatal("expected cmd for save")
	}
	msg := cmd()
	ksm, ok := msg.(keybindsSavedMsg)
	if !ok {
		t.Fatalf("expected keybindsSavedMsg, got %T", msg)
	}
	if ksm.err == nil {
		t.Error("expected save error with nil settings")
	}
}

func TestKeybindEditorViewDoesNotPanic(t *testing.T) {
	m := newTestKeybindEditor()
	th := theme.Default()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("view panicked: %v", r)
		}
	}()
	_ = m.view(60, th)
}

func TestKeybindEditorSavedOverridesPreserved(t *testing.T) {
	m := newTestKeybindEditor()
	if _, ok := m.saved["nav.jump-bottom"]; ok {
		t.Fatal("new editor should not have saved overrides without input")
	}

	saved := map[string][]string{"nav.jump-bottom": {"ctrl+b"}}
	m2 := newKeybindEditor(defaultKeyMap(), saved, nil)
	if _, ok := m2.pending["nav.jump-bottom"]; !ok {
		t.Error("nav.jump-bottom should be pending from saved")
	}
}

func TestKeybindEditorCaptureInvalidChord(t *testing.T) {
	m := newTestKeybindEditor()
	m.cursor = 0
	m.capturing = true
	m.captureID = "global.quit"

	next, cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if next == nil {
		t.Fatal("esc during capture should not close modal")
	}
	if m.capturing {
		t.Fatal("expected capturing=false")
	}
	if cmd != nil {
		t.Error("expected nil cmd after esc")
	}
}

func TestKeybindEditorViewCaptureMode(t *testing.T) {
	m := newTestKeybindEditor()
	m.capturing = true
	m.captureID = "nav.jump-bottom"
	th := theme.Default()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("view panicked in capture mode: %v", r)
		}
	}()
	_ = m.view(60, th)
}

func TestKeybindEditorViewWithPending(t *testing.T) {
	m := newTestKeybindEditor()
	m.pending["nav.jump-bottom"] = []string{"ctrl+b"}
	th := theme.Default()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("view panicked with pending: %v", r)
		}
	}()
	_ = m.view(60, th)
}

func TestKeybindEditorCaptureRKeyFallsThroughToFilter(t *testing.T) {
	m := newTestKeybindEditor()
	m.cursor = 0

	next, cmd := m.update(tea.KeyPressMsg{Code: 'r', Text: string([]rune{'r'})})
	if next == nil {
		t.Fatal("r closed modal")
	}
	if m.capturing {
		t.Fatal("r should not start capture — it filters now")
	}
	if m.filter != "r" {
		t.Fatalf("filter = %q, want %q", m.filter, "r")
	}
	if cmd != nil {
		t.Error("r should not produce cmd")
	}
}
