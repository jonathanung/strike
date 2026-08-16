package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func runIssues(t *testing.T, m Model, command string) Model {
	t.Helper()
	m.composer.SetValue(command)
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	return m
}

func issuesWin(t *testing.T, m Model) issuesWindow {
	t.Helper()
	for _, w := range m.windows.windows {
		if iw, ok := w.(issuesWindow); ok {
			return iw
		}
	}
	t.Fatal("issues window missing")
	return issuesWindow{}
}

func TestIssuesCommandListAddGetClose(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Issues = newFakeIssues()
	m.windows = configureIssuesWindow(m.windows, m.services.Issues)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})

	m = runIssues(t, m, "/issues")
	if m.windows.active().id() != issuesWindowID {
		t.Fatalf("list active = %q, want issues", m.windows.active().id())
	}
	if m.focus != focusRight {
		t.Fatalf("list focus = %v, want right", m.focus)
	}
	if len(issuesWin(t, m).items) != 0 {
		t.Fatalf("empty list items = %+v", issuesWin(t, m).items)
	}
	if m.modal != nil {
		t.Fatalf("list opened modal %T", m.modal)
	}

	m = updateApp(t, m, tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	m = runIssues(t, m, "/issues add fix auth")
	if !strings.Contains(m.notice, "opened #1 fix auth") {
		t.Fatalf("add notice = %q", m.notice)
	}
	if len(issuesWin(t, m).items) != 1 {
		t.Fatalf("after add items = %+v", issuesWin(t, m).items)
	}

	m = runIssues(t, m, "/issues get 1")
	if !strings.Contains(m.notice, "#1 [open] fix auth") {
		t.Fatalf("get notice = %q", m.notice)
	}

	m = runIssues(t, m, "/issues list")
	if m.windows.active().id() != issuesWindowID {
		t.Fatalf("list pane = %q", m.windows.active().id())
	}
	iw := issuesWin(t, m)
	if len(iw.items) != 1 || iw.items[0].ID != 1 || iw.items[0].Title != "fix auth" {
		t.Fatalf("list items = %+v", iw.items)
	}

	m = updateApp(t, m, tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	m = runIssues(t, m, "/issues close 1")
	if !strings.Contains(m.notice, "closed #1 fix auth") {
		t.Fatalf("close notice = %q", m.notice)
	}

	m = runIssues(t, m, "/issues list open")
	iw = issuesWin(t, m)
	if iw.status != "open" || len(iw.items) != 0 {
		t.Fatalf("open list = %+v status=%q", iw.items, iw.status)
	}

	m = updateApp(t, m, tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	m = runIssues(t, m, "/issues list closed")
	iw = issuesWin(t, m)
	if iw.status != "closed" || len(iw.items) != 1 || iw.items[0].Status != "closed" {
		t.Fatalf("closed list = %+v status=%q", iw.items, iw.status)
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

func TestIssuesCommandExportImport(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	store := newFakeIssues(host.Issue{ID: 1, Title: "a", Status: "open"})
	store.importItems = []host.Issue{{ID: 2, Title: "b", Status: "open"}}
	m.services.Issues = store
	m.windows = configureIssuesWindow(m.windows, store)

	m = runIssues(t, m, "/issues export")
	if m.noticeErr || !strings.Contains(m.notice, "exported to strike-issues.json") {
		t.Fatalf("export notice = %q", m.notice)
	}
	if store.exportPath != "strike-issues.json" {
		t.Fatalf("export path = %q", store.exportPath)
	}

	m = runIssues(t, m, "/issues import dump.json")
	if m.noticeErr || !strings.Contains(m.notice, "imported 1 issues (merged)") {
		t.Fatalf("import notice = %q", m.notice)
	}
	if _, ok, _ := store.Get(2); !ok {
		t.Fatal("expected imported #2")
	}

	store.importItems = []host.Issue{{ID: 9, Title: "only", Status: "closed"}}
	m = runIssues(t, m, "/issues import --replace dump.json")
	if m.noticeErr || !strings.Contains(m.notice, "replaced") {
		t.Fatalf("replace notice = %q", m.notice)
	}
	if _, ok, _ := store.Get(1); ok {
		t.Fatal("replace should drop #1")
	}
	if got, ok, _ := store.Get(9); !ok || got.Title != "only" {
		t.Fatalf("replace #9 = %+v ok=%v", got, ok)
	}

	m = runIssues(t, m, "/issues import ../x.json")
	if !m.noticeErr || !strings.Contains(m.notice, "escapes") {
		t.Fatalf("escape notice = %q", m.notice)
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

func TestIssuesWindowBrowseExpandDetail(t *testing.T) {
	store := newFakeIssues(
		host.Issue{ID: 1, Title: "fix auth", Status: "open", Body: "oauth flow"},
		host.Issue{ID: 2, Title: "ship feature", Status: "open"},
		host.Issue{ID: 3, Title: "old bug", Status: "closed", Body: "resolved"},
	)
	w := newIssuesWindow().bind(store, "").resize(40, 12).(issuesWindow)
	view := ansi.Strip(w.view(theme.Default().Resolve()))
	if !strings.Contains(view, "#1") || !strings.Contains(view, "fix auth") || !strings.Contains(view, "#3") {
		t.Fatalf("list view missing issues:\n%s", view)
	}

	next, _ := w.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	w = next.(issuesWindow)
	if !w.detail {
		t.Fatal("enter did not open detail")
	}
	detail := ansi.Strip(w.view(theme.Default().Resolve()))
	if !strings.Contains(detail, "fix auth") || !strings.Contains(detail, "oauth flow") {
		t.Fatalf("detail view missing content:\n%s", detail)
	}

	next, _ = w.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	w = next.(issuesWindow)
	if w.detail {
		t.Fatal("esc did not leave detail")
	}
}

func TestIssuesCommandManyItemsOpensPaneNotNotice(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	items := make([]host.Issue, 12)
	for i := range items {
		items[i] = host.Issue{ID: i + 1, Title: "issue " + string(rune('a'+i)), Status: "open"}
	}
	m.services.Issues = newFakeIssues(items...)
	m.windows = configureIssuesWindow(m.windows, m.services.Issues)
	m = runIssues(t, m, "/issues")
	if m.windows.active().id() != issuesWindowID {
		t.Fatalf("active = %q", m.windows.active().id())
	}
	iw := issuesWin(t, m)
	if len(iw.items) != 12 {
		t.Fatalf("items = %d, want 12", len(iw.items))
	}
	if m.notice != "" {
		t.Fatalf("notice = %q, want empty (pane owns multi-item output)", m.notice)
	}
	if m.modal != nil {
		t.Fatalf("opened modal %T", m.modal)
	}
	view := ansi.Strip(viewString(m))
	if !strings.Contains(view, "issues") {
		t.Fatalf("view missing issues pane title:\n%s", view)
	}
	body := ansi.Strip(iw.resize(36, 20).view(theme.Default().Resolve()))
	if lines := strings.Count(body, "\n") + 1; lines < 6 {
		t.Fatalf("pane body lines = %d, want >= 6:\n%s", lines, body)
	}
}

func TestIssuesWindowRefreshAfterCloseCommand(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Issues = newFakeIssues(host.Issue{ID: 1, Title: "x", Status: "open"})
	m.windows = configureIssuesWindow(m.windows, m.services.Issues)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m = runIssues(t, m, "/issues close 1")
	iw := issuesWin(t, m)
	if len(iw.items) != 1 || iw.items[0].Status != "closed" {
		t.Fatalf("after close items = %+v", iw.items)
	}
}

func TestIssuesWindowRefreshAfterToolWrite(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	store := newFakeIssues(host.Issue{ID: 1, Title: "a", Status: "open"})
	m.services.Issues = store
	m.windows = configureIssuesWindow(m.windows, store)
	if _, err := store.Create("b", ""); err != nil {
		t.Fatal(err)
	}
	m.applyEvent(protocol.ToolCallBegin{CallID: "c1", Name: "issue_write", Args: []byte(`{}`)})
	m.applyEvent(protocol.ToolCallEnd{CallID: "c1", Output: "ok"})
	iw := issuesWin(t, m)
	if len(iw.items) != 2 {
		t.Fatalf("after tool refresh items = %+v", iw.items)
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
	next, _ := modal.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	im := next.(*issuesModal)
	if !im.detail {
		t.Fatal("enter did not open detail")
	}
	detail := im.view(72, theme.Default().Resolve())
	if !strings.Contains(detail, "fix auth") || !strings.Contains(detail, "oauth flow") {
		t.Fatalf("detail view missing content:\n%s", detail)
	}

	next, _ = im.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	im = next.(*issuesModal)
	if im.detail {
		t.Fatal("esc did not leave detail")
	}
	next, _ = im.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if next != nil {
		t.Fatalf("esc list = %T, want nil", next)
	}
}
