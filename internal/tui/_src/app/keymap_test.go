package tui

import (
	"strings"

	"testing"

	"charm.land/bubbles/v2/key"

	tea "charm.land/bubbletea/v2"
)

func TestDefaultKeyMapBindingsMatchTheirRequiredKeysAndHaveHelp(t *testing.T) {

	keys := defaultKeyMap()

	tests := []struct {
		name string

		binding key.Binding

		msg tea.KeyPressMsg
	}{

		{"quit", keys.Quit, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}},

		{"focus left", keys.FocusLeft, tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl}},

		{"focus right", keys.FocusRight, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl}},

		{"window next ctrl+p", keys.CycleWindowNext, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}},

		{"window prev ctrl+o", keys.CycleWindowPrev, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}},

		{"group next ctrl+shift+o", keys.CycleGroupNext, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl | tea.ModShift}},

		{"group prev ctrl+shift+p", keys.CycleGroupPrev, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl | tea.ModShift}},

		{"palette", keys.Palette, tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}},

		{"keyhelp", keys.KeyHelp, tea.KeyPressMsg{Code: tea.KeyF1}},

		{"interrupt", keys.Interrupt, tea.KeyPressMsg{Code: tea.KeyEsc}},

		{"send", keys.Send, tea.KeyPressMsg{Code: tea.KeyEnter}},

		{"newline alt+enter", keys.Newline, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}},

		{"newline bare LF ctrl+j", keys.Newline, tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}},

		{"newline enhanced ctrl+j", keys.Newline, keyMsgAltJ()},

		{"tool expand alt+enter", keys.ToolExpand, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}},

		{"external editor", keys.ExternalEditor, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl}},
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

	if keys.CycleWindowNext.Help().Key != "ctrl+p" {

		t.Errorf("CycleWindowNext help key = %q, want ctrl+p", keys.CycleWindowNext.Help().Key)

	}

	if keys.CycleWindowPrev.Help().Key != "ctrl+o" {

		t.Errorf("CycleWindowPrev help key = %q, want ctrl+o", keys.CycleWindowPrev.Help().Key)

	}

	if keys.CycleGroupNext.Help().Key != "ctrl+shift+o" {

		t.Errorf("CycleGroupNext help key = %q, want ctrl+shift+o", keys.CycleGroupNext.Help().Key)

	}

	if keys.CycleGroupPrev.Help().Key != "ctrl+shift+p" {

		t.Errorf("CycleGroupPrev help key = %q, want ctrl+shift+p", keys.CycleGroupPrev.Help().Key)

	}

	// ctrl+o/p must not match group cycle; ctrl+shift+o/p must not match window cycle (#671).
	if key.Matches(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}, keys.CycleGroupNext, keys.CycleGroupPrev) {
		t.Error("ctrl+o must not match CycleGroup*")
	}
	if key.Matches(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}, keys.CycleGroupNext, keys.CycleGroupPrev) {
		t.Error("ctrl+p must not match CycleGroup*")
	}
	if key.Matches(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl | tea.ModShift}, keys.CycleWindowNext, keys.CycleWindowPrev) {
		t.Error("ctrl+shift+o must not match CycleWindow*")
	}
	if key.Matches(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl | tea.ModShift}, keys.CycleWindowNext, keys.CycleWindowPrev) {
		t.Error("ctrl+shift+p must not match CycleWindow*")
	}

	if keys.Palette.Help().Key != "ctrl+k" {

		t.Errorf("Palette help key = %q, want ctrl+k", keys.Palette.Help().Key)

	}

	// ctrl+j / bare LF / enhanced alt+j are newline, never pane cycle (#414).

	if key.Matches(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}, keys.CycleWindowNext, keys.CycleWindowPrev) {

		t.Error("bare KeyCtrlJ must not match CycleWindow* (#414)")

	}

	if key.Matches(keyMsgAltJ(), keys.CycleWindowNext, keys.CycleWindowPrev) {

		t.Error("alt+j (enhanced ctrl+j) must not match CycleWindow* (#414)")

	}

	if key.Matches(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}, keys.Palette) {

		t.Error("ctrl+p must not match Palette (window-next) (#414, #1009)")

	}

	if key.Matches(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}, keys.Palette) {

		t.Error("ctrl+o must not match Palette (window-prev) (#414, #1009)")

	}

	newlineHelp := keys.Newline.Help()

	if newlineHelp.Key != "ctrl+j/shift+enter/alt+enter" {

		t.Errorf("Newline help key = %q, want ctrl+j/shift+enter/alt+enter", newlineHelp.Key)

	}

	if newlineHelp.Desc != "newline" {

		t.Errorf("Newline help desc = %q, want newline", newlineHelp.Desc)

	}

	// alt+enter is first-class newline (same KeyMsg as post-CSI shift+enter) (#414).

	if !key.Matches(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}, keys.Newline) {

		t.Error("KeyEnter+Alt (alt+enter / shift+enter) must match Newline")

	}

	// ToolExpand shares alt+enter with Newline; routing is composer-empty only (#421).

	if keys.ToolExpand.Help().Key != "alt+enter" {

		t.Errorf("ToolExpand help key = %q, want alt+enter", keys.ToolExpand.Help().Key)

	}

	if key.Matches(tea.KeyPressMsg{Code: tea.KeyEnter}, keys.ToolExpand) {

		t.Error("bare enter must not match ToolExpand (#421)")

	}

}

