package tui

import (
	"encoding/json"
	"os"
	"strings"
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

	render := func(name string, width, height int, build func(*Model)) {
		m, _ := newAppTestModel(
			[]string{"build", "plan"},
			[]host.Skill{
				fakeSkill("review", "review a change", "Review $ARGUMENTS"),
				fakeSkill("explain", "explain code", "Explain this"),
			},
		)
		m = updateApp(t, m, tea.WindowSizeMsg{Width: width, Height: height})
		build(&m)
		m.reflow()
		m.refreshViewport()
		t.Logf("\n=== %s (%dx%d) ===\n%s\n", name, width, height, m.View())
	}
	renderWithHistory := func(name string, width, height int, history *fakeHistory, build func(*Model)) {
		m, _ := newAppTestModelWithHistory(
			[]string{"build", "plan", "ship", "test", "overflow-agent"},
			[]host.Skill{fakeSkill("review", "review a change", "Review $ARGUMENTS"), fakeSkill("explain", "explain code", "Explain this"), fakeSkill("audit", "audit code", "Audit $ARGUMENTS"), fakeSkill("fix", "fix code", "Fix $ARGUMENTS"), fakeSkill("overflow-skill", "extra", "Extra")},
			history,
		)
		m = updateApp(t, m, tea.WindowSizeMsg{Width: width, Height: height})
		build(&m)
		m.reflow()
		m.refreshViewport()
		t.Logf("\n=== %s (%dx%d) ===\n%s\n", name, width, height, m.View())
	}
	render("80x24 left dashboard", 80, 24, func(m *Model) {})
	render("80x24 right context", 80, 24, func(m *Model) { m.focus = focusRight })
	render("80x24 right visualizer", 80, 24, func(m *Model) {
		m.focus = focusRight
		m.sessionID = "gallery-root"
		m.applyEvent(protocol.ModelSelected{Provider: "echo", Model: "echo-1"})
		m.applyEvent(protocol.AgentSelected{Name: "build"})
		m.applyEvent(protocol.UsageReported{
			Input:  protocol.KnownTokens(1200),
			Output: protocol.KnownTokens(400),
			Used:   protocol.KnownTokens(1600),
			Source: protocol.UsageSourceActual,
		})
		m.applyEvent(protocol.ToolCallBegin{CallID: "g1", Name: "read"})
		m.applyEvent(protocol.ToolCallEnd{CallID: "g1", Title: "main.go", Output: "ok"})
		m.vizFocusID = "gallery-root"
		if reg, ok := m.windows.activate(visualizerWindowID); ok {
			m.windows = reg
		}
		m.windows, _ = m.windows.broadcast(m.visualizerStateSnapshot())
	})
	render("93x40 split canonical (left=60 gutter=1 right=32)", 93, 40, func(m *Model) {})
	render("92 left single (left=92 right=0)", 92, 60, func(m *Model) {})
	render("120x40 split (left=80 gutter=1 right=39)", 120, 40, func(m *Model) {})
	render("120x48 session stack with telemetry", 120, 48, func(m *Model) {
		m.focus = focusRight
		m.workDir = "/gallery/proj"
		if reg, ok := m.windows.activate("context"); ok {
			m.windows = reg
		}
		// Seed a rich sample so gallery shows bars (normal pressure).
		for i, w := range m.windows.windows {
			tw, ok := w.(telemetryWindow)
			if !ok {
				continue
			}
			tw.has = true
			tw.sample = host.TelemetrySample{
				CPUHostOK: true, CPUHostPct: 42.3, CPUProcOK: true, CPUProcPct: 2.1,
				MemOK: true, MemUsedBytes: 10 * 1024 * 1024 * 1024, MemTotalBytes: 32 * 1024 * 1024 * 1024,
				DiskOK: true, DiskUsedBytes: 287 * 1024 * 1024 * 1024, DiskTotalBytes: 494 * 1024 * 1024 * 1024,
				DiskFreeBytes: 207 * 1024 * 1024 * 1024, DiskRoot: m.workDir,
			}
			windows := append([]window(nil), m.windows.windows...)
			windows[i] = tw
			m.windows.windows = windows
			break
		}
		m.windows, _ = m.windows.broadcast(m.contextStateSnapshot())
	})
	render("120 cycle (left=80 gutter=1 right=39)", 120, 80, func(m *Model) { m.focus = focusRight; m.windows = m.windows.cycle() })
	render("120 modal (left=80 gutter=1 right=39)", 120, 80, func(m *Model) {
		m.applyEvent(protocol.TurnStarted{})
		m.applyEvent(protocol.PermissionAsked{RequestID: "g", Permission: "bash", Patterns: []string{"go test ./...", "rm -rf build"}})
	})
	render("120 provider picker (left=80 gutter=1 right=39)", 120, 80, func(m *Model) {
		m.modal = newProviderModal(m.services, "echo", m.ops, m.th)
	})
	render("120 command palette (left=80 gutter=1 right=39)", 120, 80, func(m *Model) {
		m.applyEvent(protocol.ModelSelected{Provider: "echo", Model: "echo-1"})
		m.modal = newPaletteModal(m.commands, m.agents, m.currentPaletteAvailability())
	})
	renderWithHistory("160x45 long data and status", 160, 45, newFakeHistory(
		"C3-OLD-SENTINEL-MUST-NOT-RENDER",
		"C3-ASCII-LONG-MARKER "+strings.Repeat("abcdefghijklmnopqrstuvwxyz ", 12),
		"C3-WIDE-COMBINING-MARKER 界界界界界界界界 e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301e\u0301",
		"C3-CONTROL-SAFE-MARKER before\x00\x1b[2J\r\n\u0085after",
	), func(m *Model) {
		m.agents = []string{"agent-with-a-very-long-name-界界界界界界界", "another-agent", "overflow-agent"}
		m.services.Auth.(*fakeAuth).statuses = append(m.services.Auth.(*fakeAuth).statuses, host.ProviderStatus{Name: "gallery-unique-provider", Detail: "sign in", OAuth: true}, host.ProviderStatus{Name: "overflow-provider", Detail: "none"})
		m.setNotice("status: "+"界界界界界界界界界界界界界界界界界界界界", true)
		m.dangerouslySkipPermissions = true
		m.applyEvent(protocol.ModelSelected{Provider: "gallery-unique-provider", Model: "gallery-unique-model"})
		m.applyEvent(protocol.TurnStarted{})
	})
	render("160x45 danger modal", 160, 45, func(m *Model) {
		m.dangerouslySkipPermissions = true
		m.applyEvent(protocol.PermissionAsked{RequestID: "gallery-danger", Permission: "bash", Patterns: []string{"go test ./..."}})
	})

	render("120 busy transcript with tool cells", 120, 80, func(m *Model) {
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
}
