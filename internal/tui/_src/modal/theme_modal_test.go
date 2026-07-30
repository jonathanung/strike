package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func testThemeEntries() []theme.Entry {
	return []theme.Entry{
		{ID: "strike", Name: "Strike", Source: "builtin"},
		{ID: "dracula", Name: "Dracula", Source: "builtin"},
		{ID: "custom-dark", Name: "Custom Dark", Source: "user"},
	}
}

func TestThemeModalUpdateKeys(t *testing.T) {
	entries := testThemeEntries()
	tests := []struct {
		name       string
		current    string
		setup      func(*themeModal)
		keys       []tea.KeyPressMsg
		wantCursor int
		wantFilter string
		wantClose  bool
		wantCmd    string // "select" | "save" | ""
	}{
		{
			name:       "down moves cursor",
			current:    "strike",
			keys:       []tea.KeyPressMsg{{Code: tea.KeyDown}},
			wantCursor: 1,
		},
		{
			name:       "j moves cursor",
			current:    "strike",
			keys:       []tea.KeyPressMsg{{Text: "j"}},
			wantCursor: 1,
		},
		{
			name:       "up at top stays",
			current:    "strike",
			keys:       []tea.KeyPressMsg{{Code: tea.KeyUp}},
			wantCursor: 0,
		},
		{
			name:    "up moves cursor",
			current: "strike",
			setup: func(m *themeModal) {
				m.cursor = 2
			},
			keys:       []tea.KeyPressMsg{{Code: tea.KeyUp}},
			wantCursor: 1,
		},
		{
			name:    "k moves cursor up",
			current: "strike",
			setup: func(m *themeModal) {
				m.cursor = 2
			},
			keys:       []tea.KeyPressMsg{{Text: "k"}},
			wantCursor: 1,
		},
		{
			name:       "ctrl+n moves down",
			current:    "strike",
			keys:       []tea.KeyPressMsg{{Code: 'n', Mod: tea.ModCtrl}},
			wantCursor: 1,
		},
		{
			name:    "ctrl+p moves up",
			current: "strike",
			setup: func(m *themeModal) {
				m.cursor = 2
			},
			keys:       []tea.KeyPressMsg{{Code: 'p', Mod: tea.ModCtrl}},
			wantCursor: 1,
		},
		{
			name:       "tab moves down",
			current:    "strike",
			keys:       []tea.KeyPressMsg{{Code: tea.KeyTab}},
			wantCursor: 1,
		},
		{
			name:    "down at bottom stays",
			current: "strike",
			setup: func(m *themeModal) {
				m.cursor = len(entries) - 1
			},
			keys:       []tea.KeyPressMsg{{Code: tea.KeyDown}},
			wantCursor: len(entries) - 1,
		},
		{
			name:       "type filters and resets cursor",
			current:    "strike",
			setup:      func(m *themeModal) { m.cursor = 2 },
			keys:       []tea.KeyPressMsg{{Text: "d"}, {Text: "r"}},
			wantCursor: 0,
			wantFilter: "dr",
		},
		{
			name: "backspace trims filter",
			setup: func(m *themeModal) {
				m.filter = "dra"
				m.refilter()
				m.cursor = 0
			},
			keys:       []tea.KeyPressMsg{{Code: tea.KeyBackspace}},
			wantCursor: 0,
			wantFilter: "dr",
		},
		{
			name:       "backspace on empty filter is noop",
			current:    "strike",
			keys:       []tea.KeyPressMsg{{Code: tea.KeyBackspace}},
			wantCursor: 0,
			wantFilter: "",
		},
		{
			name:      "esc closes",
			current:   "strike",
			keys:      []tea.KeyPressMsg{{Code: tea.KeyEsc}},
			wantClose: true,
		},
		{
			name:      "q closes",
			current:   "strike",
			keys:      []tea.KeyPressMsg{{Text: "q"}},
			wantClose: true,
		},
		{
			name:      "enter selects and closes",
			current:   "strike",
			keys:      []tea.KeyPressMsg{{Code: tea.KeyEnter}},
			wantClose: true,
			wantCmd:   "select",
		},
		{
			name:    "ctrl+d saves without closing",
			current: "strike",
			keys:    []tea.KeyPressMsg{{Code: 'd', Mod: tea.ModCtrl}},
			wantCmd: "save",
		},
		{
			name: "enter on empty filtered stays open",
			setup: func(m *themeModal) {
				m.filter = "zzz-no-match"
				m.refilter()
			},
			keys:       []tea.KeyPressMsg{{Code: tea.KeyEnter}},
			wantCursor: 0,
			wantFilter: "zzz-no-match",
		},
		{
			name: "ctrl+d on empty filtered stays open",
			setup: func(m *themeModal) {
				m.filter = "zzz-no-match"
				m.refilter()
			},
			keys:       []tea.KeyPressMsg{{Code: 'd', Mod: tea.ModCtrl}},
			wantCursor: 0,
			wantFilter: "zzz-no-match",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newThemeModal(entries, tt.current, &fakeSettings{})
			if tt.setup != nil {
				tt.setup(m)
			}
			var next modal = m
			var cmd tea.Cmd
			for _, key := range tt.keys {
				got, c := next.(*themeModal).update(key)
				next, cmd = got, c
				if next == nil {
					break
				}
			}
			if tt.wantClose {
				if next != nil {
					t.Fatalf("want closed, got %T", next)
				}
			} else {
				got, ok := next.(*themeModal)
				if !ok {
					t.Fatalf("want *themeModal, got %T", next)
				}
				if got.cursor != tt.wantCursor {
					t.Errorf("cursor = %d, want %d", got.cursor, tt.wantCursor)
				}
				if got.filter != tt.wantFilter {
					t.Errorf("filter = %q, want %q", got.filter, tt.wantFilter)
				}
			}
			switch tt.wantCmd {
			case "select":
				if cmd == nil {
					t.Fatal("expected select cmd")
				}
				if _, ok := cmd().(themeSelectedMsg); !ok {
					t.Fatalf("cmd msg type unexpected")
				}
			case "save":
				if cmd == nil {
					t.Fatal("expected save cmd")
				}
				if _, ok := cmd().(themeSavedMsg); !ok {
					t.Fatalf("cmd msg type unexpected")
				}
			case "":
				if cmd != nil && next != nil {
					// empty filtered enter/ctrl+d return nil cmd
					msg := cmd()
					t.Fatalf("unexpected cmd msg %T", msg)
				}
			}
		})
	}
}

