package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestWelcomeDashboardRendersBentoCardsForEmptyTranscript(t *testing.T) {
	m, _ := newAppTestModel([]string{"build", "plan"}, []host.Skill{fakeSkill("review", "review code", "Review $ARGUMENTS")})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	plain := ansi.Strip(m.View())
	for _, want := range []string{
		"S T R I K E",     // logo wordmark card
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
}

func TestWelcomeDashboardSurfacesRecentPromptsOnlyWithHistory(t *testing.T) {
	without, _ := newAppTestModel(nil, nil)
	without = updateApp(t, without, tea.WindowSizeMsg{Width: 100, Height: 30})
	if plain := ansi.Strip(without.View()); strings.Contains(plain, "recent prompts") {
		t.Errorf("recent-prompts card rendered without any history:\n%s", plain)
	}

	store := newFakeHistory("earlier prompt one", "earlier prompt two")
	with, _ := newAppTestModelWithHistory(nil, nil, store)
	with = updateApp(t, with, tea.WindowSizeMsg{Width: 100, Height: 30})
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
