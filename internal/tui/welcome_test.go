package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestWelcomeDashboardRendersBentoCardsForEmptyTranscript(t *testing.T) {
	m, _ := newAppTestModel([]string{"build", "plan"}, []host.Skill{fakeSkill("review", "review code", "Review $ARGUMENTS")})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	plain := ansi.Strip(m.View())
	for _, want := range []string{
		"get started",     // provider-status card title
		"anthropic",       // provider status drawn from host.Auth
		"echo",            // builtin provider
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
	// Keys card includes newline help when height allows (default welcomeKeys rows).
	keys := ansi.Strip(m.welcomeKeys())
	if !strings.Contains(keys, "shift+enter") && !strings.Contains(keys, "newline") {
		t.Errorf("welcome keys missing newline help: %q", keys)
	}
	if strings.Contains(plain, "S T R I K E") || strings.Contains(plain, "strike"+"┐") {
		t.Errorf("welcome dashboard retained removed standalone logo card:\n%s", plain)
	}
}

func TestWelcomeDashboardSurfacesRecentPromptsOnlyWithHistory(t *testing.T) {
	without, _ := newAppTestModel(nil, nil)
	without = updateApp(t, without, tea.WindowSizeMsg{Width: 120, Height: 80})
	if plain := ansi.Strip(without.View()); strings.Contains(plain, "recent prompts") {
		t.Errorf("recent-prompts card rendered without any history:\n%s", plain)
	}

	store := newFakeHistory("earlier prompt one", "earlier prompt two")
	with, _ := newAppTestModelWithHistory(nil, nil, store)
	with = updateApp(t, with, tea.WindowSizeMsg{Width: 120, Height: 80})
	plain := ansi.Strip(with.View())
	if !strings.Contains(plain, "recent prompts") || !strings.Contains(plain, "earlier prompt two") {
		t.Errorf("welcome dashboard did not surface recent history:\n%s", plain)
	}
}

func TestTranscriptReplacesWelcomeDashboardOnceCellsStream(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if plain := ansi.Strip(m.View()); !strings.Contains(plain, "get started") {
		t.Fatalf("empty transcript did not show the welcome dashboard:\n%s", plain)
	}

	m.applyEvent(protocol.UserMessage{Text: "hello strike"})
	m.refreshViewport()
	plain := ansi.Strip(m.View())
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
	wide := ansi.Strip(m.View())
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 64, Height: 24})
	narrow := ansi.Strip(m.View())
	if wide == narrow {
		t.Errorf("welcome dashboard did not repack on resize")
	}
	// The dashboard must still name a provider at the narrower width.
	if !strings.Contains(narrow, "anthropic") {
		t.Errorf("narrow welcome dashboard dropped provider content:\n%s", narrow)
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
	th.Border = fixedColor("#040506")
	th.BorderMuted = fixedColor("#040506")
	th.Spacing = theme.NewSpacing(1, 4, 3, 4)
	th.BorderStyle = theme.BorderStyle{Weight: theme.BorderWeightHeavy}
	th.Icons.Agent = "A"
	th.Icons.Bolt = "B"
	customModel, _ := newAppTestModelWithOptions(Options{Theme: &th})
	customModel.agents, customModel.skills = agents, skills
	customModel = updateApp(t, customModel, tea.WindowSizeMsg{Width: 100, Height: 30})

	custom := customModel.View()
	for _, want := range []string{"get started", "anthropic", "echo", "/provider", "keys", "agents & skills", "build", "plan", "/review"} {
		if !strings.Contains(ansi.Strip(defaultModel.View()), want) || !strings.Contains(ansi.Strip(custom), want) {
			t.Errorf("theme changed semantic welcome content %q", want)
		}
	}
	plainCustom := ansi.Strip(custom)
	if !strings.Contains(plainCustom, "┏") || !strings.Contains(plainCustom, "A build") || !strings.Contains(plainCustom, "B") {
		t.Errorf("custom border or glyph tokens are not observable:\n%s", custom)
	}
	if !strings.Contains(custom, rgbSGR("#010203")) || !strings.Contains(custom, rgbSGR("#040506")) {
		t.Errorf("custom color tokens are not observable: %q", custom)
	}
	if defaultModel.View() == custom || lipgloss.Width(custom) != 100 {
		t.Errorf("custom theme did not produce a distinct, width-safe welcome view")
	}
}
