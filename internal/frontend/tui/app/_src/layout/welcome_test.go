package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestWelcomeCardTitleUsesSoftBentoTones(t *testing.T) {
	th := theme.Default()
	// Titles keep multi-accent hierarchy without elevating panel body Tone.
	keys := welcomeCardTitle(th, "keys", ui.ToneAccent)
	if ansi.Strip(keys) != "keys" {
		t.Fatalf("keys title strip = %q", ansi.Strip(keys))
	}
	if keys == "keys" {
		t.Fatal("keys title should carry Accent SGR")
	}
	started := welcomeCardTitle(th, "get started", ui.ToneAccentAlt)
	if started == keys {
		t.Fatal("Accent vs AccentAlt titles should differ")
	}
	agents := welcomeCardTitle(th, "agents & skills", ui.ToneSuccess)
	if agents == keys || agents == started {
		t.Fatal("Success title should differ from Accent/AccentAlt")
	}
	plain := welcomeCardTitle(th, "recent prompts", ui.ToneMuted)
	if plain == keys {
		t.Fatal("Muted title should differ from Accent")
	}
}

func TestWelcomeDashboardRendersBentoCardsForEmptyTranscript(t *testing.T) {
	// welcomeView remains unit-testable; the live empty screen is home (#677).
	m, _ := newAppTestModelHome([]string{"build", "plan"}, []host.Skill{fakeSkill("review", "review code", "Review $ARGUMENTS")})
	plain := ansi.Strip(m.welcomeView(100, 30))
	for _, want := range []string{
		"get started",
		"anthropic",
		"/provider",
		"keys",
		"agents & skills",
		"build",
		"plan",
		"/review",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("welcomeView missing %q:\n%s", want, plain)
		}
	}
	keys := ansi.Strip(m.welcomeKeys())
	if strings.Contains(keys, "send") || strings.Contains(keys, "newline") {
		t.Errorf("welcome keys repeats composer actions: %q", keys)
	}
}

func TestWelcomeDashboardSurfacesRecentPromptsOnlyWithHistory(t *testing.T) {
	without, _ := newAppTestModelHome(nil, nil)
	if hasWelcomeCard(without.welcomeCards(without.services.Auth.Statuses()), "recent prompts") {
		t.Error("recent-prompts card without history")
	}

	store := newFakeHistory("earlier prompt one", "earlier prompt two")
	with, _ := newAppTestModelHomeWithHistory(nil, nil, store)
	if !hasWelcomeCard(with.welcomeCards(with.services.Auth.Statuses()), "recent prompts") {
		t.Error("recent-prompts card missing with history")
	}
	body := ansi.Strip(with.welcomeRecent(80, 3))
	if !strings.Contains(body, "earlier prompt two") {
		t.Errorf("welcomeRecent missing history:\n%s", body)
	}
}