// keyMsgAltJ is the post-WrapInput KeyMsg for enhanced ctrl+j (#240).

func keyMsgAltJ() tea.KeyPressMsg {

	return tea.KeyPressMsg{Code: 'j', Mod: tea.ModAlt}

}

// TestAltEnterAndShiftEnterNewline pins that KeyEnter+Alt (native alt+enter

// and post-WrapInput shift+enter) matches Newline and ToolExpand (shared

// chord, context-routed) — never CycleWindow*/Send/Focus* — under both split

// orientations (#53, #414, #421).

func TestAltEnterAndShiftEnterNewline(t *testing.T) {

	msg := tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}

	for _, tt := range []struct {
		name string

		orient splitOrientation
	}{

		{"horizontal", orientHorizontal},

		{"vertical", orientVertical},
	} {

		t.Run(tt.name, func(t *testing.T) {

			keys := defaultKeyMap()

			keys.applyOrientationKeys(tt.orient)

			if !key.Matches(msg, keys.Newline) {

				t.Error("KeyEnter+Alt (alt+enter/shift+enter) must match Newline")

			}

			if !key.Matches(msg, keys.ToolExpand) {

				t.Error("KeyEnter+Alt must match ToolExpand (#421)")

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
		"nav.group-next", "nav.group-prev",

		"global.palette", "global.keyhelp", "global.copy-last", "composer.external-editor",

		"composer.kill-word", "composer.word-back", "composer.word-fwd",

		"composer.kill-line-start", "composer.kill-line-end", "composer.yank",

		"agents.move", "agents.open", "agents.spawn", "agents.interrupt", "agents.rename", "agents.hide", "agents.filter", "agents.pet",
	} {

		if !seen[id] {

			t.Errorf("catalog missing %q", id)

		}

	}

	ak := defaultAgentsKeyMap()

	for _, tt := range []struct {
		id string

		b key.Binding
	}{

		{"agents.spawn", ak.Spawn},

		{"agents.open", ak.Open},

		{"agents.interrupt", ak.Interrupt},

		{"agents.rename", ak.Rename},

		{"agents.hide", ak.Hide},

		{"agents.move", ak.Move},

		{"agents.filter", ak.Filter},

		{"agents.pet", ak.Pet},
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
		name string

		focus paneFocus

		windowID string

		label string

		wantCat string

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

	agentIDs := []string{"agents.move", "agents.open", "agents.spawn", "agents.interrupt", "agents.rename", "agents.hide", "agents.filter", "agents.pet"}

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
func TestKeybindOverridesChangeJumpBottomAndCheatsheet(t *testing.T) {

	overrides := map[string][]string{"nav.jump-bottom": {"ctrl+b"}}

	m, _ := newAppTestModelWithOptions(Options{Keybinds: overrides})

	if !key.Matches(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}, m.keyMap.JumpBottom) {

		t.Fatal("ctrl+b should match JumpBottom after override")

	}

	if key.Matches(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}, m.keyMap.JumpBottom) {

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

	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyF1})

	modal, ok := m.modal.(*keybindEditor)

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

	if !key.Matches(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}, m.keyMap.JumpBottom) {

		t.Fatal("precondition: override active")

	}

	m.composer.SetValue("/keys reset")

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	m = updated.(Model)

	if !key.Matches(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}, m.keyMap.JumpBottom) {

		t.Fatal("after reset, ctrl+t should match JumpBottom")

	}

	if key.Matches(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}, m.keyMap.JumpBottom) {

		t.Fatal("after reset, ctrl+b should not match JumpBottom")

	}

	if m.notice == "" || !strings.Contains(m.notice, "reset") {

		t.Fatalf("notice = %q", m.notice)

	}

}

