package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

func TestDefaultKeyMapBindingsMatchTheirRequiredKeysAndHaveHelp(t *testing.T) {
	keys := defaultKeyMap()
	tests := []struct {
		name    string
		binding key.Binding
		msg     tea.KeyMsg
	}{
		{"quit", keys.Quit, tea.KeyMsg{Type: tea.KeyCtrlC}},
		{"focus left", keys.FocusLeft, tea.KeyMsg{Type: tea.KeyCtrlH}},
		{"focus right", keys.FocusRight, tea.KeyMsg{Type: tea.KeyCtrlL}},
		{"window next", keys.CycleWindowNext, tea.KeyMsg{Type: tea.KeyCtrlJ}},
		{"window prev", keys.CycleWindowPrev, tea.KeyMsg{Type: tea.KeyCtrlK}},
		{"palette", keys.Palette, tea.KeyMsg{Type: tea.KeyCtrlP}},
		{"keyhelp", keys.KeyHelp, tea.KeyMsg{Type: tea.KeyF1}},
		{"interrupt", keys.Interrupt, tea.KeyMsg{Type: tea.KeyEsc}},
		{"send", keys.Send, tea.KeyMsg{Type: tea.KeyEnter}},
		{"newline", keys.Newline, tea.KeyMsg{Type: tea.KeyEnter, Alt: true}},
		{"external editor", keys.ExternalEditor, tea.KeyMsg{Type: tea.KeyCtrlE}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !key.Matches(tt.msg, tt.binding) {
				t.Errorf("binding does not match %s", tt.msg.String())
			}
			help := tt.binding.Help()
			if help.Key == "" || help.Desc == "" {
				t.Errorf("binding help = %#v, want key and description", help)
			}
		})
	}
	// Newline help advertises shift+enter (the user-facing chord); the binding
	// itself matches alt+enter after the input normalizer rewrites CSI.
	newlineHelp := keys.Newline.Help()
	if newlineHelp.Key != "shift+enter" {
		t.Errorf("Newline help key = %q, want shift+enter", newlineHelp.Key)
	}
	if newlineHelp.Desc != "newline" {
		t.Errorf("Newline help desc = %q, want newline", newlineHelp.Desc)
	}
}

// TestShiftEnterKeyMsgDoesNotMatchCycleWindow pins that the post-WrapInput
// KeyMsg for shift+enter (KeyEnter+Alt) matches Newline only — never
// CycleWindow*/Send/Focus* — under both split orientations (#53).
func TestShiftEnterKeyMsgDoesNotMatchCycleWindow(t *testing.T) {
	msg := tea.KeyMsg{Type: tea.KeyEnter, Alt: true}
	for _, tt := range []struct {
		name   string
		orient splitOrientation
	}{
		{"horizontal", orientHorizontal},
		{"vertical", orientVertical},
	} {
		t.Run(tt.name, func(t *testing.T) {
			keys := defaultKeyMap()
			keys.applyOrientationKeys(tt.orient)
			if !key.Matches(msg, keys.Newline) {
				t.Error("KeyEnter+Alt must match Newline")
			}
			if key.Matches(msg, keys.CycleWindowNext) {
				t.Error("KeyEnter+Alt must not match CycleWindowNext")
			}
			if key.Matches(msg, keys.CycleWindowPrev) {
				t.Error("KeyEnter+Alt must not match CycleWindowPrev")
			}
			if key.Matches(msg, keys.Send) {
				t.Error("KeyEnter+Alt must not match Send")
			}
			if key.Matches(msg, keys.FocusLeft, keys.FocusRight) {
				t.Error("KeyEnter+Alt must not match Focus*")
			}
		})
	}
}

