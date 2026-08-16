package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
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

	next, cmd := m.update(tea.KeyPressMsg{Text: "prompt"})
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

	next, cmd = m.update(tea.KeyPressMsg{Code: tea.KeyEsc})
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

func TestLegendModalPaintsSemanticColors(t *testing.T) {
	setTUITrueColor(t)
	th := theme.Default().Resolve()
	th.Icons.OK = "Y"
	th.Icons.Err = "N"
	th.Icons.Prompt = "P"
	m := newLegendModal(th)

	// Paint roles must match live chrome tokens.
	wantPaint := map[string]legendPaint{
		"Y":                               legendPaintSuccess,
		"N":                               legendPaintError,
		"P":                               legendPaintUser,
		theme.AgentStateReady.Label():     legendPaintAgentReady,
		theme.AgentStateWorking.Label():   legendPaintAgentWorking,
		theme.AgentStateAttention.Label(): legendPaintAgentAttention,
		theme.AgentStateError.Label():     legendPaintAgentError,
	}
	for _, e := range m.entries {
		if want, ok := wantPaint[e.Glyph]; ok && e.Paint != want {
			t.Errorf("glyph %q paint = %v, want %v", e.Glyph, e.Paint, want)
		}
	}

	// Filter isolates each row so the 12-row window cannot hide samples.
	// Width 72 is the standard ModalWidth cap — Attention's long detail used
	// to overflow listRow and drop the yellow Prefix entirely (#475).
	cases := []struct {
		filter string
		sample string
	}{
		{"user prompt", th.S().UserLabel.Render("P")},
		{"success / completed", th.S().Success.Render("Y")},
		{"error / failed", th.S().Error.Render("N")},
		{"awaiting input", th.AgentStateStyle(theme.AgentStateReady).Render(theme.AgentStateReady.Label())},
		{"tool loop in flight", th.AgentStateStyle(theme.AgentStateWorking).Render(theme.AgentStateWorking.Label())},
		{"needs you", th.AgentStateStyle(theme.AgentStateAttention).Render(theme.AgentStateAttention.Label())},
		{"permission, gate", th.AgentStateStyle(theme.AgentStateAttention).Render(theme.AgentStateAttention.Label())},
		{"failed turn, tool", th.AgentStateStyle(theme.AgentStateError).Render(theme.AgentStateError.Label())},
	}
	for _, width := range []int{72, 80} {
		for _, tc := range cases {
			m.filter = tc.filter
			m.cursor = 0
			view := m.view(width, th)
			if !strings.Contains(view, tc.sample) {
				t.Errorf("width %d filter %q: missing colored sample %q in:\n%q", width, tc.filter, tc.sample, view)
			}
		}
	}
}

func TestLegendAttentionSampleStaysYellowAtModalWidth(t *testing.T) {
	// /legend at a normal 80-col terminal uses ModalWidth 72; the needs-you
	// row must still show Warning yellow on the attention glyph sample.
	setTUITrueColor(t)
	th := theme.Default().Resolve()
	sample := th.AgentStateStyle(theme.AgentStateAttention).Render(theme.AgentStateAttention.Label())
	if !strings.Contains(sample, "38;2;") {
		t.Fatalf("expected TrueColor SGR in attention sample, got %q", sample)
	}
	// Warning dark yellow from theme.Default (#ffd84d → 255;216;77).
	if !strings.Contains(sample, "255;216;77") && !strings.Contains(sample, th.Warning.Dark) {
		// Adaptive may encode as RGB components; require non-empty color vs plain label.
		if sample == theme.AgentStateAttention.Label() {
			t.Fatal("attention sample has no color")
		}
	}

	m := newLegendModal(th)
	m.filter = "needs you"
	m.cursor = 0
	dialogW := 72 // ui.ModalWidth cap
	view := m.view(dialogW, th)
	if !strings.Contains(view, sample) {
		t.Fatalf("legend at width %d missing yellow attention sample %q in:\n%q", dialogW, sample, view)
	}
	if !strings.Contains(ansi.Strip(view), theme.AgentStateAttention.Label()) {
		t.Fatalf("legend lost attention glyph text:\n%s", ansi.Strip(view))
	}
}

func TestLegendPaintStylesMatchThemeTokens(t *testing.T) {
	th := theme.Default().Resolve()
	st := th.S()
	cases := []struct {
		paint legendPaint
		want  string
	}{
		{legendPaintSuccess, st.Success.Render("x")},
		{legendPaintError, st.Error.Render("x")},
		{legendPaintUser, st.UserLabel.Render("x")},
		{legendPaintAssistant, st.AssistantLabel.Render("x")},
		{legendPaintTool, st.ToolLabel.Render("x")},
		{legendPaintWarning, st.Warning.Render("x")},
		{legendPaintAccent, st.Accent.Render("x")},
		{legendPaintAccentAlt, st.AccentAlt.Render("x")},
		{legendPaintSelected, st.Selected.Render("x")},
		{legendPaintBorderFocus, st.BorderFocus.Render("x")},
		{legendPaintInputCursor, st.InputCursor.Render("x")},
		{legendPaintMuted, st.Muted.Render("x")},
		{legendPaintAgentReady, th.AgentStateStyle(theme.AgentStateReady).Render("x")},
		{legendPaintAgentWorking, th.AgentStateStyle(theme.AgentStateWorking).Render("x")},
		{legendPaintAgentAttention, th.AgentStateStyle(theme.AgentStateAttention).Render("x")},
		{legendPaintAgentError, th.AgentStateStyle(theme.AgentStateError).Render("x")},
		{legendPaintDefault, st.Text.Render("x")},
	}
	for _, tc := range cases {
		if got := tc.paint.style(th).Render("x"); got != tc.want {
			t.Errorf("paint %v style = %q, want %q", tc.paint, got, tc.want)
		}
	}
}

func TestLegendSlashCommandOpensModal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.composer.SetValue("/legend")
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	plain := ansi.Strip(viewString(m))
	if !strings.Contains(plain, "Legend") {
		t.Errorf("/legend view missing title:\n%s", plain)
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != nil {
		t.Errorf("esc left legend modal open: %T", m.modal)
	}
}
