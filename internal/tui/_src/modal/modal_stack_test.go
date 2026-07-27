package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestPermissionAskedDoesNotReplaceUserModal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	settings := newSettingsModal(m.services, m.ops, m.th, m.workDir)
	settings.cursor = 0
	m.modal = settings

	cmd := m.applyEvent(protocol.PermissionAsked{
		RequestID:  "p-queue",
		Permission: "bash",
		Patterns:   []string{"ls"},
	})
	runPermissionCmd(t, cmd)

	sm, ok := m.modal.(*settingsModal)
	if !ok {
		t.Fatalf("modal = %T, want settingsModal", m.modal)
	}
	if sm.cursor != 0 {
		t.Fatalf("settings cursor lost: cursor=%d", sm.cursor)
	}
	if m.pendingBlockingCount() != 1 {
		t.Fatalf("pending = %d, want 1", m.pendingBlockingCount())
	}
	if got := m.pendingBlockingLabel(); got != "1 permission waiting" {
		t.Fatalf("label = %q", got)
	}
	header := ansi.Strip(m.headerView(160))
	if !strings.Contains(header, "1 permission waiting") {
		t.Fatalf("header missing pending badge:\n%s", header)
	}
	// Queued ask must not arm countdown.
	pm, ok := m.modalQueue[0].(*permissionModal)
	if !ok {
		t.Fatalf("queue[0] = %T", m.modalQueue[0])
	}
	if pm.remaining != 0 {
		t.Fatalf("queued remaining = %d, want 0", pm.remaining)
	}
}

func TestCloseUserModalPromotesQueuedPermission(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.modal = newSettingsModal(m.services, m.ops, m.th, m.workDir)
	_ = m.applyEvent(protocol.PermissionAsked{
		RequestID:  "p-promote",
		Permission: "edit",
		Patterns:   []string{"a.go"},
	})

	// Escape closes settings; permission becomes visible exactly once.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEscape})
	pm, ok := m.modal.(*permissionModal)
	if !ok {
		t.Fatalf("modal = %T, want permissionModal", m.modal)
	}
	if pm.req.RequestID != "p-promote" {
		t.Fatalf("requestID = %q", pm.req.RequestID)
	}
	if m.pendingBlockingCount() != 0 {
		t.Fatalf("pending after promote = %d", m.pendingBlockingCount())
	}
	assertNoPermissionReply(t, ops)
}

func TestMultipleAsksQueueOrderAndCorrelate(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.modal = newThemeModal(nil, "", nil)

	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "p1", Permission: "bash", Patterns: []string{"a"}})
	_ = m.applyEvent(protocol.QuestionAsked{
		RequestID: "q1",
		Questions: []protocol.QuestionPrompt{{Question: "pick?", Options: []protocol.QuestionOption{{Label: "yes"}}}},
	})
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "p2", Permission: "edit", Patterns: []string{"b"}})

	if _, ok := m.modal.(*themeModal); !ok {
		t.Fatalf("theme replaced by %T", m.modal)
	}
	if m.pendingBlockingCount() != 3 {
		t.Fatalf("pending = %d, want 3", m.pendingBlockingCount())
	}

	// Dismiss theme → first permission.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEscape})
	if pm, ok := m.modal.(*permissionModal); !ok || pm.req.RequestID != "p1" {
		t.Fatalf("first promote = %T id=%v", m.modal, modalRequestID(m.modal))
	}

	// User answers p1; dismiss promotes the next queued ask immediately.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(Model)
	reply := receiveSinglePermissionReply(t, ops, cmd)
	assertPermissionReply(t, reply, "p1", protocol.DecisionOnce, "")
	if qm, ok := m.modal.(*questionModal); !ok || qm.req.RequestID != "q1" {
		t.Fatalf("second promote = %T id=%v", m.modal, modalRequestID(m.modal))
	}

	_ = m.applyEvent(protocol.QuestionResolved{RequestID: "q1"})
	if pm, ok := m.modal.(*permissionModal); !ok || pm.req.RequestID != "p2" {
		t.Fatalf("third promote = %T id=%v", m.modal, modalRequestID(m.modal))
	}
}

func TestResolvedWhileQueuedNeverAppears(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.modal = newHelpModal(m.commands)
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "stale", Permission: "bash", Patterns: []string{"x"}})
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "keep", Permission: "edit", Patterns: []string{"y"}})
	_ = m.applyEvent(protocol.PermissionResolved{RequestID: "stale"})

	if m.pendingBlockingCount() != 1 {
		t.Fatalf("pending = %d, want 1 after cancel", m.pendingBlockingCount())
	}
	if id := modalRequestID(m.modalQueue[0]); id != "keep" {
		t.Fatalf("queue id = %q, want keep", id)
	}

	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEscape})
	if pm, ok := m.modal.(*permissionModal); !ok || pm.req.RequestID != "keep" {
		t.Fatalf("promoted = %T id=%v", m.modal, modalRequestID(m.modal))
	}
}

func TestDedupeQueuedPermissionByRequestID(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.modal = m.newKeysModal()
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "dup", Permission: "bash", Patterns: []string{"old"}})
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "dup", Permission: "bash", Patterns: []string{"new"}})

	if m.pendingBlockingCount() != 1 {
		t.Fatalf("pending = %d, want 1 after dedupe", m.pendingBlockingCount())
	}
	pm := m.modalQueue[0].(*permissionModal)
	if got := strings.Join(pm.req.Patterns, ","); got != "new" {
		t.Fatalf("patterns = %q, want new", got)
	}
}

