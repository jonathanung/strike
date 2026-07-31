package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
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
	m, _ := newAppTestModel([]string{"build", "plan"}, []host.Skill{fakeSkill("review", "review code", "Review $ARGUMENTS")})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	plain := ansi.Strip(viewString(m))
	for _, want := range []string{
		"get started",     // provider-status card title
		"anthropic",       // provider status drawn from host.Auth
		"/provider",       // get-started hint
		"keys",            // keybinding card
		"agents & skills", // agents/skills card
		"build",           // selectable agent
		"plan",            // second agent
		"/review",         // skill from services
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("welcome dashboard missing %q:\n%s", want, plain)
		}
	}
	// Composer actions stay in composer chrome instead of repeating in this card.
	keys := ansi.Strip(m.welcomeKeys())
	if strings.Contains(keys, "send") || strings.Contains(keys, "newline") {
		t.Errorf("welcome keys repeats composer actions: %q", keys)
	}
	// Standalone titled "logo" card is gone; a Logo band (S T R I K E) may still
	// appear above the card grid when height allows — that is intentional chrome.
	if strings.Contains(plain, "strike"+"┐") || strings.Contains(plain, "╭─ logo") {
		t.Errorf("welcome dashboard retained removed standalone logo card:\n%s", plain)
	}
}

func TestWelcomeDashboardSurfacesRecentPromptsOnlyWithHistory(t *testing.T) {
	without, _ := newAppTestModel(nil, nil)
	without = updateApp(t, without, tea.WindowSizeMsg{Width: 120, Height: 80})
	if plain := ansi.Strip(viewString(without)); strings.Contains(plain, "recent prompts") {
		t.Errorf("recent-prompts card rendered without any history:\n%s", plain)
	}

	store := newFakeHistory("earlier prompt one", "earlier prompt two")
	with, _ := newAppTestModelWithHistory(nil, nil, store)
	with = updateApp(t, with, tea.WindowSizeMsg{Width: 120, Height: 80})
	plain := ansi.Strip(viewString(with))
	if !strings.Contains(plain, "recent prompts") || !strings.Contains(plain, "earlier prompt two") {
		t.Errorf("welcome dashboard did not surface recent history:\n%s", plain)
	}
}

func TestTranscriptReplacesWelcomeDashboardOnceCellsStream(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if plain := ansi.Strip(viewString(m)); !strings.Contains(plain, "get started") {
		t.Fatalf("empty transcript did not show the welcome dashboard:\n%s", plain)
	}

	m.applyEvent(protocol.UserMessage{Text: "hello strike"})
	m.refreshViewport()
	plain := ansi.Strip(viewString(m))
	if strings.Contains(plain, "get started") {
		t.Errorf("welcome dashboard persisted after a cell streamed:\n%s", plain)
	}
	if !strings.Contains(plain, "hello strike") {
		t.Errorf("transcript did not render the streamed user message:\n%s", plain)
	}
}

func TestWelcomeDashboardRecomputesOnResize(t *testing.T) {
	m, _ := newAppTestModel([]string{"build"}, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	wide := ansi.Strip(viewString(m))
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 64, Height: 24})
	narrow := ansi.Strip(viewString(m))
	if wide == narrow {
		t.Errorf("welcome dashboard did not repack on resize")
	}
	// The dashboard must still name a provider at the narrower width.
	if !strings.Contains(narrow, "anthropic") {
		t.Errorf("narrow welcome dashboard dropped provider content:\n%s", narrow)
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
// welcome keys card (primary when the selected provider is already authed)
// stays visible without BorderFocus / SurfaceFocus.
func TestLeftFocusHighlightsOnlyPromptNotWelcomeKeys(t *testing.T) {
	setTUITrueColor(t)
	th := theme.Default()
	th.Surface = fixedColor("#112233")
	th.SurfaceFocus = fixedColor("#445566")
	th.BorderFocus = fixedColor("#778899")
	th.SurfaceMuted = fixedColor("#aabbcc")
	th.Border = fixedColor("#ddeeff")
	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	// Authed selected provider drops "get started", so keys becomes primary.
	m.providerName = "echo"
	m.modelName = "echo-1"
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.focus != focusLeft {
		t.Fatalf("focus = %v, want left", m.focus)
	}
	cards := m.welcomeCards(m.services.Auth.Statuses())
	if len(cards) == 0 || cards[0].title != "keys" || !cards[0].primary {
		t.Fatalf("welcome cards = %+v, want primary keys first", cards)
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
	if plain := ansi.Strip(composer); !strings.Contains(plain, "prompt") {
		t.Fatalf("composer missing prompt title:\n%s", plain)
	}
	if !strings.Contains(composer, rgbSGR("#778899")) {
		t.Fatal("prompt box missing BorderFocus when left-focused")
	}
	if !strings.Contains(composer, rgbBGSGR("#445566")) {
		t.Fatal("prompt box missing SurfaceFocus title edge when left-focused")
	}

	// Full frame: keys still present; focus chrome still comes from the prompt.
	view := viewString(m)
	if plain := ansi.Strip(view); !strings.Contains(plain, "keys") || !strings.Contains(plain, "prompt") {
		t.Fatalf("full view missing keys or prompt:\n%s", plain)
	}
	if !strings.Contains(view, rgbSGR("#778899")) {
		t.Fatal("left-focused full view missing prompt BorderFocus")
	}
}

func TestWelcomeDashboardUsesCustomThemeWithoutChangingContent(t *testing.T) {
	setTUITrueColor(t)
	agents := []string{"build", "plan"}
	skills := []host.Skill{fakeSkill("review", "review code", "Review $ARGUMENTS")}
	defaultModel, _ := newAppTestModel(agents, skills)
	defaultModel = updateApp(t, defaultModel, tea.WindowSizeMsg{Width: 100, Height: 30})

	th := theme.Default()
	th.Accent = fixedColor("#010203")
	th.Surface = fixedColor("#040506")
	th.SurfaceMuted = fixedColor("#040506")
	th.Spacing = theme.NewSpacing(1, 4, 3, 4)
	th.Icons.Agent = "A"
	th.Icons.Bolt = "B"
	customModel, _ := newAppTestModelWithOptions(Options{Theme: &th})
	customModel.agents, customModel.skills = agents, skills
	customModel = updateApp(t, customModel, tea.WindowSizeMsg{Width: 100, Height: 30})

	custom := viewString(customModel)
	for _, want := range []string{"get started", "anthropic", "/provider", "keys", "agents & skills", "build", "plan", "/review"} {
		if !strings.Contains(ansi.Strip(viewString(defaultModel)), want) || !strings.Contains(ansi.Strip(custom), want) {
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
	if viewString(defaultModel) == custom || lipgloss.Width(custom) != 100 {
		t.Errorf("custom theme did not produce a distinct, width-safe welcome view")
	}
}
