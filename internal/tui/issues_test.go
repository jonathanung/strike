package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
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
	if m.modal != nil {
		t.Fatalf("empty list opened modal %T", m.modal)
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
	im, ok := m.modal.(*issuesModal)
	if !ok || im == nil {
		t.Fatalf("list modal = %T, want *issuesModal", m.modal)
	}
	if len(im.all) != 1 || im.all[0].ID != 1 || im.all[0].Title != "fix auth" {
		t.Fatalf("list modal items = %+v", im.all)
	}
	if m.notice != "" {
		t.Fatalf("list left notice = %q, want empty", m.notice)
	}

	m.modal = nil
	m = runIssues(t, m, "/issues close 1")
	if !strings.Contains(m.notice, "closed #1 fix auth") {
		t.Fatalf("close notice = %q", m.notice)
	}

	m = runIssues(t, m, "/issues list open")
	if !strings.Contains(m.notice, "no open issues") {
		t.Fatalf("open list notice = %q", m.notice)
	}
	if m.modal != nil {
		t.Fatalf("empty open list opened modal %T", m.modal)
	}

	m = runIssues(t, m, "/issues list closed")
	im, ok = m.modal.(*issuesModal)
	if !ok || im == nil {
		t.Fatalf("closed list modal = %T", m.modal)
	}
	if im.status != "closed" || len(im.all) != 1 || im.all[0].Status != "closed" {
		t.Fatalf("closed list modal = %+v status=%q", im.all, im.status)
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

func TestIssuesModalBrowseFilterDetail(t *testing.T) {
	items := []host.Issue{
		{ID: 1, Title: "fix auth", Status: "open", Body: "oauth flow"},
		{ID: 2, Title: "ship feature", Status: "open"},
		{ID: 3, Title: "old bug", Status: "closed", Body: "resolved"},
	}
	modal := newIssuesModal(items, "")
	view := modal.view(72, theme.Default().Resolve())
	if !strings.Contains(view, "#1") || !strings.Contains(view, "fix auth") || !strings.Contains(view, "#3") {
		t.Fatalf("list view missing issues:\n%s", view)
	}

	modal.filter = "auth"
	list := modal.filtered()
	if len(list) != 1 || list[0].ID != 1 {
		t.Fatalf("filter = %+v", list)
	}
	modal.cursor = 0
	next, _ := modal.update(tea.KeyMsg{Type: tea.KeyEnter})
	im := next.(*issuesModal)
	if !im.detail {
		t.Fatal("enter did not open detail")
	}
	detail := im.view(72, theme.Default().Resolve())
	if !strings.Contains(detail, "fix auth") || !strings.Contains(detail, "oauth flow") {
		t.Fatalf("detail view missing content:\n%s", detail)
	}

	next, _ = im.update(tea.KeyMsg{Type: tea.KeyEsc})
	im = next.(*issuesModal)
	if im.detail {
		t.Fatal("esc did not leave detail")
	}
	next, _ = im.update(tea.KeyMsg{Type: tea.KeyEsc})
	if next != nil {
		t.Fatalf("esc list = %T, want nil", next)
	}
}

func TestIssuesCommandManyItemsOpensModalNotNotice(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	items := make([]host.Issue, 12)
	for i := range items {
		items[i] = host.Issue{ID: i + 1, Title: "issue " + string(rune('a'+i)), Status: "open"}
	}
	m.services.Issues = newFakeIssues(items...)
	m = runIssues(t, m, "/issues")
	im, ok := m.modal.(*issuesModal)
	if !ok || im == nil {
		t.Fatalf("modal = %T", m.modal)
	}
	if len(im.all) != 12 {
		t.Fatalf("items = %d, want 12", len(im.all))
	}
	if m.notice != "" {
		t.Fatalf("notice = %q, want empty (browse modal owns multi-item output)", m.notice)
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Issues") {
		t.Fatalf("view missing Issues dialog:\n%s", view)
	}
}
