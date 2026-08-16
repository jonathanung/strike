package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestComposerReadlineKillYankAndWordNav(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.windows = windowRegistry{windows: []window{
		statefulTestWindow{windowID: "a", windowTitle: "A"},
		statefulTestWindow{windowID: "b", windowTitle: "B"},
	}}
	startWin := m.windows.index

	// ctrl+w kills the previous word.
	m.composer.SetValue("hello world")
	m.composer.SetCursorColumn(11)
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if got := m.composer.Value(); got != "hello " {
		t.Fatalf("ctrl+w = %q, want %q", got, "hello ")
	}
	if m.killBuf != "world" {
		t.Fatalf("killBuf after ctrl+w = %q, want world", m.killBuf)
	}
	if m.windows.index != startWin {
		t.Fatalf("ctrl+w cycled window to %d", m.windows.index)
	}

	// ctrl+u kills to line start.
	m.composer.SetValue("hello world")
	m.composer.SetCursorColumn(5)
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if got := m.composer.Value(); got != " world" {
		t.Fatalf("ctrl+u = %q, want %q", got, " world")
	}
	if m.killBuf != "hello" {
		t.Fatalf("killBuf after ctrl+u = %q, want hello", m.killBuf)
	}

	// ctrl+k kills to line end (must not cycle windows on left focus).
	m.composer.SetValue("hello world")
	m.composer.SetCursorColumn(5)
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if got := m.composer.Value(); got != "hello" {
		t.Fatalf("ctrl+k = %q, want %q", got, "hello")
	}
	if m.killBuf != " world" {
		t.Fatalf("killBuf after ctrl+k = %q, want %q", m.killBuf, " world")
	}
	if m.windows.index != startWin {
		t.Fatalf("ctrl+k cycled window to %d", m.windows.index)
	}

	// ctrl+y yanks the kill buffer.
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	if got := m.composer.Value(); got != "hello world" {
		t.Fatalf("ctrl+y = %q, want %q", got, "hello world")
	}

	// alt+b / alt+f move by word without editing.
	m.composer.SetValue("hello world")
	m.composer.SetCursorColumn(11)
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt})
	if col := m.composer.LineInfo().ColumnOffset; col != 6 {
		t.Fatalf("alt+b column = %d, want 6", col)
	}
	if m.composer.Value() != "hello world" {
		t.Fatalf("alt+b mutated value to %q", m.composer.Value())
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'f', Mod: tea.ModAlt})
	if col := m.composer.LineInfo().ColumnOffset; col != 11 {
		t.Fatalf("alt+f column = %d, want 11", col)
	}

	assertNoAppOp(t, ops)
}

func TestComposerReadlineDoesNotStealPaletteOrRightPaneCycle(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.windows = windowRegistry{windows: []window{
		statefulTestWindow{windowID: "a", windowTitle: "A"},
		statefulTestWindow{windowID: "b", windowTitle: "B"},
	}}

	// ctrl+p cycles windows next on left focus even with composer text (#414, #1009).
	m.composer.SetValue("draft")
	m.composer.SetCursorColumn(2)
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if m.windows.active().id() != "b" {
		t.Fatalf("left-focus ctrl+p window = %s, want b", m.windows.active().id())
	}
	if m.composer.Value() != "draft" {
		t.Fatalf("cycle stole composer edit: %q", m.composer.Value())
	}
	if m.modal != nil {
		t.Fatalf("ctrl+p opened modal %T", m.modal)
	}

	// Right-focus ctrl+p still cycles windows; ctrl+k opens palette (#414, #1009).
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right", m.focus)
	}
	before := m.windows.index
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if m.windows.index == before {
		t.Fatalf("right-focus ctrl+p did not cycle windows")
	}
	if m.composer.Value() != "draft" {
		t.Fatalf("right-focus ctrl+p edited composer: %q", m.composer.Value())
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if _, ok := m.modal.(*paletteModal); !ok {
		t.Fatalf("right-focus ctrl+k modal = %T, want palette", m.modal)
	}
	m.modal = nil

	// Mid-line kill wins; EOL falls through to palette (shared ctrl+k).
	m.focus = focusLeft
	m.composer.Focus()
	m.splitOrientation = orientVertical
	m.keyMap.applyOrientationKeys(orientVertical)
	m.composer.SetValue("alpha beta")
	m.composer.SetCursorColumn(5)
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if m.focus != focusLeft {
		t.Fatalf("vertical left ctrl+k mid-line focus = %v, want left", m.focus)
	}
	if got := m.composer.Value(); got != "alpha" {
		t.Fatalf("vertical left ctrl+k = %q, want alpha", got)
	}
	if m.modal != nil {
		t.Fatalf("mid-line kill opened modal %T", m.modal)
	}
	m.composer.SetValue("alpha")
	m.composer.SetCursorColumn(5) // EOL — no kill, palette
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if _, ok := m.modal.(*paletteModal); !ok {
		t.Fatalf("EOL ctrl+k modal = %T, want palette", m.modal)
	}
	if m.focus != focusLeft {
		t.Fatalf("EOL ctrl+k focus = %v, want left", m.focus)
	}

	assertNoAppOp(t, ops)
}

func TestContiguousDeletion(t *testing.T) {
	tests := []struct {
		before, after, want string
		ok                  bool
	}{
		{"hello world", "hello ", "world", true},
		{"hello world", "hello", " world", true},
		{"hello world", " world", "hello", true},
		{"hello", "hello", "", false},
		{"hello", "hello!", "", false},
		{"abXYcd", "abcd", "XY", true},
		{"abXYcd", "abZcd", "", false},
	}
	for _, tt := range tests {
		got, ok := contiguousDeletion(tt.before, tt.after)
		if ok != tt.ok || got != tt.want {
			t.Errorf("contiguousDeletion(%q, %q) = %q/%v, want %q/%v", tt.before, tt.after, got, ok, tt.want, tt.ok)
		}
	}
}
