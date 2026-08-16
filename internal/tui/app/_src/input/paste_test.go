package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestPasteLineCountAndLargeDetection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		in        string
		wantLines int
		wantLarge bool
	}{
		{name: "empty", in: "", wantLines: 0, wantLarge: false},
		{name: "one line", in: "hello", wantLines: 1, wantLarge: false},
		{name: "two lines", in: "a\nb", wantLines: 2, wantLarge: false},
		{name: "three lines", in: "a\nb\nc", wantLines: 3, wantLarge: true},
		{name: "trailing newline three", in: "a\nb\nc\n", wantLines: 3, wantLarge: true},
		{name: "long single line", in: strings.Repeat("x", largePasteMinRunes), wantLines: 1, wantLarge: true},
		{name: "just under rune threshold", in: strings.Repeat("x", largePasteMinRunes-1), wantLines: 1, wantLarge: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pasteLineCount(tc.in); got != tc.wantLines {
				t.Errorf("pasteLineCount = %d, want %d", got, tc.wantLines)
			}
			if got := isLargePaste(tc.in); got != tc.wantLarge {
				t.Errorf("isLargePaste = %v, want %v", got, tc.wantLarge)
			}
		})
	}
}

func TestNormalizePasteCRLF(t *testing.T) {
	t.Parallel()
	if got := normalizePaste("a\r\nb\rc"); got != "a\nb\nc" {
		t.Errorf("normalizePaste = %q, want a\\nb\\nc", got)
	}
}

func TestExpandPendingPastesLongestFirst(t *testing.T) {
	t.Parallel()
	pastes := []pasteChip{
		{Placeholder: "[pasted 3 lines]", Content: "AAA"},
		{Placeholder: "[pasted 3 lines #2]", Content: "BBB"},
	}
	in := "prefix [pasted 3 lines #2] mid [pasted 3 lines] end"
	want := "prefix BBB mid AAA end"
	if got := expandPendingPastes(in, pastes); got != want {
		t.Errorf("expand = %q, want %q", got, want)
	}
}

func TestPrunePendingPastesDropsMissingChips(t *testing.T) {
	t.Parallel()
	pastes := []pasteChip{
		{Placeholder: "[pasted 3 lines]", Content: "a\nb\nc"},
		{Placeholder: "[pasted 4 lines]", Content: "w\nx\ny\nz"},
	}
	got := prunePendingPastes("keep [pasted 4 lines] only", pastes)
	if len(got) != 1 || got[0].Placeholder != "[pasted 4 lines]" {
		t.Fatalf("prune = %+v, want only 4-line chip", got)
	}
	if prunePendingPastes("nothing", pastes) != nil {
		t.Fatal("expected nil when no chips remain")
	}
}

func TestComposerLargePasteCollapsesToChip(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	body := "line1\nline2\nline3\nline4"
	m = updateApp(t, m, tea.PasteMsg{Content: body})

	wantChip := fmt.Sprintf("[pasted %d lines]", pasteLineCount(body))
	if got := m.composer.Value(); got != wantChip {
		t.Fatalf("composer = %q, want chip %q", got, wantChip)
	}
	if len(m.pendingPastes) != 1 || m.pendingPastes[0].Content != body {
		t.Fatalf("pendingPastes = %+v, want one entry with full body", m.pendingPastes)
	}
	if strings.Contains(m.composer.Value(), "line1") {
		t.Fatal("large paste flooded composer instead of collapsing")
	}
}

func TestComposerSmallPasteInsertsVerbatim(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	body := "short\npaste"
	m = updateApp(t, m, tea.PasteMsg{Content: body})

	if got := m.composer.Value(); got != body {
		t.Fatalf("composer = %q, want verbatim %q", got, body)
	}
	if len(m.pendingPastes) != 0 {
		t.Fatalf("pendingPastes = %+v, want empty for small paste", m.pendingPastes)
	}
}

func TestComposerPasteExpandsOnSend(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.providerName = "echo"
	m.modelName = "test"

	body := "alpha\nbeta\ngamma\ndelta"
	m = typeAppText(t, m, "note: ")
	m = updateApp(t, m, tea.PasteMsg{Content: body})

	chip := fmt.Sprintf("[pasted %d lines]", pasteLineCount(body))
	if !strings.Contains(m.composer.Value(), chip) {
		t.Fatalf("composer before send = %q, want chip %q", m.composer.Value(), chip)
	}

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	_ = runAllAppCmds(t, cmd)
	op := receiveAppOp(t, ops)
	ui, ok := op.(protocol.UserInput)
	if !ok {
		t.Fatalf("op = %T, want UserInput", op)
	}
	want := "note: " + body
	if ui.Text != want {
		t.Fatalf("UserInput.Text = %q, want expanded %q", ui.Text, want)
	}
	if m.composer.Value() != "" || len(m.pendingPastes) != 0 {
		t.Fatalf("composer after send = %q pastes=%+v, want cleared", m.composer.Value(), m.pendingPastes)
	}
}

func TestComposerMultiplePastesGetUniqueChips(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	a := "a1\na2\na3"
	b := "b1\nb2\nb3"
	m = updateApp(t, m, tea.PasteMsg{Content: a})
	m = updateApp(t, m, tea.KeyPressMsg{Text: " "})
	m = updateApp(t, m, tea.PasteMsg{Content: b})

	want := "[pasted 3 lines] [pasted 3 lines #2]"
	if got := m.composer.Value(); got != want {
		t.Fatalf("composer = %q, want %q", got, want)
	}
	if len(m.pendingPastes) != 2 {
		t.Fatalf("pendingPastes len = %d, want 2", len(m.pendingPastes))
	}
	expanded := m.composerTextExpanded()
	if expanded != a+" "+b {
		t.Fatalf("expanded = %q, want %q", expanded, a+" "+b)
	}
}

func TestComposerDeletingChipPrunesPendingPaste(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	body := "x\ny\nz"
	m = updateApp(t, m, tea.PasteMsg{Content: body})
	if len(m.pendingPastes) != 1 {
		t.Fatalf("setup: pendingPastes = %+v", m.pendingPastes)
	}
	m.resetComposer()
	if len(m.pendingPastes) != 0 || m.composer.Value() != "" {
		t.Fatalf("after reset: value=%q pastes=%+v", m.composer.Value(), m.pendingPastes)
	}
}

func TestComposerPasteKeepsDraftWhenNoProvider(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	// providerName empty by default

	body := "one\ntwo\nthree"
	m = updateApp(t, m, tea.PasteMsg{Content: body})
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	assertNoAppOp(t, ops)
	chip := fmt.Sprintf("[pasted %d lines]", pasteLineCount(body))
	if m.composer.Value() != chip {
		t.Fatalf("composer = %q, want kept chip %q", m.composer.Value(), chip)
	}
	if len(m.pendingPastes) != 1 {
		t.Fatalf("pendingPastes should remain until send succeeds, got %+v", m.pendingPastes)
	}
}