func TestQueuedPermissionCountdownDoesNotArmOrFire(t *testing.T) {
	m, ops := newAppTestModelWithOptions(Options{
		PermissionAutoApproveSeconds: 3,
	})
	m.modal = newSettingsModal(m.services, m.ops, m.th, m.workDir)
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "hidden", Permission: "edit", Patterns: []string{"a.go"}})

	pm := m.modalQueue[0].(*permissionModal)
	if pm.remaining != 0 {
		t.Fatalf("queued remaining = %d", pm.remaining)
	}
	// Stale tick while still queued/hidden must not approve.
	updated, cmd := m.Update(permissionCountdownMsg{requestID: "hidden", gen: 1})
	m = updated.(Model)
	runPermissionCmd(t, cmd)
	assertNoPermissionReply(t, ops)
	if _, ok := m.modal.(*settingsModal); !ok {
		t.Fatalf("settings replaced: %T", m.modal)
	}

	// After promote, countdown arms.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEscape})
	pm, ok := m.modal.(*permissionModal)
	if !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	if pm.remaining != 3 {
		t.Fatalf("visible remaining = %d, want 3", pm.remaining)
	}
}

func TestPermissionOnEmptySlotStillOpens(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "open", Permission: "bash", Patterns: []string{"ls"}})
	if pm, ok := m.modal.(*permissionModal); !ok || pm.req.RequestID != "open" {
		t.Fatalf("modal = %T", m.modal)
	}
	if m.pendingBlockingCount() != 0 {
		t.Fatalf("pending = %d", m.pendingBlockingCount())
	}
}

func TestQuestionAskedQueuesBehindUserModal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.modal = newPaletteModal(m.commands, nil, m.currentPaletteAvailability())
	_ = m.applyEvent(protocol.QuestionAsked{
		RequestID: "qq",
		Questions: []protocol.QuestionPrompt{{Question: "ok?"}},
	})
	if _, ok := m.modal.(*paletteModal); !ok {
		t.Fatalf("palette replaced by %T", m.modal)
	}
	if got := m.pendingBlockingLabel(); got != "1 question waiting" {
		t.Fatalf("label = %q", got)
	}
}

func TestRootSwitchPreservesModalQueue(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.modal = newSettingsModal(m.services, m.ops, m.th, m.workDir)
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "pa", Permission: "bash", Patterns: []string{"x"}})
	m.stashActiveRoot()

	// Simulate switching away then back.
	p := m.roots["root-a"]
	if p == nil {
		t.Fatal("stash missing root-a")
	}
	if _, ok := p.modal.(*settingsModal); !ok {
		t.Fatalf("stashed modal = %T", p.modal)
	}
	if len(p.modalQueue) != 1 {
		t.Fatalf("stashed queue len = %d", len(p.modalQueue))
	}

	m.modal = nil
	m.modalQueue = nil
	m.loadRootPane(p)
	if _, ok := m.modal.(*settingsModal); !ok {
		t.Fatalf("restored modal = %T", m.modal)
	}
	if m.pendingBlockingCount() != 1 {
		t.Fatalf("restored pending = %d", m.pendingBlockingCount())
	}
}

func TestResizeKeepsModalQueue(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.modal = newHelpModal(m.commands)
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "rz", Permission: "bash", Patterns: []string{"z"}})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	if _, ok := m.modal.(*helpModal); !ok {
		t.Fatalf("modal after resize = %T", m.modal)
	}
	if m.pendingBlockingCount() != 1 {
		t.Fatalf("pending after resize = %d", m.pendingBlockingCount())
	}
}

func TestAwaitingPermissionStaysWhileQueueNonEmpty(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.modal = newSettingsModal(m.services, m.ops, m.th, m.workDir)
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "a1", Permission: "bash", Patterns: []string{"1"}})
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "a2", Permission: "bash", Patterns: []string{"2"}})
	if !m.awaitingPermission {
		t.Fatal("awaitingPermission false with queued asks")
	}
	_ = m.applyEvent(protocol.PermissionResolved{RequestID: "a1"})
	if !m.awaitingPermission {
		t.Fatal("awaitingPermission cleared while a2 still queued")
	}
	if m.pendingBlockingCount() != 1 {
		t.Fatalf("pending = %d", m.pendingBlockingCount())
	}
	_ = m.applyEvent(protocol.PermissionResolved{RequestID: "a2"})
	if m.awaitingPermission {
		t.Fatal("awaitingPermission still set after queue drained")
	}
}

func TestDoctorYieldsToPermission(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.modal = newDoctorModal(protocol.EffectivePrompt{}, 0, false)
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "doc", Permission: "bash", Patterns: []string{"ls"}})
	if pm, ok := m.modal.(*permissionModal); !ok || pm.req.RequestID != "doc" {
		t.Fatalf("doctor should yield: modal=%T", m.modal)
	}
}

func TestInputRoutesToVisibleTopOnly(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.modal = newSettingsModal(m.services, m.ops, m.th, m.workDir)
	_ = m.applyEvent(protocol.PermissionAsked{RequestID: "top", Permission: "bash", Patterns: []string{"ls"}})

	// Keys go to settings only; must not submit a permission reply.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if _, ok := m.modal.(*settingsModal); !ok {
		t.Fatalf("modal = %T", m.modal)
	}
	assertNoPermissionReply(t, ops)
	if m.pendingBlockingCount() != 1 {
		t.Fatalf("pending = %d", m.pendingBlockingCount())
	}
}
