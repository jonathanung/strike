package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

var _ window = markdownWindow{}

func TestMarkdownWindowEmptyView(t *testing.T) {
	w := newMarkdownWindow().resize(40, 10).(markdownWindow)
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "No file open") {
		t.Errorf("empty view = %q, want it to mention No file open", plain)
	}
}

func TestMarkdownWindowLoadUsesInjectedRenderer(t *testing.T) {
	w := newMarkdownWindow()
	w.renderMarkdown = func(source string, width int) (string, error) {
		return "MARKER:" + source, nil
	}
	w = w.resize(40, 10).(markdownWindow)
	w = w.load("notes.md", "# Hello")
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "MARKER:# Hello") {
		t.Errorf("loaded view = %q, want injected marker", plain)
	}
}

func TestMarkdownWindowReRendersOnWidthChangeOnly(t *testing.T) {
	var calls int
	var lastWidth int
	w := newMarkdownWindow()
	w.renderMarkdown = func(source string, width int) (string, error) {
		calls++
		lastWidth = width
		return fmt.Sprintf("w=%d", width), nil
	}
	w = w.resize(40, 10).(markdownWindow)
	w = w.load("doc.md", "body")
	if calls != 1 || lastWidth != 40 {
		t.Fatalf("after load: calls=%d lastWidth=%d, want 1/40", calls, lastWidth)
	}

	w = w.resize(40, 20).(markdownWindow) // height-only
	if calls != 1 {
		t.Errorf("height-only resize re-rendered: calls=%d, want 1", calls)
	}

	w = w.resize(55, 20).(markdownWindow) // width change
	if calls != 2 || lastWidth != 55 {
		t.Errorf("width resize: calls=%d lastWidth=%d, want 2/55", calls, lastWidth)
	}
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "w=55") {
		t.Errorf("view after width change = %q, want w=55", plain)
	}
}

func TestMarkdownWindowScrollIncreasesYOffset(t *testing.T) {
	w := newMarkdownWindow()
	w.renderMarkdown = func(source string, width int) (string, error) {
		var b strings.Builder
		for i := 0; i < 80; i++ {
			fmt.Fprintf(&b, "line-%02d\n", i)
		}
		return b.String(), nil
	}
	w = w.resize(40, 5).(markdownWindow)
	w = w.load("tall.md", "# tall")
	if w.vp.YOffset() != 0 {
		t.Fatalf("initial YOffset = %d, want 0", w.vp.YOffset())
	}

	updated, _ := w.update(tea.KeyPressMsg{Code: tea.KeyDown})
	w = updated.(markdownWindow)
	if w.vp.YOffset() <= 0 {
		t.Errorf("after KeyDown YOffset = %d, want > 0", w.vp.YOffset())
	}
	afterDown := w.vp.YOffset()

	updated, _ = w.update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	w = updated.(markdownWindow)
	if w.vp.YOffset() <= afterDown {
		t.Errorf("after PgDown YOffset = %d, want > %d", w.vp.YOffset(), afterDown)
	}
}

func TestMarkdownWindowTitleIsBasenameWhenPathSet(t *testing.T) {
	w := newMarkdownWindow()
	if got := w.title(); got != "markdown" {
		t.Errorf("empty title = %q, want markdown", got)
	}
	w = w.load("/tmp/docs/notes.md", "# hi")
	if got := w.title(); got != "notes.md" {
		t.Errorf("loaded title = %q, want notes.md", got)
	}
}

func TestMarkdownWindowEmptyStateIsWidthSafe(t *testing.T) {
	w := newMarkdownWindow().resize(8, 3).(markdownWindow)
	view := w.view(theme.Default())
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 8 {
			t.Errorf("empty markdown line width = %d, want <= 8: %q", got, line)
		}
	}
}
