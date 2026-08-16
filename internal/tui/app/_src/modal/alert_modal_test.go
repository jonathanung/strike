package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

func TestStartupAlertOpensModal(t *testing.T) {
	const body = "Session worktree was not created because no git repository was detected."
	m, _ := newAppTestModelWithOptions(Options{StartupAlert: body})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updateApp(t, m, startupAlertMsg{})
	if m.modal == nil {
		t.Fatal("expected startup alert modal")
	}
	am, ok := m.modal.(*alertModal)
	if !ok {
		t.Fatalf("modal type = %T, want *alertModal", m.modal)
	}
	if am.body != body {
		t.Fatalf("body = %q", am.body)
	}
	if am.tone != ui.ToneWarning {
		t.Fatalf("tone = %v, want warning", am.tone)
	}
	plain := ansi.Strip(am.view(60, m.th))
	if !strings.Contains(plain, "Session worktree") {
		t.Fatalf("view missing title:\n%s", plain)
	}
	// Body wraps across box rows; match a stable fragment from each line.
	if !strings.Contains(plain, "was not created") || !strings.Contains(plain, "repository was detected") {
		t.Fatalf("view missing body:\n%s", plain)
	}
	// Dismiss via modal update (enter).
	next, cmd := am.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if next != nil {
		t.Fatal("modal should close on enter")
	}
	if cmd == nil {
		t.Fatal("want alertDismissedMsg cmd")
	}
	if _, ok := cmd().(alertDismissedMsg); !ok {
		t.Fatalf("cmd msg type wrong")
	}
	m.modal = nil
	m = updateApp(t, m, alertDismissedMsg{})
	if m.modal != nil {
		t.Fatalf("after dismiss without firstRun, modal = %T", m.modal)
	}
}

func TestStartupAlertDefersFirstRunUntilDismiss(t *testing.T) {
	const body = "no git repository was detected"
	m, _ := newAppTestModelWithOptions(Options{FirstRun: true, StartupAlert: body})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updateApp(t, m, startupAlertMsg{})
	if _, ok := m.modal.(*alertModal); !ok {
		t.Fatalf("want alert first, got %T", m.modal)
	}
	// firstRunSetup while alert is open must not steal the modal.
	m = updateApp(t, m, firstRunSetupMsg{})
	if _, ok := m.modal.(*alertModal); !ok {
		t.Fatalf("alert should remain, got %T", m.modal)
	}
	// Dismiss alert → first-run /ftue wizard via alertDismissedMsg.
	m.modal = nil
	m = updateApp(t, m, alertDismissedMsg{})
	if m.modal == nil {
		t.Fatal("expected first-run /ftue modal after alert dismiss")
	}
	if _, ok := m.modal.(*ftueModal); !ok {
		t.Fatalf("modal after dismiss = %T, want *ftueModal", m.modal)
	}
}

func TestAlertModalDismissKeys(t *testing.T) {
	for _, key := range []string{"enter", "esc", "q"} {
		am := newAlertModal("Session worktree", "body text", ui.ToneWarning)
		var msg tea.KeyPressMsg
		switch key {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "q":
			msg = tea.KeyPressMsg{Text: "q"}
		}
		next, cmd := am.update(msg)
		if next != nil {
			t.Fatalf("%s: modal still open", key)
		}
		if cmd == nil {
			t.Fatalf("%s: want dismiss cmd", key)
		}
		if _, ok := cmd().(alertDismissedMsg); !ok {
			t.Fatalf("%s: wrong msg type", key)
		}
	}
}
