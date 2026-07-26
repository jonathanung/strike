package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestStyleColumnsAndExtractSelection(t *testing.T) {
	style := lipgloss.NewStyle().Reverse(true)
	line := "hello world"
	got := styleColumns(line, 6, 11, style)
	if !strings.Contains(ansi.Strip(got), "world") {
		t.Fatalf("styled line missing world: %q", got)
	}
	if ansi.Strip(got) != line {
		t.Fatalf("strip = %q, want %q", ansi.Strip(got), line)
	}

	frame := "aaabbbccc\ndddeeefff"
	sel := textSel{has: true, a: screenPos{X: 3, Y: 0}, b: screenPos{X: 5, Y: 1}}
	text := extractTextSelection(frame, sel)
	if text != "bbbccc\ndddeee" {
		t.Fatalf("extract = %q", text)
	}
	out := applyTextSelection(frame, sel, style)
	if ansi.Strip(out) != frame {
		t.Fatalf("highlight changed plain text:\n%s", ansi.Strip(out))
	}
}

func TestTextSelectRegionOnlyTranscriptAndPrompt(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.applyEvent(protocol.UserMessage{Text: "selectable transcript line one"})
	m.applyEvent(protocol.TextDelta{Text: "selectable transcript line two"})
	m.refreshViewport()
	m.viewport.GotoTop()

	tr, ok := m.transcriptContentRect()
	if !ok {
		t.Fatal("expected transcript content rect")
	}
	pr, ok := m.promptContentRect()
	if !ok {
		t.Fatal("expected prompt content rect")
	}

	// Header row (y=0) is chrome — not selectable.
	if _, ok := m.textSelectRegionAt(tr.X+1, 0); ok {
		t.Fatal("header should not be a text-select region")
	}
	// Hints / footer near bottom.
	if _, ok := m.textSelectRegionAt(1, m.height-1); ok {
		t.Fatal("footer/hints should not be a text-select region")
	}
	if r, ok := m.textSelectRegionAt(tr.X+1, tr.Y+1); !ok || r != tr {
		t.Fatalf("transcript point: ok=%v r=%+v want %+v", ok, r, tr)
	}
	if r, ok := m.textSelectRegionAt(pr.X+1, pr.Y); !ok || r != pr {
		t.Fatalf("prompt point: ok=%v r=%+v want %+v", ok, r, pr)
	}
}

func TestMouseDragSelectsTranscriptAndIgnoresChrome(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.applyEvent(protocol.UserMessage{Text: "alpha bravo charlie delta"})
	m.refreshViewport()
	m.viewport.GotoTop()

	tr, ok := m.transcriptContentRect()
	if !ok {
		t.Fatal("no transcript rect")
	}
	x0, y0 := tr.X+2, tr.Y
	x1 := tr.X + 12

	m = updateApp(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      x0,
		Y:      y0,
	})
	if !m.textSel.dragging {
		t.Fatal("press in transcript should start drag")
	}
	m = updateApp(t, m, tea.MouseMsg{
		Action: tea.MouseActionMotion,
		Button: tea.MouseButtonLeft,
		X:      x1,
		Y:      y0,
	})
	m = updateApp(t, m, tea.MouseMsg{
		Action: tea.MouseActionRelease,
		Button: tea.MouseButtonLeft,
		X:      x1,
		Y:      y0,
	})
	if !m.textSel.has {
		t.Fatal("drag should finish with a selection")
	}
	if m.cellClip == nil || m.cellClip.osc == "" {
		t.Fatal("selection should stage OSC52 copy")
	}
	frame := m.View()
	if len(osc52Payloads(frame)) != 1 {
		t.Fatalf("view should emit one OSC52, got %d", len(osc52Payloads(frame)))
	}

	// Chrome press clears selection.
	m = updateApp(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      1,
		Y:      0,
	})
	if m.textSel.active() {
		t.Fatal("chrome press should clear text selection")
	}
}

func TestMouseDragOutsideRegionClamps(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.applyEvent(protocol.UserMessage{Text: "clamp test line"})
	m.refreshViewport()
	m.viewport.GotoTop()

	tr, ok := m.transcriptContentRect()
	if !ok {
		t.Fatal("no transcript rect")
	}
	m = updateApp(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      tr.X + 1,
		Y:      tr.Y,
	})
	// Drag into header chrome — focus must stay inside transcript rect.
	m = updateApp(t, m, tea.MouseMsg{
		Action: tea.MouseActionMotion,
		Button: tea.MouseButtonLeft,
		X:      tr.X + 1,
		Y:      0,
	})
	if !tr.contains(m.textSel.b.X, m.textSel.b.Y) {
		t.Fatalf("focus %+v escaped transcript %+v", m.textSel.b, tr)
	}
}

func TestMouseSelectDoesNotStartOnChrome(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.applyEvent(protocol.UserMessage{Text: "hi"})
	m.refreshViewport()

	m = updateApp(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      2,
		Y:      0, // header
	})
	if m.textSel.dragging || m.textSel.has {
		t.Fatal("header press must not start text selection")
	}
}

func TestPromptRegionAcceptsSelection(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.composer.SetValue("prompt body text for drag")
	m.reflow()

	pr, ok := m.promptContentRect()
	if !ok {
		t.Fatal("no prompt rect")
	}
	m = updateApp(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      pr.X + 1,
		Y:      pr.Y,
	})
	if !m.textSel.dragging {
		t.Fatal("press in prompt should start selection")
	}
	m = updateApp(t, m, tea.MouseMsg{
		Action: tea.MouseActionMotion,
		Button: tea.MouseButtonLeft,
		X:      pr.X + 8,
		Y:      pr.Y,
	})
	m = updateApp(t, m, tea.MouseMsg{
		Action: tea.MouseActionRelease,
		Button: tea.MouseButtonLeft,
		X:      pr.X + 8,
		Y:      pr.Y,
	})
	if !m.textSel.has {
		t.Fatal("prompt drag should yield a selection")
	}
}
