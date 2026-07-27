package tui

import (
	"strings"
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
		{"window next ctrl+o", keys.CycleWindowNext, tea.KeyMsg{Type: tea.KeyCtrlO}},
		{"window prev ctrl+p", keys.CycleWindowPrev, tea.KeyMsg{Type: tea.KeyCtrlP}},
		{"palette", keys.Palette, tea.KeyMsg{Type: tea.KeyCtrlK}},
		{"keyhelp", keys.KeyHelp, tea.KeyMsg{Type: tea.KeyF1}},
		{"interrupt", keys.Interrupt, tea.KeyMsg{Type: tea.KeyEsc}},
		{"send", keys.Send, tea.KeyMsg{Type: tea.KeyEnter}},
		{"newline alt+enter", keys.Newline, tea.KeyMsg{Type: tea.KeyEnter, Alt: true}},
		{"newline bare LF ctrl+j", keys.Newline, tea.KeyMsg{Type: tea.KeyCtrlJ}},
		{"newline enhanced ctrl+j", keys.Newline, keyMsgAltJ()},
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
	if keys.CycleWindowNext.Help().Key != "ctrl+o" {
		t.Errorf("CycleWindowNext help key = %q, want ctrl+o", keys.CycleWindowNext.Help().Key)
	}
	if keys.CycleWindowPrev.Help().Key != "ctrl+p" {
		t.Errorf("CycleWindowPrev help key = %q, want ctrl+p", keys.CycleWindowPrev.Help().Key)
	}
	if keys.Palette.Help().Key != "ctrl+k" {
		t.Errorf("Palette help key = %q, want ctrl+k", keys.Palette.Help().Key)
	}
	// ctrl+j / bare LF / enhanced alt+j are newline, never pane cycle (#414).
	if key.Matches(tea.KeyMsg{Type: tea.KeyCtrlJ}, keys.CycleWindowNext, keys.CycleWindowPrev) {
		t.Error("bare KeyCtrlJ must not match CycleWindow* (#414)")
	}
	if key.Matches(keyMsgAltJ(), keys.CycleWindowNext, keys.CycleWindowPrev) {
		t.Error("alt+j (enhanced ctrl+j) must not match CycleWindow* (#414)")
	}
	if key.Matches(tea.KeyMsg{Type: tea.KeyCtrlP}, keys.Palette) {
		t.Error("ctrl+p must not match Palette (window-prev) (#414)")
	}
	newlineHelp := keys.Newline.Help()
	if newlineHelp.Key != "ctrl+j/shift+enter" {
		t.Errorf("Newline help key = %q, want ctrl+j/shift+enter", newlineHelp.Key)
	}
	if newlineHelp.Desc != "newline" {
		t.Errorf("Newline help desc = %q, want newline", newlineHelp.Desc)
	}
}

// keyMsgAltJ is the post-WrapInput KeyMsg for enhanced ctrl+j (#240).
func keyMsgAltJ() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}, Alt: true}
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
		"global.palette", "global.keyhelp", "global.copy-last", "composer.external-editor",
		"composer.kill-word", "composer.word-back", "composer.word-fwd",
		"composer.kill-line-start", "composer.kill-line-end", "composer.yank",
		"agents.move", "agents.open", "agents.spawn", "agents.interrupt", "agents.rename", "agents.hide", "agents.filter",
	} {
		if !seen[id] {
			t.Errorf("catalog missing %q", id)
		}
	}
	ak := defaultAgentsKeyMap()
	for _, tt := range []struct {
		id string
		b  key.Binding
	}{
		{"agents.spawn", ak.Spawn},
		{"agents.open", ak.Open},
		{"agents.interrupt", ak.Interrupt},
		{"agents.rename", ak.Rename},
		{"agents.hide", ak.Hide},
		{"agents.move", ak.Move},
		{"agents.filter", ak.Filter},
	} {
		help := tt.b.Help()
		found := false
		for _, e := range catalog {
			if e.ID != tt.id {
				continue
			}
			found = true
			if e.Keys != help.Key || e.Action != help.Desc {
				t.Errorf("%s = keys=%q action=%q, want keys=%q action=%q from agentsKeyMap",
					tt.id, e.Keys, e.Action, help.Key, help.Desc)
			}
		}
		if !found {
			t.Errorf("catalog missing %q", tt.id)
		}
	}
	if keys.Agent.Help().Desc != "cycle agent persona" {
		t.Errorf("Agent help desc = %q, want cycle agent persona", keys.Agent.Help().Desc)
	}
	if keys.Leader.Help().Desc != "subagent leader" {
		t.Errorf("Leader help desc = %q, want subagent leader", keys.Leader.Help().Desc)
	}
	m := newKeysModal(keys, keysModalContext{})
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