func TestKeybindCatalogCoversAppBindingsAndIsSearchable(t *testing.T) {
	keys := defaultKeyMap()
	catalog := keybindCatalog(keys)
	if len(catalog) < 10 {
		t.Fatalf("catalog length = %d, want a full cheatsheet", len(catalog))
	}
	seen := map[string]bool{}
	for _, entry := range catalog {
		if entry.ID == "" || entry.Category == "" || entry.Keys == "" || entry.Action == "" {
			t.Errorf("incomplete entry: %#v", entry)
		}
		if seen[entry.ID] {
			t.Errorf("duplicate catalog id %q", entry.ID)
		}
		seen[entry.ID] = true
	}
	for _, id := range []string{
		"nav.focus-left", "nav.focus-right", "nav.window-next", "nav.window-prev",
		"global.palette", "global.keyhelp", "composer.external-editor",
		"composer.kill-word", "composer.word-back", "composer.word-fwd",
		"composer.kill-line-start", "composer.kill-line-end", "composer.yank",
	} {
		if !seen[id] {
			t.Errorf("catalog missing %q", id)
		}
	}
	m := newKeysModal(keys)
	m.filter = "ctrl+h"
	got := m.filtered()
	if len(got) == 0 || got[0].ID != "nav.focus-left" {
		t.Errorf("filter ctrl+h = %#v, want focus-left first", got)
	}
	m.filter = "window"
	if len(m.filtered()) < 2 {
		t.Errorf("filter window matched %d rows, want at least next/prev", len(m.filtered()))
	}
}

func TestVimPaneAndWindowKeys(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.windows = windowRegistry{windows: []window{
		statefulTestWindow{windowID: "a", windowTitle: "A"},
		statefulTestWindow{windowID: "b", windowTitle: "B"},
		statefulTestWindow{windowID: "c", windowTitle: "C"},
	}}

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if m.focus != focusRight {
		t.Fatalf("ctrl+l focus = %v, want right", m.focus)
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlH})
	if m.focus != focusLeft || !m.composer.Focused() {
		t.Fatalf("ctrl+h focus = %v/composer=%v, want left/focused", m.focus, m.composer.Focused())
	}

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.windows.index != 1 || m.windows.active().id() != "b" {
		t.Errorf("ctrl+j window = %d/%s, want 1/b", m.windows.index, m.windows.active().id())
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.windows.active().id() != "c" {
		t.Errorf("second ctrl+j window = %s, want c", m.windows.active().id())
	}
	// Empty composer: ctrl+k still cycles prev (kill only claims when it deletes).
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
	if m.windows.active().id() != "b" {
		t.Errorf("ctrl+k window = %s, want b", m.windows.active().id())
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
	if m.windows.active().id() != "a" {
		t.Errorf("second ctrl+k window = %s, want a", m.windows.active().id())
	}
}

func TestKeyHelpOpensFilterableCheatsheet(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyF1})
	modal, ok := m.modal.(*keysModal)
	if !ok {
		t.Fatalf("f1 modal = %T, want keysModal", m.modal)
	}
	if len(modal.entries) == 0 {
		t.Fatal("cheatsheet has no entries")
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pal")})
	modal = m.modal.(*keysModal)
	if got := modal.filtered(); len(got) == 0 {
		t.Fatal("filter pal matched nothing")
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.modal != nil {
		t.Errorf("esc left modal open: %T", m.modal)
	}
}

func TestKeysCommandAndPaletteOpenCheatsheet(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.composer.SetValue("/keys")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if _, ok := m.modal.(*keysModal); !ok {
		t.Fatalf("/keys modal = %T, want keysModal", m.modal)
	}

	m, _ = newAppTestModel(nil, nil)
	m = updateApp(t, m, paletteInvokeMsg{Action: paletteAction{Kind: paletteActionKeybinds}})
	if _, ok := m.modal.(*keysModal); !ok {
		t.Fatalf("palette keybinds modal = %T, want keysModal", m.modal)
	}
}

func TestWindowRegistryCycleByWrapsBothDirections(t *testing.T) {
	r := windowRegistry{windows: []window{
		statefulTestWindow{windowID: "one"},
		statefulTestWindow{windowID: "two"},
	}}
	r = r.cycleBy(-1)
	if r.active().id() != "two" {
		t.Fatalf("cycleBy(-1) = %q, want two", r.active().id())
	}
	r = r.cycleBy(1)
	if r.active().id() != "one" {
		t.Fatalf("cycleBy(1) = %q, want one", r.active().id())
	}
}
