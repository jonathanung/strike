package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func runMDRead(t *testing.T, m Model, command string) Model {
	t.Helper()
	// Slash commands are entered on the left composer; restore left focus when a
	// prior /md-read moved focus to the right pane.
	if m.focus != focusLeft {
		cmd := m.setPaneFocus(focusLeft)
		if cmd != nil {
			runAppCmd(t, cmd)
		}
	}
	m.composer.SetValue(command)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		runAppCmd(t, cmd)
	}
	return m
}

func TestMDReadMissingArgShowsUsageAndKeepsLeftFocus(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = runMDRead(t, m, "/md-read")
	if !m.noticeErr || !strings.Contains(strings.ToLower(m.notice), "usage") {
		t.Errorf("notice = %q (err=%v), want usage error", m.notice, m.noticeErr)
	}
	if m.focus != focusLeft {
		t.Errorf("focus = %v, want left", m.focus)
	}
	if m.windows.active().id() != "context" {
		t.Errorf("active window = %q, want context (unchanged)", m.windows.active().id())
	}
}

func TestMDReadNilFilesShowsUnavailable(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Files = nil
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = runMDRead(t, m, "/md-read notes.md")
	if !m.noticeErr || !strings.Contains(strings.ToLower(m.notice), "unavailable") {
		t.Errorf("notice = %q (err=%v), want unavailable", m.notice, m.noticeErr)
	}
	if m.focus == focusRight {
		t.Error("nil Files forced right focus")
	}
}

func TestMDReadReadErrorShowsNoticeWithoutRightFocus(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Files = &fakeFiles{err: errors.New("permission denied")}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = runMDRead(t, m, "/md-read secret.md")
	if !m.noticeErr || !strings.Contains(m.notice, "permission denied") {
		t.Errorf("notice = %q (err=%v), want read error", m.notice, m.noticeErr)
	}
	if m.focus == focusRight {
		t.Error("read error forced right focus")
	}
}

func TestMDReadSuccessActivatesMarkdownAndShowsContent(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.services.Files = &fakeFiles{files: map[string][]byte{
		"notes.md": []byte("# Hello\n\nworld"),
	}}
	m = runMDRead(t, m, "/md-read notes.md")
	if m.windows.active().id() != markdownWindowID {
		t.Errorf("active id = %q, want markdown", m.windows.active().id())
	}
	if m.focus != focusRight {
		t.Errorf("focus = %v, want right", m.focus)
	}
	if m.notice != "" {
		t.Errorf("notice = %q, want cleared on success", m.notice)
	}
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "Hello") && !strings.Contains(plain, "world") {
		t.Errorf("view missing markdown content: %q", plain)
	}
	if title := m.windows.active().title(); title != "notes.md" {
		t.Errorf("title = %q, want notes.md", title)
	}
}

func TestMDReadPathWithSpaces(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.services.Files = &fakeFiles{files: map[string][]byte{
		"my file.md": []byte("# SpacedTitle\n"),
	}}
	m = runMDRead(t, m, "/md-read my file.md")
	if m.windows.active().id() != markdownWindowID {
		t.Fatalf("active id = %q, want markdown", m.windows.active().id())
	}
	mw := m.windows.active().(markdownWindow)
	if mw.path != "my file.md" {
		t.Errorf("path = %q, want %q", mw.path, "my file.md")
	}
	if !strings.Contains(ansi.Strip(m.View()), "SpacedTitle") {
		t.Errorf("view missing spaced-file content: %q", ansi.Strip(m.View()))
	}
}

func TestHelpNoticeIncludesMDRead(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = runMDRead(t, m, "/help")
	if !strings.Contains(m.notice, "/md-read") {
		t.Errorf("help notice omits /md-read: %q", m.notice)
	}
}

func TestMDReadRejectsBinaryContent(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.services.Files = &fakeFiles{files: map[string][]byte{
		"blob.bin": []byte("hello\x00world"),
	}}
	m = runMDRead(t, m, "/md-read blob.bin")
	if !m.noticeErr || !strings.Contains(strings.ToLower(m.notice), "binary") {
		t.Errorf("notice = %q (err=%v), want binary rejection", m.notice, m.noticeErr)
	}
	if m.focus == focusRight {
		t.Error("binary rejection forced right focus")
	}
}

func TestMDReadSecondCallReplacesContent(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.services.Files = &fakeFiles{files: map[string][]byte{
		"one.md": []byte("# FirstDoc unique-aaa"),
		"two.md": []byte("# SecondDoc unique-bbb"),
	}}
	m = runMDRead(t, m, "/md-read one.md")
	first := m.windows.active().(markdownWindow)
	if first.path != "one.md" || !strings.Contains(first.source, "FirstDoc") {
		t.Fatalf("first load path/source = %q/%q", first.path, first.source)
	}

	m = runMDRead(t, m, "/md-read two.md")
	second := m.windows.active().(markdownWindow)
	if second.path != "two.md" {
		t.Errorf("second path = %q, want two.md", second.path)
	}
	if !strings.Contains(second.source, "SecondDoc") {
		t.Errorf("second source = %q, want SecondDoc", second.source)
	}
	if strings.Contains(second.source, "FirstDoc") {
		t.Errorf("second source still contains FirstDoc: %q", second.source)
	}
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "unique-bbb") {
		t.Errorf("view after replace missing second content: %q", plain)
	}
	if strings.Contains(plain, "unique-aaa") {
		t.Errorf("view after replace still shows first content: %q", plain)
	}
}
