package tui

import (
	"encoding/json"
	"os"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// TestGallery renders representative full-screen views to the test log for
// human/orchestrator eyeballing. It is a silent no-op unless STRIKE_GALLERY=1,
// so it never affects the normal suite:
//
//	STRIKE_GALLERY=1 go test ./internal/tui/ -run Gallery -count=1 -v
func TestGallery(t *testing.T) {
	if os.Getenv("STRIKE_GALLERY") != "1" {
		t.Skip("set STRIKE_GALLERY=1 to render the view gallery")
	}

	render := func(name string, build func(*Model)) {
		m, _ := newAppTestModel(
			[]string{"build", "plan"},
			[]host.Skill{
				fakeSkill("review", "review a change", "Review $ARGUMENTS"),
				fakeSkill("explain", "explain code", "Explain this"),
			},
		)
		m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
		build(&m)
		m.reflow()
		m.refreshViewport()
		t.Logf("\n=== %s ===\n%s\n", name, m.View())
	}

	render("welcome dashboard (100x30)", func(m *Model) {})

	render("busy transcript with tool cells (100x30)", func(m *Model) {
		m.applyEvent(protocol.ModelSelected{Provider: "anthropic", Model: "claude-sonnet-5"})
		m.applyEvent(protocol.AgentSelected{Name: "build"})
		m.applyEvent(protocol.UserMessage{Text: "Refactor the auth store, then run the tests."})
		m.applyEvent(protocol.TurnStarted{})
		m.applyEvent(protocol.TextDelta{Text: "On it — reading the store first, then running the suite."})
		m.applyEvent(protocol.ToolCallBegin{CallID: "1", Name: "read", Args: json.RawMessage(`{"path":"internal/auth/store.go"}`)})
		m.applyEvent(protocol.ToolCallEnd{CallID: "1", Title: "internal/auth/store.go", Output: "package auth\n\ntype Store struct {\n\tpath string\n}\n"})
		m.applyEvent(protocol.ToolCallBegin{CallID: "2", Name: "bash", Args: json.RawMessage(`{"cmd":"go test ./..."}`)})
		m.applyEvent(protocol.ToolCallEnd{CallID: "2", Title: "go test ./...", Output: "ok  \tstrike/internal/auth\t0.20s\nok  \tstrike/internal/tui\t0.31s"})
	})

	render("permission modal (100x30)", func(m *Model) {
		m.applyEvent(protocol.TurnStarted{})
		m.applyEvent(protocol.PermissionAsked{RequestID: "g", Permission: "bash", Patterns: []string{"go test ./...", "rm -rf build"}})
	})

	render("provider picker (100x30)", func(m *Model) {
		m.modal = newProviderModal(m.services, "echo", m.ops, m.th)
	})

	render("command palette (100x30)", func(m *Model) {
		m.applyEvent(protocol.ModelSelected{Provider: "echo", Model: "echo-1"})
		m.modal = newPaletteModal(m.commands, m.agents, m.currentPaletteAvailability())
	})
}