func TestBuildKeyMapOrientationPreservesOverrides(t *testing.T) {

	overrides := map[string][]string{

		"nav.focus-left": {"alt+h"},

		"nav.window-next": {"alt+o"},
	}

	horiz := buildKeyMap(overrides, orientHorizontal)

	if !key.Matches(tea.KeyPressMsg{Code: 'h', Mod: tea.ModAlt}, horiz.FocusLeft) {

		t.Fatal("horizontal focus-left override")

	}

	if !key.Matches(tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt}, horiz.CycleWindowNext) {

		t.Fatal("horizontal window-next override")

	}

	vert := buildKeyMap(overrides, orientVertical)

	// Orientation-independent: overrides stay on the same actions (#414).

	if !key.Matches(tea.KeyPressMsg{Code: 'h', Mod: tea.ModAlt}, vert.FocusLeft) {

		t.Fatal("vertical focus-left should keep override")

	}

	if !key.Matches(tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt}, vert.CycleWindowNext) {

		t.Fatal("vertical window-next should keep override")

	}

	if key.Matches(tea.KeyPressMsg{Code: 'o', Mod: tea.ModAlt}, vert.FocusLeft) {

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

func TestRootSwitcherDefaultBinding(t *testing.T) {
	km := defaultKeyMap()
	if !key.Matches(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}, km.RootSwitcher) {
		t.Fatal("ctrl+s should match RootSwitcher by default")
	}
	help := km.RootSwitcher.Help()
	if help.Key != "ctrl+s" {
		t.Fatalf("help key = %q, want ctrl+s", help.Key)
	}
	if help.Desc != "switch session" {
		t.Fatalf("help desc = %q, want switch session", help.Desc)
	}
}

func TestRootSwitcherOverride(t *testing.T) {
	overrides := map[string][]string{"nav.root-switcher": {"ctrl+\\"}}
	m, _ := newAppTestModelWithOptions(Options{Keybinds: overrides})
	if !key.Matches(tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl}, m.keyMap.RootSwitcher) {
		t.Fatal("ctrl+\\ should match RootSwitcher after override")
	}
	if key.Matches(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}, m.keyMap.RootSwitcher) {
		t.Fatal("ctrl+s should not match RootSwitcher after override")
	}
	help := m.keyMap.RootSwitcher.Help()
	if help.Key != "ctrl+\\" {
		t.Fatalf("help key = %q, want ctrl+\\", help.Key)
	}
}

func TestRootSwitcherInCatalog(t *testing.T) {
	km := defaultKeyMap()
	catalog := keybindCatalog(km)
	found := false
	for _, e := range catalog {
		if e.ID == "nav.root-switcher" {
			found = true
			if e.Keys != "ctrl+s" {
				t.Fatalf("catalog keys = %q, want ctrl+s", e.Keys)
			}
			if e.Action != "switch session" {
				t.Fatalf("catalog action = %q, want switch session", e.Action)
			}
			if e.Slash != "" {
				t.Fatalf("catalog slash = %q, want empty (keybind only)", e.Slash)
			}
		}
	}
	if !found {
		t.Fatal("catalog missing nav.root-switcher")
	}
}