func TestTranscriptReplacesWelcomeDashboardOnceCellsStream(t *testing.T) {
	m, _ := newAppTestModelHome(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if !m.showHomeLayout() {
		t.Fatal("empty transcript should show home layout")
	}

	m.applyEvent(protocol.UserMessage{Text: "hello strike"})
	m.refreshViewport()
	plain := ansi.Strip(viewString(m))
	if m.showHomeLayout() {
		t.Error("home layout persisted after a cell streamed")
	}
	if !strings.Contains(plain, "hello strike") {
		t.Errorf("transcript did not render the streamed user message:\n%s", plain)
	}
}

func TestWelcomeDashboardRecomputesOnResize(t *testing.T) {
	m, _ := newAppTestModelHome([]string{"build"}, nil)
	wide := ansi.Strip(m.welcomeView(120, 40))
	narrow := ansi.Strip(m.welcomeView(64, 24))
	if wide == narrow {
		t.Errorf("welcomeView did not repack on resize")
	}
	if !strings.Contains(narrow, "anthropic") {
		t.Errorf("narrow welcomeView dropped provider content:\n%s", narrow)
	}
}

func TestWelcomeConstrainedHeightKeepsPrimaryOnboarding(t *testing.T) {
	m, _ := newAppTestModel([]string{"build"}, []host.Skill{fakeSkill("review", "review code", "Review $ARGUMENTS")})
	cards := m.welcomeCards(m.services.Auth.Statuses())
	for len(cards) > 1 {
		cards = welcomeDropCard(cards)
	}
	if got := cards[0].title; got != "get started" {
		t.Fatalf("last welcome card = %q, want primary onboarding", got)
	}
}

func TestWelcomeKeysDoNotRepeatComposerActions(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	plain := ansi.Strip(m.welcomeKeys(80, 20))
	for _, repeated := range []string{"send", "newline"} {
		if strings.Contains(plain, repeated) {
			t.Errorf("welcome keys repeats composer action %q:\n%s", repeated, plain)
		}
	}
}

// TestLeftFocusHighlightsOnlyPromptNotWelcomeKeys locks #663: when the left
// (prompt) side has focus, only the composer panel uses focus chrome. The
// welcome keys card stays without BorderFocus / SurfaceFocus.
func TestLeftFocusHighlightsOnlyPromptNotWelcomeKeys(t *testing.T) {
	setTUITrueColor(t)
	th := theme.Default()
	th.Surface = fixedColor("#112233")
	th.SurfaceFocus = fixedColor("#445566")
	th.BorderFocus = fixedColor("#778899")
	th.SurfaceMuted = fixedColor("#aabbcc")
	th.Border = fixedColor("#ddeeff")
	m, _ := newAppTestModelHome(nil, nil)
	m.th = th
	m.providerName = "echo"
	m.modelName = "echo-1"
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.focus != focusLeft {
		t.Fatalf("focus = %v, want left", m.focus)
	}

	welcome := m.welcomeView(60, 20)
	if plain := ansi.Strip(welcome); !strings.Contains(plain, "keys") {
		t.Fatalf("welcome missing keys card:\n%s", plain)
	}
	if strings.Contains(welcome, rgbSGR("#778899")) {
		t.Fatal("welcome keys card used BorderFocus while left focus belongs to prompt")
	}
	if strings.Contains(welcome, rgbBGSGR("#445566")) {
		t.Fatal("welcome keys card used SurfaceFocus title edge while left focus belongs to prompt")
	}

	composer := m.composerView(false, 60, 6)
	if plain := ansi.Strip(composer); !strings.Contains(plain, "chat") {
		t.Fatalf("composer missing mode title:\n%s", plain)
	}
	if !strings.Contains(composer, rgbSGR("#778899")) {
		t.Fatal("prompt box missing BorderFocus when left-focused")
	}
	if !strings.Contains(composer, rgbBGSGR("#445566")) {
		t.Fatal("prompt box missing SurfaceFocus title edge when left-focused")
	}
}

func TestWelcomeDashboardUsesCustomThemeWithoutChangingContent(t *testing.T) {
	setTUITrueColor(t)
	agents := []string{"build", "plan"}
	skills := []host.Skill{fakeSkill("review", "review code", "Review $ARGUMENTS")}
	defaultModel, _ := newAppTestModelHome(agents, skills)
	defaultView := defaultModel.welcomeView(100, 30)

	th := theme.Default()
	th.Accent = fixedColor("#010203")
	th.Surface = fixedColor("#040506")
	th.SurfaceMuted = fixedColor("#040506")
	th.Spacing = theme.NewSpacing(1, 4, 3, 4)
	th.Icons.Agent = "A"
	th.Icons.Bolt = "B"
	customModel, _ := newAppTestModelHome(agents, skills)
	customModel.th = th
	custom := customModel.welcomeView(100, 30)

	for _, want := range []string{"get started", "anthropic", "/provider", "keys", "agents & skills", "build", "plan", "/review"} {
		if !strings.Contains(ansi.Strip(defaultView), want) || !strings.Contains(ansi.Strip(custom), want) {
			t.Errorf("theme changed semantic welcome content %q", want)
		}
	}
	plainCustom := ansi.Strip(custom)
	if !strings.Contains(plainCustom, "A build") || !strings.Contains(plainCustom, "B") {
		t.Errorf("custom glyph tokens are not observable:\n%s", custom)
	}
	if !strings.Contains(custom, rgbSGR("#010203")) || !strings.Contains(custom, rgbBGSGR("#040506")) {
		t.Errorf("custom color tokens are not observable: %q", custom)
	}
	if defaultView == custom || lipgloss.Width(custom) != 100 {
		t.Errorf("custom theme did not produce a distinct, width-safe welcome view")
	}
}
