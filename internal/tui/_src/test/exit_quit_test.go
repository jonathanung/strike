package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestExitQuitInCommandCatalogAndReserved(t *testing.T) {
	found := map[string]bool{}
	for _, spec := range builtinCommandSpecs {
		switch {
		case spec.ID == commandExit && spec.Name == "/exit":
			found["exit"] = true
			if spec.Description != "quit strike" {
				t.Errorf("/exit description = %q", spec.Description)
			}
		case spec.ID == commandQuit && spec.Name == "/quit":
			found["quit"] = true
			if spec.Description != "quit strike" {
				t.Errorf("/quit description = %q", spec.Description)
			}
		}
	}
	if !found["exit"] {
		t.Fatal("/exit missing from builtinCommandSpecs")
	}
	if !found["quit"] {
		t.Fatal("/quit missing from builtinCommandSpecs")
	}
	for _, name := range []string{"exit", "quit"} {
		if _, ok := reservedCommandNames[name]; !ok {
			t.Errorf("%s not reserved", name)
		}
		if validSkillName(name) {
			t.Errorf("validSkillName(%q) = true, want false", name)
		}
	}
}

func TestExitQuitHandleCommandReturnsTeaQuit(t *testing.T) {
	for _, name := range []string{"/exit", "/quit"} {
		t.Run(name, func(t *testing.T) {
			m, ops := newAppTestModel(nil, nil)
			next, cmd := m.handleCommand(name)
			if cmd == nil {
				t.Fatal("expected quit cmd")
			}
			if _, ok := runAppCmd(t, cmd).(tea.QuitMsg); !ok {
				t.Fatalf("%s cmd is not tea.Quit", name)
			}
			nm := next.(Model)
			if nm.PendingUpgrade() {
				t.Error("PendingUpgrade set by quit command")
			}
			if nm.PendingResume() != "" {
				t.Errorf("PendingResume = %q, want empty", nm.PendingResume())
			}
			assertNoAppOp(t, ops)
		})
	}
}
