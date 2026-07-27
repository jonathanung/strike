package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestKeybindActionsHaveSlashOrDocumentedException(t *testing.T) {
	catalog := keybindCatalog(defaultKeyMap())
	if len(catalog) == 0 {
		t.Fatal("empty keybind catalog")
	}
	slashNames := map[string]struct{}{}
	for _, spec := range builtinCommandSpecs {
		slashNames[spec.Name] = struct{}{}
	}
	for _, e := range catalog {
		primary, mapped := keybindSlashPrimary[e.ID]
		reason, excepted := keybindNoSlashReason[e.ID]
		switch {
		case mapped && excepted:
			t.Errorf("%s listed in both keybindSlashPrimary and keybindNoSlashReason", e.ID)
		case !mapped && !excepted:
			t.Errorf("%s missing from keybindSlashPrimary and keybindNoSlashReason", e.ID)
		case excepted:
			if reason == "" {
				t.Errorf("%s exception reason is empty", e.ID)
			}
			if e.Slash != "" {
				t.Errorf("%s exception has Slash=%q, want empty", e.ID, e.Slash)
			}
		case mapped:
			if primary == "" {
				t.Errorf("%s primary slash is empty", e.ID)
				continue
			}
			if e.Slash != primary {
				t.Errorf("%s catalog Slash=%q, want %q", e.ID, e.Slash, primary)
			}
			if _, ok := slashNames[primary]; !ok {
				t.Errorf("%s primary %s not in builtinCommandSpecs", e.ID, primary)
			}
			for _, alias := range keybindSlashAliases[e.ID] {
				if _, ok := slashNames[alias]; !ok {
					t.Errorf("%s alias %s not in builtinCommandSpecs", e.ID, alias)
				}
			}
		}
	}
	// Every primary/alias must be unique across actions (except intentional
	// multi-id sharing is not used — each slash maps one action).
	owner := map[string]string{}
	for id, slash := range keybindSlashPrimary {
		if prev, ok := owner[slash]; ok {
			t.Errorf("slash %s claimed by both %s and %s", slash, prev, id)
		}
		owner[slash] = id
	}
}

func TestKeybindBackedSlashCommandsAreReserved(t *testing.T) {
	for _, name := range keybindSlashReservedNames() {
		if _, ok := reservedCommandNames[name]; !ok {
			t.Errorf("reservedCommandNames missing %q", name)
		}
		if validSkillName(name) {
			t.Errorf("validSkillName(%q) = true, want false", name)
		}
	}
}

func TestKeybindSlashCommandsInvokeSameActions(t *testing.T) {
	t.Run("focus panes", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		next, _ := m.handleCommand("/focus-right")
		m = next.(Model)
		if m.focus != focusRight {
			t.Fatalf("focus = %v, want right", m.focus)
		}
		next, _ = m.handleCommand("/focus-left")
		m = next.(Model)
		if m.focus != focusLeft {
			t.Fatalf("focus = %v, want left", m.focus)
		}
	})

	t.Run("window cycle", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
		start := m.windows.index
		next, _ := m.handleCommand("/window-next")
		m = next.(Model)
		if m.windows.index == start && len(m.windows.windows) > 1 {
			t.Fatalf("window index stuck at %d", start)
		}
		next, _ = m.handleCommand("/window-prev")
		m = next.(Model)
		if m.windows.index != start {
			t.Fatalf("window index = %d, want %d after prev", m.windows.index, start)
		}
	})

	t.Run("palette and keys", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		next, _ := m.handleCommand("/palette")
		m = next.(Model)
		if _, ok := m.modal.(*paletteModal); !ok {
			t.Fatalf("modal = %T, want paletteModal", m.modal)
		}
		m.modal = nil
		next, _ = m.handleCommand("/interrupt")
		m = next.(Model)
		if m.notice == "" || !strings.Contains(m.notice, "nothing to interrupt") {
			t.Fatalf("idle interrupt notice = %q", m.notice)
		}
	})

	t.Run("interrupt running turn", func(t *testing.T) {
		m, ops := newAppTestModel(nil, nil)
		m.turnRunning = true
		_, cmd := m.handleCommand("/interrupt")
		if cmd == nil {
			t.Fatal("expected interrupt cmd")
		}
		runAppCmd(t, cmd)
		got := receiveAppOp(t, ops)
		if _, ok := got.(protocol.Interrupt); !ok {
			t.Fatalf("op = %#v, want Interrupt", got)
		}
	})

	t.Run("agent-next and mode-next", func(t *testing.T) {
		m, ops := newAppTestModel(nil, nil)
		m.agents = []string{"build", "plan"}
		m.agentName = "build"
		_, cmd := m.handleCommand("/agent-next")
		if cmd == nil {
			t.Fatal("expected select agent cmd")
		}
		runAppCmd(t, cmd)
		got := receiveAppOp(t, ops)
		sel, ok := got.(protocol.SelectAgent)
		if !ok || sel.Name != "plan" {
			t.Fatalf("op = %#v, want SelectAgent plan", got)
		}

		m, ops = newAppTestModel(nil, nil)
		m.permMode = protocol.PermissionModeDefault
		_, cmd = m.handleCommand("/mode-next")
		if cmd == nil {
			t.Fatal("expected set mode cmd")
		}
		runAppCmd(t, cmd)
		got = receiveAppOp(t, ops)
		if _, ok := got.(protocol.SetPermissionMode); !ok {
			t.Fatalf("op = %#v, want SetPermissionMode", got)
		}
	})

	t.Run("layout still works", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		start := m.splitOrientation
		next, _ := m.handleCommand("/layout")
		m = next.(Model)
		if m.splitOrientation == start {
			t.Fatal("layout did not toggle")
		}
	})

	t.Run("subagent nav notices", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		next, _ := m.handleCommand("/subagent")
		m = next.(Model)
		if m.notice == "" {
			t.Fatal("expected no-subagent notice")
		}
		next, _ = m.handleCommand("/parent")
		m = next.(Model)
		if m.notice == "" || !strings.Contains(m.notice, "already at root") {
			t.Fatalf("parent notice = %q", m.notice)
		}
	})

	t.Run("root-new spawns", func(t *testing.T) {
		m, _ := newAppTestModel(nil, nil)
		// spawnRoot may no-op without session services; just ensure no panic.
		_, _ = m.handleCommand("/root-new")
	})
}

func TestKeysModalShowsSlashCrossRef(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyF1})
	modal, ok := m.modal.(*keysModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	modal.filter = "focus-left"
	list := modal.filtered()
	if len(list) == 0 || list[0].ID != "nav.focus-left" {
		t.Fatalf("filter focus-left = %#v", list)
	}
	if list[0].Slash != "/focus-left" {
		t.Fatalf("Slash = %q", list[0].Slash)
	}
	view := modal.view(80, m.th)
	if !strings.Contains(view, "/focus-left") {
		t.Fatalf("keys modal view missing /focus-left: %q", view)
	}
}
