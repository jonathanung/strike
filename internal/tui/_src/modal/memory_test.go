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

func runMemory(t *testing.T, m Model, command string) Model {
	t.Helper()
	m.composer.SetValue(command)
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	return m
}

func memoryWin(t *testing.T, m Model) memoryWindow {
	t.Helper()
	for _, w := range m.windows.windows {
		if mw, ok := w.(memoryWindow); ok {
			return mw
		}
	}
	t.Fatal("memory window missing")
	return memoryWindow{}
}

func TestMemoryCommandListSetGetRm(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Memory = newFakeMemory()
	m.windows = configureMemoryWindow(m.windows, m.services.Memory)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})

	m = runMemory(t, m, "/memory")
	if m.windows.active().id() != memoryWindowID {
		t.Fatalf("list active = %q, want memory", m.windows.active().id())
	}
	if m.focus != focusRight {
		t.Fatalf("list focus = %v, want right", m.focus)
	}
	mw := memoryWin(t, m)
	if len(mw.entries) != 0 {
		t.Fatalf("empty list entries = %+v", mw.entries)
	}
	if m.modal != nil {
		t.Fatalf("list opened modal %T", m.modal)
	}

	// Return focus so slash commands still work from the composer.
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	m = runMemory(t, m, "/memory set build.cmd make test")
	if !strings.Contains(m.notice, "set build.cmd") {
		t.Fatalf("set notice = %q", m.notice)
	}
	mw = memoryWin(t, m)
	if len(mw.entries) != 1 || mw.entries[0].Key != "build.cmd" {
		t.Fatalf("after set entries = %+v", mw.entries)
	}

	m = runMemory(t, m, "/memory get build.cmd")
	if !strings.Contains(m.notice, "build.cmd=make test") {
		t.Fatalf("get notice = %q", m.notice)
	}

	m = runMemory(t, m, "/memory list")
	if m.windows.active().id() != memoryWindowID || m.focus != focusRight {
		t.Fatalf("list pane = %q focus=%v", m.windows.active().id(), m.focus)
	}
	mw = memoryWin(t, m)
	if len(mw.entries) != 1 || mw.entries[0].Key != "build.cmd" || mw.entries[0].Value != "make test" {
		t.Fatalf("list entries = %+v", mw.entries)
	}

	m = updateApp(t, m, tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	m = runMemory(t, m, "/memory rm build.cmd")
	if !strings.Contains(m.notice, "deleted build.cmd") {
		t.Fatalf("rm notice = %q", m.notice)
	}
	mw = memoryWin(t, m)
	if len(mw.entries) != 0 {
		t.Fatalf("after rm entries = %+v", mw.entries)
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

func TestMemoryCommandExportImport(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	mem := newFakeMemory(host.MemoryEntry{Key: "a", Value: "1"})
	mem.importEntries = []host.MemoryEntry{{Key: "b", Value: "2"}}
	m.services.Memory = mem
	m.windows = configureMemoryWindow(m.windows, mem)

	m = runMemory(t, m, "/memory export")
	if m.noticeErr || !strings.Contains(m.notice, "exported to strike-memory.json") {
		t.Fatalf("export notice = %q", m.notice)
	}
	if mem.exportPath != "strike-memory.json" {
		t.Fatalf("export path = %q", mem.exportPath)
	}

	m = runMemory(t, m, "/memory export custom.json")
	if mem.exportPath != "custom.json" {
		t.Fatalf("custom export path = %q", mem.exportPath)
	}

	m = runMemory(t, m, "/memory import dump.json")
	if m.noticeErr || !strings.Contains(m.notice, "imported 1 entries (merged)") {
		t.Fatalf("import notice = %q", m.notice)
	}
	if _, ok, _ := mem.Get("b"); !ok {
		t.Fatal("expected imported key b")
	}

	mem.importEntries = []host.MemoryEntry{{Key: "only", Value: "x"}}
	m = runMemory(t, m, "/memory import dump.json --replace")
	if m.noticeErr || !strings.Contains(m.notice, "replaced") {
		t.Fatalf("replace notice = %q", m.notice)
	}
	if _, ok, _ := mem.Get("a"); ok {
		t.Fatal("replace should drop prior keys")
	}
	if _, ok, _ := mem.Get("only"); !ok {
		t.Fatal("replace missing only")
	}

	m = runMemory(t, m, "/memory import ../escape.json")
	if !m.noticeErr || !strings.Contains(m.notice, "escapes") {
		t.Fatalf("escape notice = %q", m.notice)
	}

	m = runMemory(t, m, "/memory import")
	if !m.noticeErr || !strings.Contains(m.notice, "usage:") {
		t.Fatalf("missing path notice = %q", m.notice)
	}
}

func TestMemoryCommandListTag(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Memory = newFakeMemory(
		host.MemoryEntry{Key: "a", Value: "1", Tags: []string{"keep"}},
		host.MemoryEntry{Key: "b", Value: "2", Tags: []string{"skip"}},
	)
	m.windows = configureMemoryWindow(m.windows, m.services.Memory)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m = runMemory(t, m, "/memory list keep")
	mw := memoryWin(t, m)
	if mw.tag != "keep" {
		t.Fatalf("tag = %q, want keep", mw.tag)
	}
	if len(mw.entries) != 1 || mw.entries[0].Key != "a" {
		t.Fatalf("tag list entries = %+v", mw.entries)
	}
}

func TestMemoryWindowBrowseExpandDetail(t *testing.T) {
	mem := newFakeMemory(
		host.MemoryEntry{Key: "build.cmd", Value: "make test", Tags: []string{"ci"}},
		host.MemoryEntry{Key: "deploy.cmd", Value: "make ship", Tags: []string{"prod"}},
		host.MemoryEntry{Key: "notes", Value: "long value " + strings.Repeat("x", 80)},
	)
	w := newMemoryWindow().bind(mem, "").resize(40, 12).(memoryWindow)
	view := ansi.Strip(w.view(theme.Default().Resolve()))
	if !strings.Contains(view, "build.cmd") || !strings.Contains(view, "deploy.cmd") || !strings.Contains(view, "notes") {
		t.Fatalf("list view missing entries:\n%s", view)
	}

	next, _ := w.update(tea.KeyPressMsg{Code: tea.KeyDown})
	w = next.(memoryWindow)
	next, _ = w.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	w = next.(memoryWindow)
	if !w.detail {
		t.Fatal("enter did not open detail")
	}
	detail := ansi.Strip(w.view(theme.Default().Resolve()))
	if !strings.Contains(detail, "deploy.cmd") || !strings.Contains(detail, "make ship") {
		t.Fatalf("detail view missing content:\n%s", detail)
	}

	next, _ = w.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	w = next.(memoryWindow)
	if w.detail {
		t.Fatal("esc did not leave detail")
	}
}

func TestMemoryCommandManyEntriesOpensPaneNotNotice(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	entries := make([]host.MemoryEntry, 12)
	for i := range entries {
		entries[i] = host.MemoryEntry{Key: "k" + string(rune('a'+i)), Value: "v"}
	}
	m.services.Memory = newFakeMemory(entries...)
	m.windows = configureMemoryWindow(m.windows, m.services.Memory)
	m = runMemory(t, m, "/memory")
	if m.windows.active().id() != memoryWindowID {
		t.Fatalf("active = %q", m.windows.active().id())
	}
	mw := memoryWin(t, m)
	if len(mw.entries) != 12 {
		t.Fatalf("entries = %d, want 12", len(mw.entries))
	}
	if m.notice != "" {
		t.Fatalf("notice = %q, want empty (pane owns multi-item output)", m.notice)
	}
	if m.modal != nil {
		t.Fatalf("opened modal %T", m.modal)
	}
	view := ansi.Strip(viewString(m))
	if !strings.Contains(view, "memory") {
		t.Fatalf("view missing memory pane title:\n%s", view)
	}
	// More than 5 rows of content available in the right pane body.
	body := ansi.Strip(mw.resize(36, 20).view(theme.Default().Resolve()))
	if lines := strings.Count(body, "\n") + 1; lines < 6 {
		t.Fatalf("pane body lines = %d, want >= 6 for multi-item browse:\n%s", lines, body)
	}
}

func TestMemoryWindowRefreshAfterToolWrite(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	mem := newFakeMemory(host.MemoryEntry{Key: "a", Value: "1"})
	m.services.Memory = mem
	m.windows = configureMemoryWindow(m.windows, mem)
	if len(memoryWin(t, m).entries) != 1 {
		t.Fatal("expected one seed entry")
	}
	if err := mem.Put("b", "2", nil); err != nil {
		t.Fatal(err)
	}
	// Simulate agent tool completion writing memory.
	m.applyEvent(protocol.ToolCallBegin{CallID: "c1", Name: "memory_write", Args: []byte(`{}`)})
	m.applyEvent(protocol.ToolCallEnd{CallID: "c1", Output: "ok"})
	mw := memoryWin(t, m)
	if len(mw.entries) != 2 {
		t.Fatalf("after tool refresh entries = %+v", mw.entries)
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
	next, _ := modal.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	mm := next.(*memoryModal)
	if !mm.detail {
		t.Fatal("enter did not open detail")
	}
	detail := mm.view(72, theme.Default().Resolve())
	if !strings.Contains(detail, "deploy.cmd") || !strings.Contains(detail, "make ship") {
		t.Fatalf("detail view missing content:\n%s", detail)
	}

	next, _ = mm.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	mm = next.(*memoryModal)
	if mm.detail {
		t.Fatal("esc did not leave detail")
	}
	next, _ = mm.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if next != nil {
		t.Fatalf("esc list = %T, want nil", next)
	}
}