func TestKeysModalContextForFocus(t *testing.T) {
	tests := []struct {
		name     string
		focus    paneFocus
		windowID string
		label    string
		wantCat  string
		wantPref string
	}{
		{"composer", focusLeft, "", "composer", "Composer", "nav.tool-"},
		{"agents", focusRight, agentsWindowID, "agents", "Agents", ""},
		{"editor", focusRight, terminalWindowID, "editor", "Editor", ""},
		{"files", focusRight, filesWindowID, "files", "Lists", ""},
		{"activity", focusRight, "activity", "activity", "Navigation", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := keysModalContextFor(tt.focus, tt.windowID)
			if ctx.Label != tt.label {
				t.Errorf("label = %q, want %q", ctx.Label, tt.label)
			}
			if tt.wantCat != "" {
				found := false
				for _, c := range ctx.Categories {
					if c == tt.wantCat {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("categories = %#v, want %q", ctx.Categories, tt.wantCat)
				}
			}
			if tt.wantPref != "" {
				found := false
				for _, p := range ctx.IDPrefixes {
					if p == tt.wantPref {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("prefixes = %#v, want %q", ctx.IDPrefixes, tt.wantPref)
				}
			}
		})
	}
}

func TestOrderKeybindEntriesPromotesFocusContext(t *testing.T) {
	keys := defaultKeyMap()
	catalog := keybindCatalog(keys)
	if len(catalog) < 10 {
		t.Fatalf("catalog too small: %d", len(catalog))
	}

	agents := orderKeybindEntries(catalog, keysModalContextFor(focusRight, agentsWindowID))
	if len(agents) != len(catalog) {
		t.Fatalf("agents order length = %d, want %d (must not drop binds)", len(agents), len(catalog))
	}
	agentIDs := []string{"agents.move", "agents.open", "agents.spawn", "agents.interrupt", "agents.rename", "agents.hide", "agents.filter"}
	for i, id := range agentIDs {
		if agents[i].ID != id {
			t.Fatalf("agents[%d] = %q, want %q", i, agents[i].ID, id)
		}
		if !agents[i].Context {
			t.Errorf("%s Context = false, want true", id)
		}
	}
	for i := len(agentIDs); i < len(agents); i++ {
		if agents[i].Context {
			t.Errorf("non-focus row %q unexpectedly Context", agents[i].ID)
		}
		if strings.HasPrefix(agents[i].ID, "agents.") {
			t.Errorf("agent bind %q after focus section", agents[i].ID)
		}
	}

	composer := orderKeybindEntries(catalog, keysModalContextFor(focusLeft, ""))
	if len(composer) != len(catalog) {
		t.Fatalf("composer order length = %d, want %d", len(composer), len(catalog))
	}
	if !composer[0].Context || composer[0].Category != "Composer" {
		t.Fatalf("composer[0] = %#v, want first Composer context row", composer[0])
	}
	if composer[0].ID != "composer.send" {
		t.Fatalf("composer[0].ID = %q, want composer.send", composer[0].ID)
	}
	sawComposer := false
	sawPref := false
	sawNonContext := false
	for _, e := range composer {
		if e.Context {
			if sawNonContext {
				t.Fatalf("context row %q after non-context section", e.ID)
			}
			switch {
			case e.Category == "Composer":
				if sawPref {
					t.Errorf("Composer row %q after prefix rows", e.ID)
				}
				sawComposer = true
			case strings.HasPrefix(e.ID, "nav.scroll-"),
				strings.HasPrefix(e.ID, "nav.jump-"),
				strings.HasPrefix(e.ID, "nav.tool-"):
				sawPref = true
			default:
				t.Errorf("unexpected context row %q cat=%q", e.ID, e.Category)
			}
		} else {
			sawNonContext = true
			if e.Category == "Composer" {
				t.Errorf("composer bind %q not promoted", e.ID)
			}
		}
	}
	if !sawComposer {
		t.Fatal("no Composer category rows in focus section")
	}
	if !sawPref {
		t.Fatal("no transcript scroll/tool prefix rows in focus section")
	}

	// Empty context is a no-op.
	plain := orderKeybindEntries(catalog, keysModalContext{})
	for i := range plain {
		if plain[i].ID != catalog[i].ID || plain[i].Context {
			t.Fatalf("empty context changed catalog at %d: got %#v want %#v", i, plain[i], catalog[i])
		}
	}
}

func TestKeysModalOpensWithFocusContext(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	// Default left/composer focus → composer binds first.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyF1})
	modal, ok := m.modal.(*keysModal)
	if !ok {
		t.Fatalf("f1 modal = %T, want keysModal", m.modal)
	}
	if modal.contextLabel != "composer" {
		t.Fatalf("contextLabel = %q, want composer", modal.contextLabel)
	}
	list := modal.filtered()
	if len(list) == 0 || !list[0].Context || list[0].Category != "Composer" {
		t.Fatalf("composer focus first row = %#v, want Composer context", firstEntry(list))
	}
	view := modal.view(60, m.th)
	if !strings.Contains(view, "Current focus") {
		t.Errorf("composer view missing Current focus section: %q", view)
	}
	if !strings.Contains(view, "composer") {
		t.Errorf("composer view missing title context: %q", view)
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEsc})

	// Agents pane focused → agent root controls first.
	reg, ok := m.windows.activate(agentsWindowID)
	if !ok {
		t.Fatal("activate agents")
	}
	m.windows = reg
	m.focus = focusRight
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyF1})
	modal, ok = m.modal.(*keysModal)
	if !ok {
		t.Fatalf("agents f1 modal = %T", m.modal)
	}
	if modal.contextLabel != "agents" {
		t.Fatalf("contextLabel = %q, want agents", modal.contextLabel)
	}
	list = modal.filtered()
	wantAgents := []string{"agents.move", "agents.open", "agents.spawn", "agents.interrupt", "agents.rename", "agents.hide", "agents.filter"}
	for i, id := range wantAgents {
		if i >= len(list) || list[i].ID != id || !list[i].Context {
			t.Fatalf("agents focus list[%d] = %#v, want %s context", i, firstEntry(list[i:]), id)
		}
	}
	view = modal.view(60, m.th)
	if !strings.Contains(view, "Current focus") {
		t.Errorf("agents view missing Current focus: %q", view)
	}
	// Agent actions appear in the leading section (spawn help text).
	if !strings.Contains(view, "new root") {
		t.Errorf("agents view missing spawn action: %q", view)
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEsc})

	// Switching focus and reopening updates the context section.
	m.focus = focusLeft
	_ = m.composer.Focus()
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyF1})
	modal, ok = m.modal.(*keysModal)
	if !ok {
		t.Fatalf("reopen modal = %T", m.modal)
	}
	if modal.contextLabel != "composer" {
		t.Fatalf("after switch contextLabel = %q, want composer", modal.contextLabel)
	}
	list = modal.filtered()
	if len(list) == 0 || list[0].ID == "agents.move" {
		t.Fatalf("after switch first row still agents: %#v", firstEntry(list))
	}
	if !list[0].Context || list[0].Category != "Composer" {
		t.Fatalf("after switch first = %#v, want Composer context", firstEntry(list))
	}
}

