package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestLegendModalListsThemeIconsAndFilters(t *testing.T) {
	th := theme.Default().Resolve()
	th.Icons.Prompt = "P"
	th.Icons.OK = "Y"
	th.Icons.Err = "N"
	m := newLegendModal(th)
	if len(m.entries) < 10 {
		t.Fatalf("entries = %d, want a full legend", len(m.entries))
	}

	// Glyphs must come from the supplied theme, not DefaultIcons literals.
	foundPrompt, foundOK := false, false
	for _, e := range m.entries {
		if e.Glyph == "P" && strings.Contains(e.Description, "prompt") {
			foundPrompt = true
		}
		if e.Glyph == "Y" && strings.Contains(e.Description, "success") {
			foundOK = true
		}
		// Never hardcode stock DefaultIcons in the built list when overridden.
		if e.Glyph == "❯" || e.Glyph == "✓" {
			t.Errorf("legend used default glyph %q despite theme override", e.Glyph)
		}
	}
	if !foundPrompt || !foundOK {
		t.Fatalf("legend missing themed prompt/ok entries: prompt=%v ok=%v", foundPrompt, foundOK)
	}

	// Agent state labels appear for status chrome.
	for _, want := range []string{
		theme.AgentStateReady.Label(),
		theme.AgentStateWorking.Label(),
		theme.AgentStateAttention.Label(),
		theme.AgentStateError.Label(),
	} {
		found := false
		for _, e := range m.entries {
			if e.Glyph == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("legend omitted agent state %q", want)
		}
	}

	next, cmd := m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("prompt")})
	if next != m || cmd != nil {
		t.Fatal("typing filter closed legend modal or emitted a command")
	}
	filtered := m.filtered()
	if len(filtered) == 0 || !strings.Contains(strings.ToLower(filtered[0].Description), "prompt") {
		t.Fatalf("filter prompt = %#v, want prompt-related first", filtered)
	}
	view := ansi.Strip(m.view(80, th))
	if !strings.Contains(view, "Legend") {
		t.Errorf("legend view missing title:\n%s", view)
	}
	if !strings.Contains(view, "P") {
		t.Errorf("legend view missing themed prompt glyph:\n%s", view)
	}

	next, cmd = m.update(tea.KeyMsg{Type: tea.KeyEsc})
	if next != nil || cmd != nil {
		t.Fatal("escape did not close legend modal")
	}
}

func TestLegendModalTinyWidths(t *testing.T) {
	m := newLegendModal(theme.Default())
	for _, width := range []int{0, 1, 2, 3, 4} {
		if got := m.view(width, theme.Default()); got == "" {
			t.Errorf("view at width %d was empty", width)
		}
	}
}

func TestLegendModalUsesLiveThemeOnView(t *testing.T) {
	m := newLegendModal(theme.Default())
	th := theme.Default().Resolve()
	th.Icons.Tool = "T"
	view := ansi.Strip(m.view(80, th))
	if !strings.Contains(view, "T") {
		t.Errorf("view did not pick up live theme tool glyph:\n%s", view)
	}
}

func TestLegendSlashCommandOpensModal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.composer.SetValue("/legend")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	leg, ok := m.modal.(*legendModal)
	if !ok {
		t.Fatalf("/legend modal = %T, want legendModal", m.modal)
	}
	if m.notice != "" {
		t.Errorf("/legend set notice %q, want empty", m.notice)
	}
	if len(leg.entries) == 0 {
		t.Fatal("legend modal has no entries")
	}
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "Legend") {
		t.Errorf("/legend view missing title:\n%s", plain)
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.modal != nil {
		t.Errorf("esc left legend modal open: %T", m.modal)
	}
}
