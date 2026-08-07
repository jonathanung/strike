package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPaneSlashCommandsFocusNamedWindows(t *testing.T) {
	tests := []struct {
		command string
		wantID  string
	}{
		{"/agents", agentsWindowID},
		{"/activity", "activity"},
		{"/queue", queueWindowID},
		{"/files", filesWindowID},
		{"/diagnostics", diagnosticsWindowID},
		{"/visualizer", visualizerWindowID},
		{"/system", telemetryWindowID},
		{"/pets", petsWindowID},
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
			updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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

func TestSystemSlashRequiresTelemetry(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.windows, _ = setTelemetryEnabled(m.windows, false)
	m.composer.SetValue("/system")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(m.notice, "telemetry off") {
		t.Fatalf("notice = %q, want telemetry off hint", m.notice)
	}
	if m.focus == focusRight && m.windows.active().id() == telemetryWindowID {
		t.Fatal("/system focused hidden telemetry pane")
	}
}

func TestTelemetrySlashToggle(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if !telemetryEnabled(m.windows) {
		t.Fatal("default telemetry off")
	}
	m.composer.SetValue("/telemetry status")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(m.notice, "on") {
		t.Fatalf("status notice = %q", m.notice)
	}
	m.composer.SetValue("/telemetry off")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if telemetryEnabled(m.windows) {
		t.Fatal("/telemetry off did not disable")
	}
	if !strings.Contains(m.notice, "off") {
		t.Fatalf("off notice = %q", m.notice)
	}
	m.composer.SetValue("/telemetry on")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	if !telemetryEnabled(m.windows) {
		t.Fatal("/telemetry on did not enable")
	}
	if !strings.Contains(m.notice, "on") {
		t.Fatalf("on notice = %q", m.notice)
	}
}

func TestAgentsPaneSlashDoesNotOpenAgentPicker(t *testing.T) {
	m, _ := newAppTestModel([]string{"build", "plan"}, nil)
	m.agentName = "plan"
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m.composer.SetValue("/agents")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	m.composer.SetValue("/agent")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
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