func firstEntry(list []keybindEntry) any {
	if len(list) == 0 {
		return nil
	}
	return list[0]
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

	// ctrl+o / ctrl+p cycle secondary panes from left focus without editing (#414).
	m.composer.SetValue("draft")
	m.composer.SetCursor(len("draft"))
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlO})
	if got := m.composer.Value(); got != "draft" {
		t.Fatalf("ctrl+o composer = %q, want draft", got)
	}
	if m.windows.index != 1 || m.windows.active().id() != "b" {
		t.Fatalf("left ctrl+o window = %d/%s, want 1/b", m.windows.index, m.windows.active().id())
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlO})
	if m.windows.index != 2 || m.windows.active().id() != "c" {
		t.Fatalf("left second ctrl+o window = %d/%s, want 2/c", m.windows.index, m.windows.active().id())
	}

	// ctrl+j inserts newline, does not cycle (#414).
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if got := m.composer.Value(); got != "draft\n" {
		t.Fatalf("ctrl+j composer = %q, want draft\\n", got)
	}
	if m.windows.active().id() != "c" {
		t.Fatalf("ctrl+j cycled window to %s", m.windows.active().id())
	}

	// Cycle windows with ctrl+o while the right pane is focused; ctrl+p prev.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if m.focus != focusRight {
		t.Fatalf("ctrl+l focus = %v, want right", m.focus)
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlO})
	if m.windows.index != 0 || m.windows.active().id() != "a" {
		t.Errorf("right ctrl+o window = %d/%s, want 0/a", m.windows.index, m.windows.active().id())
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlO})
	if m.windows.active().id() != "b" {
		t.Errorf("second right ctrl+o window = %s, want b", m.windows.active().id())
	}
	// Active is b; ctrl+p prev → a, then c.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if m.windows.active().id() != "a" {
		t.Errorf("ctrl+p window = %s, want a", m.windows.active().id())
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlP})
	if m.windows.active().id() != "c" {
		t.Errorf("second ctrl+p window = %s, want c", m.windows.active().id())
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

func TestKeybindOverridesChangeJumpBottomAndCheatsheet(t *testing.T) {
	overrides := map[string][]string{"nav.jump-bottom": {"ctrl+b"}}
	m, _ := newAppTestModelWithOptions(Options{Keybinds: overrides})
	if !key.Matches(tea.KeyMsg{Type: tea.KeyCtrlB}, m.keyMap.JumpBottom) {
		t.Fatal("ctrl+b should match JumpBottom after override")
	}
	if key.Matches(tea.KeyMsg{Type: tea.KeyCtrlT}, m.keyMap.JumpBottom) {
		t.Fatal("ctrl+t should no longer match JumpBottom")
	}
	help := m.keyMap.JumpBottom.Help()
	if help.Key != "ctrl+b" {
		t.Fatalf("JumpBottom help key = %q, want ctrl+b", help.Key)
	}
	catalog := keybindCatalog(m.keyMap)
	found := false
	for _, e := range catalog {
		if e.ID == "nav.jump-bottom" {
			found = true
			if e.Keys != "ctrl+b" {
				t.Fatalf("catalog jump-bottom keys = %q, want ctrl+b", e.Keys)
			}
		}
	}
	if !found {
		t.Fatal("catalog missing nav.jump-bottom")
	}
	// /keys modal shows effective chords.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyF1})
	modal, ok := m.modal.(*keysModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	for _, e := range modal.entries {
		if e.ID == "nav.jump-bottom" && e.Keys != "ctrl+b" {
			t.Fatalf("modal keys = %q", e.Keys)
		}
	}
}

func TestKeysResetRestoresDefaults(t *testing.T) {
	m, _ := newAppTestModelWithOptions(Options{
		Keybinds: map[string][]string{"nav.jump-bottom": {"ctrl+b"}},
	})
	if !key.Matches(tea.KeyMsg{Type: tea.KeyCtrlB}, m.keyMap.JumpBottom) {
		t.Fatal("precondition: override active")
	}
	m.composer.SetValue("/keys reset")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if !key.Matches(tea.KeyMsg{Type: tea.KeyCtrlT}, m.keyMap.JumpBottom) {
		t.Fatal("after reset, ctrl+t should match JumpBottom")
	}
	if key.Matches(tea.KeyMsg{Type: tea.KeyCtrlB}, m.keyMap.JumpBottom) {
		t.Fatal("after reset, ctrl+b should not match JumpBottom")
	}
	if m.notice == "" || !strings.Contains(m.notice, "reset") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestBuildKeyMapOrientationPreservesOverrides(t *testing.T) {
	overrides := map[string][]string{
		"nav.focus-left":  {"alt+h"},
		"nav.window-next": {"alt+o"},
	}
	horiz := buildKeyMap(overrides, orientHorizontal)
	if !key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}, Alt: true}, horiz.FocusLeft) {
		t.Fatal("horizontal focus-left override")
	}
	if !key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}, Alt: true}, horiz.CycleWindowNext) {
		t.Fatal("horizontal window-next override")
	}
	vert := buildKeyMap(overrides, orientVertical)
	// Orientation-independent: overrides stay on the same actions (#414).
	if !key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}, Alt: true}, vert.FocusLeft) {
		t.Fatal("vertical focus-left should keep override")
	}
	if !key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}, Alt: true}, vert.CycleWindowNext) {
		t.Fatal("vertical window-next should keep override")
	}
	if key.Matches(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}, Alt: true}, vert.FocusLeft) {
		t.Fatal("vertical must not swap window-next onto focus-left")
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
