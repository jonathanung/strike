package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
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

	m = runMemory(t, m, "/memory set build.cmd make test")
	if !strings.Contains(m.notice, "set build.cmd") {
		t.Fatalf("set notice = %q", m.notice)
	}

	m = runMemory(t, m, "/memory get build.cmd")
	if !strings.Contains(m.notice, "build.cmd=make test") {
		t.Fatalf("get notice = %q", m.notice)
	}

	m = runMemory(t, m, "/memory list")
	if !strings.Contains(m.notice, "build.cmd=make test") {
		t.Fatalf("list notice = %q", m.notice)
	}

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
	if !strings.Contains(m.notice, "a=1") || strings.Contains(m.notice, "b=2") {
		t.Fatalf("tag list notice = %q", m.notice)
	}
}