func TestThemeModalRefilterByIDAndName(t *testing.T) {
	entries := testThemeEntries()
	tests := []struct {
		filter string
		want   []string // IDs
	}{
		{"", []string{"strike", "dracula", "custom-dark"}},
		{"drac", []string{"dracula"}},
		{"custom", []string{"custom-dark"}},
		{"Dark", []string{"custom-dark"}},
		{"user", []string{}},
		{"zzz", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.filter, func(t *testing.T) {
			m := newThemeModal(entries, "strike", nil)
			m.filter = tt.filter
			m.cursor = 99
			m.refilter()
			if len(m.filtered) != len(tt.want) {
				t.Fatalf("filtered len = %d, want %d (%v)", len(m.filtered), len(tt.want), idsOf(m.filtered))
			}
			for i, id := range tt.want {
				if m.filtered[i].ID != id {
					t.Errorf("filtered[%d] = %q, want %q", i, m.filtered[i].ID, id)
				}
			}
			if len(m.filtered) == 0 {
				if m.cursor != 0 {
					t.Errorf("empty-result cursor = %d, want 0", m.cursor)
				}
			} else if tt.filter != "" && m.cursor >= len(m.filtered) {
				// Non-empty filter clamps an OOB cursor; empty filter leaves it.
				t.Errorf("cursor %d out of range len=%d", m.cursor, len(m.filtered))
			}
		})
	}
}

func idsOf(entries []theme.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}

func TestThemeModalView(t *testing.T) {
	entries := testThemeEntries()
	tests := []struct {
		name    string
		current string
		setup   func(*themeModal)
		want    []string
	}{
		{
			name:    "lists themes and marks current",
			current: "dracula",
			want:    []string{"Select theme", "Strike", "Dracula", "Custom Dark", "builtin"},
		},
		{
			name:    "shows filter and empty state",
			current: "strike",
			setup: func(m *themeModal) {
				m.filter = "nope"
				m.refilter()
			},
			want: []string{"Select theme", "no themes found", "nope"},
		},
		{
			name:    "name differs from id shows id in detail",
			current: "strike",
			want:    []string{"Custom Dark", "custom-dark"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newThemeModal(entries, tt.current, &fakeSettings{})
			if tt.setup != nil {
				tt.setup(m)
			}
			plain := ansi.Strip(m.view(72, theme.Default()))
			for _, w := range tt.want {
				if !strings.Contains(plain, w) {
					t.Errorf("view missing %q:\n%s", w, plain)
				}
			}
		})
	}
}

func TestThemeModalEnterSelectsEntry(t *testing.T) {
	m := newThemeModal(testThemeEntries(), "strike", nil)
	for i, e := range m.filtered {
		if e.ID == "dracula" {
			m.cursor = i
			break
		}
	}
	next, cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if next != nil {
		t.Fatalf("enter left modal open: %T", next)
	}
	if cmd == nil {
		t.Fatal("expected select cmd")
	}
	msg, ok := cmd().(themeSelectedMsg)
	if !ok || msg.entry.ID != "dracula" {
		t.Fatalf("msg = %#v", msg)
	}
}

func TestThemeModalCtrlDSavesDefault(t *testing.T) {
	settings := &fakeSettings{}
	m := newThemeModal(testThemeEntries(), "strike", settings)
	for i, e := range m.filtered {
		if e.ID == "custom-dark" {
			m.cursor = i
			break
		}
	}
	next, cmd := m.update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if next == nil {
		t.Fatal("ctrl+d closed modal")
	}
	if cmd == nil {
		t.Fatal("expected save cmd")
	}
	msg, ok := cmd().(themeSavedMsg)
	if !ok || msg.err != nil || msg.id != "custom-dark" {
		t.Fatalf("msg = %#v", msg)
	}
	if len(settings.savedThemes) != 1 || settings.savedThemes[0] != "custom-dark" {
		t.Fatalf("savedThemes = %v", settings.savedThemes)
	}
}

func TestSaveThemeThroughCmdNilSettings(t *testing.T) {
	msg := saveThemeThroughCmd(nil, "dracula")()
	saved, ok := msg.(themeSavedMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	if !errors.Is(saved.err, errNoSettings) {
		t.Errorf("err = %v, want errNoSettings", saved.err)
	}
	if saved.id != "dracula" {
		t.Errorf("id = %q", saved.id)
	}
}

func TestNewThemeModalCursorOnCurrent(t *testing.T) {
	m := newThemeModal(testThemeEntries(), "custom-dark", nil)
	if m.filtered[m.cursor].ID != "custom-dark" {
		t.Errorf("cursor on %q, want custom-dark", m.filtered[m.cursor].ID)
	}
}
