package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPaneSlashCommandsFocusNamedWindows(t *testing.T) {
	tests := []struct {
		command string
		wantID  string
	}{
		{"/agents", agentsWindowID},
		{"/activity", "activity"},
		{"/files", filesWindowID},
		{"/visualizer", visualizerWindowID},
		{"/system", telemetryWindowID},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
			if m.focus != focusLeft {
				t.Fatalf("start focus = %v, want left", m.focus)
			}
			// Leave a different window active so activate is observable.
			if reg, ok := m.windows.activate(memoryWindowID); ok {
				m.windows = reg
			}
			m.composer.SetValue(tt.command)
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			if cmd != nil {
				runAppCmd(t, cmd)
			}
			if m.windows.active().id() != tt.wantID {
				t.Fatalf("active window = %q, want %q", m.windows.active().id(), tt.wantID)
			}
			if m.focus != focusRight {
				t.Fatalf("focus = %v, want right", m.focus)
			}
			if m.composer.Value() != "" {
				t.Fatalf("composer = %q, want cleared", m.composer.Value())
			}
			if m.modal != nil {
				t.Fatalf("modal = %T, want nil", m.modal)
			}
		})
	}
}

func TestAgentsPaneSlashDoesNotOpenAgentPicker(t *testing.T) {
	m, _ := newAppTestModel([]string{"build", "plan"}, nil)
	m.agentName = "plan"
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m.composer.SetValue("/agents")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	if _, ok := m.modal.(*agentModal); ok {
		t.Fatal("/agents opened agent picker; want agents pane focus only")
	}
	if m.windows.active().id() != agentsWindowID {
		t.Fatalf("active = %q, want agents", m.windows.active().id())
	}
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right", m.focus)
	}

	// /agent remains persona select.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlH})
	m.composer.SetValue("/agent")
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if _, ok := m.modal.(*agentModal); !ok {
		t.Fatalf("modal = %T, want *agentModal after /agent", m.modal)
	}
}

func TestFocusRightWindowMissingID(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	next, cmd := m.focusRightWindow("no-such-pane")
	m = next.(Model)
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	if !strings.Contains(m.notice, "missing") {
		t.Fatalf("notice = %q, want missing pane", m.notice)
	}
	if m.focus == focusRight {
		t.Fatal("focus moved right on missing pane")
	}
}
