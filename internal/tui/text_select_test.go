package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
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

func TestApplyTextSelectionClipsToRegion(t *testing.T) {
	// Multi-line selection must not paint columns outside the hit region.
	style := lipgloss.NewStyle().Reverse(true)
	frame := "AAAAAAAAAA\nBBBBBBBBBB\nCCCCCCCCCC"
	region := contentRect{X: 2, Y: 0, W: 4, H: 3}
	sel := textSel{
		has:    true,
		a:      screenPos{X: 2, Y: 0},
		b:      screenPos{X: 5, Y: 2},
		region: region,
	}
	out := applyTextSelection(frame, sel, style)
	for y, line := range strings.Split(out, "\n") {
		if plain := ansi.Strip(line); plain != strings.Split(frame, "\n")[y] {
			t.Fatalf("row %d plain text changed: %q", y, plain)
		}
	}
	if text := extractTextSelection(frame, sel); text != "AAAA\nBBBB\nCCCC" {
		t.Fatalf("extract = %q, want region-clipped AAAA/BBBB/CCCC", text)
	}
	// Outside-region columns stay unstyled: restyle only region and compare.
	manual := frame
	for y := 0; y < 3; y++ {
		lines := strings.Split(manual, "\n")
		lines[y] = styleColumns(lines[y], 2, 6, style)
		manual = strings.Join(lines, "\n")
	}
	if out != manual {
		t.Fatalf("highlight escaped region\ngot:\n%s\nwant:\n%s", out, manual)
	}

	// Without region, middle rows still span the full line.
	open := textSel{has: true, a: screenPos{X: 1, Y: 0}, b: screenPos{X: 2, Y: 1}}
	if got := extractTextSelection(frame, open); got != "AAAAAAAAA\nBBB" {
		t.Fatalf("open extract = %q, want AAAAAAAAA\\nBBB", got)
	}
}

func TestSelectionDoesNotPaintRightPaneColumns(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m.applyEvent(protocol.UserMessage{Text: "left pane only selection line alpha"})
	m.applyEvent(protocol.TextDelta{Text: "left pane only selection line bravo"})
	m.refreshViewport()
	m.viewport.GotoTop()

	tr, ok := m.transcriptContentRect()
	if !ok || tr.W < 4 {
		t.Fatalf("transcript rect=%+v", tr)
	}
	// Right pane starts after the left stack; pick a column past the region.
	rightX := tr.X + tr.W + 4
	if rightX >= m.width {
		t.Skip("no columns past transcript region at this width")
	}
	sel := textSel{
		has:    true,
		a:      screenPos{X: tr.X, Y: tr.Y},
		b:      screenPos{X: tr.X + tr.W - 1, Y: tr.Y + min(1, tr.H-1)},
		region: tr,
	}
	base := m.renderFrame()
	styled := applyTextSelection(base, sel, lipgloss.NewStyle().Reverse(true))
	baseLines := strings.Split(base, "\n")
	styledLines := strings.Split(styled, "\n")
	for y := sel.a.Y; y <= sel.b.Y && y < len(styledLines); y++ {
		// Cells outside the region must match the unstyled base exactly.
		baseCell := ansi.Cut(baseLines[y], rightX, rightX+1)
		gotCell := ansi.Cut(styledLines[y], rightX, rightX+1)
		if baseCell != gotCell {
			t.Fatalf("row %d col %d changed outside region\nbase=%q\ngot=%q", y, rightX, baseCell, gotCell)
		}
	}
}

func TestModalOverlayFullBleedNoSpillAt80x24And120x40(t *testing.T) {
	for _, size := range []struct{ w, h int }{{80, 24}, {120, 40}} {
		m, _ := newAppTestModel(nil, nil)
		m = updateApp(t, m, tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m.modal = &appProbeModal{}
		m.reflow()
		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) != size.h {
			t.Errorf("%dx%d: lines=%d", size.w, size.h, len(lines))
		}
		for i, line := range lines {
			if w := ansi.StringWidth(line); w != size.w {
				t.Errorf("%dx%d row %d width=%d", size.w, size.h, i, w)
				break
			}
		}
		if !strings.Contains(ansi.Strip(view), "probe") {
			t.Errorf("%dx%d missing modal content", size.w, size.h)
		}
	}
}

func TestFocusedPaneHasChromeNotBodyWash(t *testing.T) {
	setTUITrueColor(t)
	th := theme.Default()
	th.Surface = fixedColor("#112233")
	th.SurfaceFocus = fixedColor("#445566")
	th.BorderFocus = fixedColor("#778899")
	th.SurfaceMuted = fixedColor("#aabbcc")
	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	view := m.View()
	// Title edge focus token present; body uses Surface not a full SurfaceFocus flood.
	if !strings.Contains(view, rgbBGSGR("#445566")) {
		t.Fatal("focused title edge missing SurfaceFocus")
	}
	if !strings.Contains(view, rgbBGSGR("#778899")) {
		t.Fatal("focused leading bar missing BorderFocus")
	}
	if !strings.Contains(view, rgbBGSGR("#112233")) {
		t.Fatal("focused body missing normal Surface")
	}
	// Dim right pane still tokenized.
	if !strings.Contains(view, rgbBGSGR("#aabbcc")) {
		t.Fatal("dim pane missing SurfaceMuted")
	}
}
