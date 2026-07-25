package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
	m.composer.SetCursor(11)
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlW})
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
	m.composer.SetCursor(5)
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := m.composer.Value(); got != " world" {
		t.Fatalf("ctrl+u = %q, want %q", got, " world")
	}
	if m.killBuf != "hello" {
		t.Fatalf("killBuf after ctrl+u = %q, want hello", m.killBuf)
	}

	// ctrl+k kills to line end (must not cycle windows on left focus).
	m.composer.SetValue("hello world")
	m.composer.SetCursor(5)
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
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
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlY})
	if got := m.composer.Value(); got != "hello world" {
		t.Fatalf("ctrl+y = %q, want %q", got, "hello world")
	}

	// alt+b / alt+f move by word without editing.
	m.composer.SetValue("hello world")
	m.composer.SetCursor(11)
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}, Alt: true})
	if col := m.composer.LineInfo().ColumnOffset; col != 6 {
		t.Fatalf("alt+b column = %d, want 6", col)
	}
	if m.composer.Value() != "hello world" {
		t.Fatalf("alt+b mutated value to %q", m.composer.Value())
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}, Alt: true})
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

	// ctrl+p remains palette on left focus even with composer text.
	m.composer.SetValue("draft")
	m.composer.SetCursor(2)
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if _, ok := m.modal.(*paletteModal); !ok {
		t.Fatalf("left-focus ctrl+p modal = %T, want palette", m.modal)
	}
	if m.composer.Value() != "draft" {
		t.Fatalf("palette stole composer edit: %q", m.composer.Value())
	}
	m.modal = nil

	// Right-focus ctrl+k still cycles windows.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right", m.focus)
	}
	before := m.windows.index
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
	if m.windows.index == before {
		t.Fatalf("right-focus ctrl+k did not cycle windows")
	}
	if m.composer.Value() != "draft" {
		t.Fatalf("right-focus ctrl+k edited composer: %q", m.composer.Value())
	}

	// Vertical split: mid-line kill wins; EOL falls through to focus bottom.
	m.focus = focusLeft
	m.composer.Focus()
	m.splitOrientation = orientVertical
	m.keyMap.applyOrientationKeys(orientVertical)
	m.composer.SetValue("alpha beta")
	m.composer.SetCursor(5)
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
	if m.focus != focusLeft {
		t.Fatalf("vertical left ctrl+k mid-line focus = %v, want left", m.focus)
	}
	if got := m.composer.Value(); got != "alpha" {
		t.Fatalf("vertical left ctrl+k = %q, want alpha", got)
	}
	m.composer.SetValue("alpha")
	m.composer.SetCursor(5) // EOL — no kill, focus bottom
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
	if m.focus != focusRight {
		t.Fatalf("vertical left ctrl+k at EOL focus = %v, want right", m.focus)
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
