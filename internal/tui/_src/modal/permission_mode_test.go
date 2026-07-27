package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestPermissionModeBadgeInHeader(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.permMode = protocol.PermissionModeYolo
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "yolo") {
		t.Fatalf("header missing yolo badge:\n%s", plain)
	}
	if !strings.Contains(plain, "DANGER: yolo mode") {
		t.Fatalf("yolo danger banner missing:\n%s", plain)
	}
}

func TestPermissionModeCommandDirect(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.composer.SetValue("/mode yolo")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)
	op := receiveAppOp(t, ops)
	want := protocol.SetPermissionMode{Mode: protocol.PermissionModeYolo}
	if op != want {
		t.Errorf("operation = %#v, want %#v", op, want)
	}
	if m.composer.Value() != "" {
		t.Error("mode command did not reset composer")
	}
}

func TestPermissionModeCommandAcceptsEveryMode(t *testing.T) {
	for _, mode := range protocol.PermissionModes() {
		t.Run(string(mode), func(t *testing.T) {
			m, ops := newAppTestModel(nil, nil)
			m.composer.SetValue("/mode " + string(mode))
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			runAppCmd(t, cmd)
			if op := receiveAppOp(t, ops); op != (protocol.SetPermissionMode{Mode: mode}) {
				t.Errorf("operation = %#v, want SetPermissionMode{%q}", op, mode)
			}
			if m.noticeErr {
				t.Errorf("mode %q produced an error notice: %s", mode, m.notice)
			}
		})
	}
}

func TestPermissionModeCommandOpensModal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.permMode = protocol.PermissionModeAcceptEdits
	m.composer.SetValue("/mode")
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	picker, ok := m.modal.(*permissionModeModal)
	if !ok {
		t.Fatalf("modal = %T, want *permissionModeModal", m.modal)
	}
	if picker.modes[picker.cursor] != protocol.PermissionModeAcceptEdits {
		t.Errorf("cursor on %q, want accept-edits", picker.modes[picker.cursor])
	}
	plain := ansi.Strip(m.View())
	for _, want := range []string{"default", "plan", "soft-approve", "accept-edits", "yolo"} {
		if !strings.Contains(plain, want) {
			t.Errorf("modal missing %q:\n%s", want, plain)
		}
	}
}

func TestSoftApproveModeSelectedShowsArmedChrome(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.applyEvent(protocol.PermissionModeSelected{Mode: protocol.PermissionModeSoftApprove})
	if m.permMode != protocol.PermissionModeSoftApprove {
		t.Fatalf("permMode = %q", m.permMode)
	}
	if got := m.effectivePermissionAutoApproveSeconds(); got != protocol.SoftApproveSeconds {
		t.Fatalf("effective seconds = %d, want %d", got, protocol.SoftApproveSeconds)
	}
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "soft") {
		t.Fatalf("missing soft badge:\n%s", plain)
	}
	if !strings.Contains(plain, "auto-allow") || !strings.Contains(plain, "15s") {
		t.Fatalf("missing auto-allow 15s badge:\n%s", plain)
	}
}

func TestPermissionModeSelectedUpdatesChrome(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.applyEvent(protocol.PermissionModeSelected{Mode: protocol.PermissionModeAcceptEdits})
	if m.permMode != protocol.PermissionModeAcceptEdits {
		t.Fatalf("permMode = %q, want accept-edits", m.permMode)
	}
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "edits") {
		t.Fatalf("header missing accept-edits short label:\n%s", plain)
	}
	if !strings.HasPrefix(m.notice, "mode:") {
		t.Fatalf("notice = %q, want mode: prefix", m.notice)
	}
}

func TestPermissionModeCommandUnknown(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.composer.SetValue("/mode nope")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)
	select {
	case op := <-ops:
		t.Fatalf("unknown mode sent %#v, want nothing", op)
	default:
	}
	if !m.noticeErr || !strings.Contains(m.notice, "unknown mode") {
		t.Fatalf("notice = %q err=%v, want unknown mode error", m.notice, m.noticeErr)
	}
}

func TestPermissionModeModalSelectSendsOp(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	picker := newPermissionModeModal(protocol.PermissionModeDefault, ops, &fakeSettings{})
	picker.update(tea.KeyMsg{Type: tea.KeyDown}) // plan
	picker.update(tea.KeyMsg{Type: tea.KeyDown}) // soft-approve
	picker.update(tea.KeyMsg{Type: tea.KeyDown}) // accept-edits
	next, cmd := picker.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next != nil {
		t.Fatalf("modal still open after enter: %T", next)
	}
	runAppCmd(t, cmd)
	op := receiveAppOp(t, ops)
	if op != (protocol.SetPermissionMode{Mode: protocol.PermissionModeAcceptEdits}) {
		t.Fatalf("op = %#v, want accept-edits", op)
	}
}

func TestPermissionModePickerCtrlDSavesOnlyTheModeDefault(t *testing.T) {
	settings := &fakeSettings{}
	picker := newPermissionModeModal(protocol.PermissionModeDefault, make(chan protocol.Op, 1), settings)
	picker.update(tea.KeyMsg{Type: tea.KeyDown}) // plan
	picker.update(tea.KeyMsg{Type: tea.KeyDown}) // soft-approve

	next, cmd := picker.update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if next == nil {
		t.Error("ctrl+d closed the picker, want it to stay open")
	}
	runAppCmd(t, cmd)

	if len(settings.saved) != 1 {
		t.Fatalf("saved defaults = %d, want 1", len(settings.saved))
	}
	got := settings.saved[0]
	want := savedDefaults{mode: "soft-approve"}
	if got != want {
		t.Errorf("saved = %#v, want %#v (provider/model/agent/effort untouched)", got, want)
	}
}

func TestPermissionModePickerHintListsSetDefault(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	picker := newPermissionModeModal(protocol.PermissionModeYolo, make(chan protocol.Op, 1), &fakeSettings{})
	out := ansi.Strip(picker.view(72, m.th))
	if !strings.Contains(out, "ctrl+d set default") {
		t.Errorf("picker hint missing ctrl+d set default:\n%s", out)
	}
}

// TestSaveDefaultsIncludesTheActivePermissionMode: global ctrl+d persists
// the whole current selection, permission mode included.
func TestSaveDefaultsIncludesTheActivePermissionMode(t *testing.T) {
	m, _ := newAppTestModel([]string{"build"}, nil)
	settings := m.services.Settings.(*fakeSettings)
	m.providerName, m.modelName, m.agentName = "anthropic", "claude-opus-5", "build"
	m.effort = protocol.EffortMax
	m.permMode = protocol.PermissionModeAcceptEdits

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	runAppCmd(t, cmd)

	if len(settings.saved) != 1 {
		t.Fatalf("ctrl+d saved %d defaults, want 1", len(settings.saved))
	}
	want := savedDefaults{
		provider: "anthropic",
		model:    "claude-opus-5",
		agent:    "build",
		effort:   "max",
		mode:     "accept-edits",
	}
	if settings.saved[0] != want {
		t.Errorf("saved = %#v, want %#v", settings.saved[0], want)
	}
}
