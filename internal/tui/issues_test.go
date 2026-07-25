package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
)

func runIssues(t *testing.T, m Model, command string) Model {
	t.Helper()
	m.composer.SetValue(command)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	return m
}

func TestIssuesCommandListAddGetClose(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Issues = newFakeIssues()

	m = runIssues(t, m, "/issues")
	if !strings.Contains(m.notice, "(empty)") {
		t.Fatalf("empty list notice = %q", m.notice)
	}

	m = runIssues(t, m, "/issues add fix auth")
	if !strings.Contains(m.notice, "opened #1 fix auth") {
		t.Fatalf("add notice = %q", m.notice)
	}

	m = runIssues(t, m, "/issues get 1")
	if !strings.Contains(m.notice, "#1 [open] fix auth") {
		t.Fatalf("get notice = %q", m.notice)
	}

	m = runIssues(t, m, "/issues list")
	if !strings.Contains(m.notice, "#1 [open] fix auth") {
		t.Fatalf("list notice = %q", m.notice)
	}

	m = runIssues(t, m, "/issues close 1")
	if !strings.Contains(m.notice, "closed #1 fix auth") {
		t.Fatalf("close notice = %q", m.notice)
	}

	m = runIssues(t, m, "/issues list open")
	if !strings.Contains(m.notice, "no open issues") {
		t.Fatalf("open list notice = %q", m.notice)
	}

	m = runIssues(t, m, "/issues list closed")
	if !strings.Contains(m.notice, "#1 [closed] fix auth") {
		t.Fatalf("closed list notice = %q", m.notice)
	}
}

func TestIssuesCommandUnavailable(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Issues = nil
	m = runIssues(t, m, "/issues list")
	if !m.noticeErr || !strings.Contains(m.notice, "unavailable") {
		t.Fatalf("notice = %q (err=%v)", m.notice, m.noticeErr)
	}
}

func TestIssuesCommandUsage(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Issues = newFakeIssues()
	m = runIssues(t, m, "/issues add")
	if !m.noticeErr || !strings.Contains(m.notice, "usage:") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestIssuesCommandGetMiss(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Issues = newFakeIssues(host.Issue{ID: 1, Title: "x", Status: "open"})
	m = runIssues(t, m, "/issues get 99")
	if !m.noticeErr || !strings.Contains(m.notice, "no issue #99") {
		t.Fatalf("notice = %q", m.notice)
	}
}
