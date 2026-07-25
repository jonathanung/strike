package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func runMemory(t *testing.T, m Model, command string) Model {
	t.Helper()
	m.composer.SetValue(command)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	return m
}

func TestMemoryCommandListSetGetRm(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Memory = newFakeMemory()

	m = runMemory(t, m, "/memory")
	if !strings.Contains(m.notice, "(empty)") {
		t.Fatalf("empty list notice = %q", m.notice)
	}
	if m.modal != nil {
		t.Fatalf("empty list opened modal %T", m.modal)
	}

	m = runMemory(t, m, "/memory set build.cmd make test")
	if !strings.Contains(m.notice, "set build.cmd") {
		t.Fatalf("set notice = %q", m.notice)
	}

	m = runMemory(t, m, "/memory get build.cmd")
	if !strings.Contains(m.notice, "build.cmd=make test") {
		t.Fatalf("get notice = %q", m.notice)
	}

	m = runMemory(t, m, "/memory list")
	mm, ok := m.modal.(*memoryModal)
	if !ok || mm == nil {
		t.Fatalf("list modal = %T, want *memoryModal", m.modal)
	}
	if len(mm.all) != 1 || mm.all[0].Key != "build.cmd" || mm.all[0].Value != "make test" {
		t.Fatalf("list modal entries = %+v", mm.all)
	}
	if m.notice != "" {
		t.Fatalf("list left notice = %q, want empty", m.notice)
	}

	m.modal = nil
	m = runMemory(t, m, "/memory rm build.cmd")
	if !strings.Contains(m.notice, "deleted build.cmd") {
		t.Fatalf("rm notice = %q", m.notice)
	}

	m = runMemory(t, m, "/memory get build.cmd")
	if !strings.Contains(m.notice, "no entry") {
		t.Fatalf("miss notice = %q", m.notice)
	}
}

func TestMemoryCommandUnavailable(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Memory = nil
	m = runMemory(t, m, "/memory list")
	if !m.noticeErr || !strings.Contains(m.notice, "unavailable") {
		t.Fatalf("notice = %q (err=%v)", m.notice, m.noticeErr)
	}
}

func TestMemoryCommandUsage(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Memory = newFakeMemory()
	m = runMemory(t, m, "/memory set onlykey")
	if !m.noticeErr || !strings.Contains(m.notice, "usage:") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestMemoryCommandListTag(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Memory = newFakeMemory(
		host.MemoryEntry{Key: "a", Value: "1", Tags: []string{"keep"}},
		host.MemoryEntry{Key: "b", Value: "2", Tags: []string{"skip"}},
	)
	m = runMemory(t, m, "/memory list keep")
	mm, ok := m.modal.(*memoryModal)
	if !ok || mm == nil {
		t.Fatalf("tag list modal = %T", m.modal)
	}
	if mm.tag != "keep" {
		t.Fatalf("tag = %q, want keep", mm.tag)
	}
	if len(mm.all) != 1 || mm.all[0].Key != "a" {
		t.Fatalf("tag list entries = %+v", mm.all)
	}
}

func TestMemoryModalBrowseFilterDetail(t *testing.T) {
	entries := []host.MemoryEntry{
		{Key: "build.cmd", Value: "make test", Tags: []string{"ci"}},
		{Key: "deploy.cmd", Value: "make ship", Tags: []string{"prod"}},
		{Key: "notes", Value: "long value " + strings.Repeat("x", 80)},
	}
	modal := newMemoryModal(entries, "")
	view := modal.view(72, theme.Default().Resolve())
	if !strings.Contains(view, "build.cmd") || !strings.Contains(view, "deploy.cmd") || !strings.Contains(view, "notes") {
		t.Fatalf("list view missing entries:\n%s", view)
	}

	modal.filter = "deploy"
	list := modal.filtered()
	if len(list) != 1 || list[0].Key != "deploy.cmd" {
		t.Fatalf("filter = %+v", list)
	}
	modal.cursor = 0
	next, _ := modal.update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(*memoryModal)
	if !mm.detail {
		t.Fatal("enter did not open detail")
	}
	detail := mm.view(72, theme.Default().Resolve())
	if !strings.Contains(detail, "deploy.cmd") || !strings.Contains(detail, "make ship") {
		t.Fatalf("detail view missing content:\n%s", detail)
	}

	next, _ = mm.update(tea.KeyMsg{Type: tea.KeyEsc})
	mm = next.(*memoryModal)
	if mm.detail {
		t.Fatal("esc did not leave detail")
	}
	next, _ = mm.update(tea.KeyMsg{Type: tea.KeyEsc})
	if next != nil {
		t.Fatalf("esc list = %T, want nil", next)
	}
}

func TestMemoryCommandManyEntriesOpensModalNotNotice(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	entries := make([]host.MemoryEntry, 12)
	for i := range entries {
		entries[i] = host.MemoryEntry{Key: "k" + string(rune('a'+i)), Value: "v"}
	}
	m.services.Memory = newFakeMemory(entries...)
	m = runMemory(t, m, "/memory")
	mm, ok := m.modal.(*memoryModal)
	if !ok || mm == nil {
		t.Fatalf("modal = %T", m.modal)
	}
	if len(mm.all) != 12 {
		t.Fatalf("entries = %d, want 12", len(mm.all))
	}
	if m.notice != "" {
		t.Fatalf("notice = %q, want empty (browse modal owns multi-item output)", m.notice)
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Memory") {
		t.Fatalf("view missing Memory dialog:\n%s", view)
	}
}
