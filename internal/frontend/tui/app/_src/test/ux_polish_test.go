package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestWorkingHeaderElapsedAndTools(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.providerName = "echo"
	m.modelName = "echo-1"

	m.applyEvent(protocol.TurnStarted{})
	// Backdate so the compact duration is non-zero and stable.
	m.turnStartedAt = time.Now().Add(-5 * time.Second)
	m.applyEvent(protocol.ToolCallBegin{CallID: "c1", Name: "bash"})

	header := ansi.Strip(m.headerView(100))
	for _, want := range []string{"working", "tool call", "s"} {
		if !strings.Contains(header, want) {
			t.Errorf("working header missing %q:\n%s", want, header)
		}
	}

	m.applyEvent(protocol.ToolCallBegin{CallID: "c2", Name: "read"})
	m.applyEvent(protocol.ToolCallBegin{CallID: "c3", Name: "grep"})
	header = ansi.Strip(m.headerView(100))
	if !strings.Contains(header, "3") || !strings.Contains(header, "tool calls") {
		t.Errorf("header after 3 tools missing count:\n%s", header)
	}

	m.applyEvent(protocol.TurnCompleted{StopReason: "end_turn"})
	header = ansi.Strip(m.headerView(100))
	if strings.Contains(header, "tool call") {
		t.Errorf("working tool-call chrome persisted after TurnCompleted:\n%s", header)
	}
	if strings.Contains(header, "working (") {
		t.Errorf("working elapsed chrome persisted after TurnCompleted:\n%s", header)
	}
	if m.agentState() != theme.AgentStateReady {
		t.Errorf("expected ready state after successful turn, got %v", m.agentState())
	}
}

// TestHeaderKeepsWorkingWithMeterAtNarrowWidth pins that the budget-aware
// context meter shrinks on tight widths so StatusBar still shows "working"
// rather than dropping the entire right-side status.
func TestHeaderKeepsWorkingWithMeterAtNarrowWidth(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.providerName = "echo"
	m.modelName = "echo-1"
	m.applyEvent(protocol.UsageReported{
		Used:   protocol.KnownTokens(12_345),
		Source: protocol.UsageSourceActual,
	})
	m = updateApp(t, m, contextLimitsMsg{
		provider: "echo", model: "echo-1",
		contextTokens: 200_000, contextOK: true,
	})
	m.applyEvent(protocol.TurnStarted{})
	m.turnStartedAt = time.Now().Add(-3 * time.Second)

	// Autonomy + permission-mode badges need a few more columns than the
	// pre-dial floor; meter still shrinks first so working stays on the right.
	header := ansi.Strip(m.headerView(64))
	if !strings.Contains(header, "working") {
		t.Errorf("narrow header dropped working status despite budget-aware meter:\n%s", header)
	}
}

func TestHealthBadgeInHeader(t *testing.T) {
	setTUITrueColor(t)

	th := theme.Default()
	th.Success = fixedColor("#0a0b0c")
	th.Background = theme.NoBackground()

	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.providerName = "echo"
	m.modelName = "echo-1"
	m.services.Auth = &fakeAuth{statuses: []host.ProviderStatus{
		{Name: "echo", Detail: "offline dev provider", Authed: true, Builtin: true},
	}}

	tone, ok := providerHealthTone(m)
	if !ok || tone != ui.ToneSuccess {
		t.Fatalf("providerHealthTone = %v, %v; want Success, true", tone, ok)
	}

	header := m.headerView(100)
	plain := ansi.Strip(header)
	if !strings.Contains(plain, "echo") {
		t.Fatalf("header missing provider name:\n%s", plain)
	}
	if !strings.Contains(header, rgbSGR("#0a0b0c")) {
		t.Errorf("header missing success health color:\n%q", header)
	}
}

func TestHeaderHidesNormalPostureAndShowsExceptionalPosture(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	plain := ansi.Strip(m.headerView(100))
	if strings.Contains(plain, "auto sup") || strings.Contains(plain, " def ") {
		t.Fatalf("normal posture adds redundant header badges:\n%s", plain)
	}

	m.autonomy = protocol.AutonomyAgent
	m.permMode = protocol.PermissionModeYolo
	plain = ansi.Strip(m.headerView(100))
	for _, want := range []string{"auto agent", "yolo"} {
		if !strings.Contains(plain, want) {
			t.Errorf("exceptional posture missing %q:\n%s", want, plain)
		}
	}
}

func TestAuthExpiryNoticeOnce(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.providerName = "openai"
	m.services.Auth = &fakeAuth{statuses: []host.ProviderStatus{
		{
			Name:      "openai",
			Detail:    "oauth",
			Authed:    true,
			OAuth:     true,
			ExpiresAt: time.Now().Add(time.Hour),
		},
	}}

	cmd := m.authExpiryNoticeCmd()
	if cmd == nil {
		t.Fatal("expected auth expiry notice cmd when expiry is within warn window")
	}
	msg := runAppCmd(t, cmd)
	if _, ok := msg.(authExpiryNoticeMsg); !ok {
		t.Fatalf("cmd msg = %T, want authExpiryNoticeMsg", msg)
	}

	m = updateApp(t, m, msg)
	if !m.authExpiryNoticed {
		t.Fatal("authExpiryNoticed not set after notice msg")
	}
	if !strings.Contains(m.notice, "/auth") {
		t.Errorf("notice = %q, want /auth hint", m.notice)
	}

	if cmd := m.authExpiryNoticeCmd(); cmd != nil {
		t.Error("second authExpiryNoticeCmd should be nil once noticed")
	}

	// Applying the notice msg again must not duplicate or clear the flag.
	prior := m.notice
	m = updateApp(t, m, authExpiryNoticeMsg{})
	if m.notice != prior {
		t.Errorf("duplicate notice application changed notice %q → %q", prior, m.notice)
	}
}

func TestAuthExpiryNoticeViaModelSelected(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Auth = &fakeAuth{statuses: []host.ProviderStatus{
		{
			Name:      "openai",
			Detail:    "oauth",
			Authed:    true,
			OAuth:     true,
			ExpiresAt: time.Now().Add(time.Hour),
		},
	}}

	cmd := m.applyEvent(protocol.ModelSelected{Provider: "openai", Model: "gpt-test"})
	var sawNotice bool
	for _, msg := range runAllAppCmds(t, cmd) {
		if _, ok := msg.(authExpiryNoticeMsg); ok {
			sawNotice = true
			m = updateApp(t, m, msg)
		}
	}
	if !sawNotice {
		t.Fatal("ModelSelected did not emit authExpiryNoticeMsg for soon-expiring OAuth")
	}
	if !strings.Contains(m.notice, "/auth") {
		t.Errorf("notice after ModelSelected = %q", m.notice)
	}
}

func TestFirstRunWelcomeCard(t *testing.T) {
	// welcomeView still carries first-run onboarding cards; live empty UI is home.
	first, _ := newAppTestModelHome(nil, nil)
	first.firstRun = true
	first.providerName = ""
	plain := ansi.Strip(first.welcomeView(100, 30))
	for _, want := range []string{"first run", "/auth", "/model"} {
		if !strings.Contains(plain, want) {
			t.Errorf("first-run welcome missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "get started") {
		t.Errorf("first-run welcome still showed get started:\n%s", plain)
	}

	// Default FirstRun false keeps existing unauthed "get started" card.
	normal, _ := newAppTestModelHome(nil, nil)
	normal.providerName = ""
	plain = ansi.Strip(normal.welcomeView(100, 30))
	if !strings.Contains(plain, "get started") {
		t.Errorf("default welcome missing get started:\n%s", plain)
	}
	if strings.Contains(plain, "first run") {
		t.Errorf("default welcome showed first run:\n%s", plain)
	}

	// Home context bar surfaces first-run when Options say so.
	home, _ := newAppTestModelHome(nil, nil)
	home.firstRun = true
	home.providerName = ""
	home = updateApp(t, home, tea.WindowSizeMsg{Width: 100, Height: 30})
	if plain := ansi.Strip(viewString(home)); !strings.Contains(plain, "first run") {
		t.Errorf("home context bar missing first run:\n%s", plain)
	}
}

func TestFirstRunOpensFTUEModalOnce(t *testing.T) {
	m, _ := newAppTestModelWithOptions(Options{FirstRun: true})
	if !m.firstRun {
		t.Fatal("Options{FirstRun:true} did not set firstRun")
	}

	m = updateApp(t, m, firstRunSetupMsg{})
	if _, ok := m.modal.(*ftueModal); !ok {
		t.Fatalf("first setup modal = %T, want *ftueModal", m.modal)
	}
	if !m.firstRunModalOpened {
		t.Fatal("firstRunModalOpened not set")
	}

	// Clear modal as if the user dismissed it; second setup must not reopen.
	m.modal = nil
	m = updateApp(t, m, firstRunSetupMsg{})
	if m.modal != nil {
		t.Fatalf("second firstRunSetupMsg reopened modal: %T", m.modal)
	}
}

func TestOnboardingServiceDrivesFirstRun(t *testing.T) {
	ops := make(chan protocol.Op, 8)
	events := make(chan protocol.Event)
	svc := testServices(nil, nil)
	ob := &fakeOnboarding{autoOpen: true}
	svc.Onboarding = ob
	m := New(ops, events, svc)
	if !m.firstRun {
		t.Fatal("ShouldAutoOpen should set firstRun")
	}
	m = updateApp(t, m, firstRunSetupMsg{})
	if _, ok := m.modal.(*ftueModal); !ok {
		t.Fatalf("modal = %T, want *ftueModal", m.modal)
	}
}

func TestFTUEDismissAcknowledgesOnboarding(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	ob := &fakeOnboarding{autoOpen: true}
	m.services.Onboarding = ob
	m.firstRun = true
	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != nil {
		t.Fatalf("modal after esc = %T", m.modal)
	}
	if ob.acks != 1 {
		t.Fatalf("acks = %d, want 1", ob.acks)
	}
	if m.firstRun {
		t.Fatal("firstRun should clear after ack msg")
	}
	if ob.autoOpen {
		t.Fatal("autoOpen should be false after ack")
	}
}

func TestFTUEFinishAcknowledgesOnboarding(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	ob := &fakeOnboarding{autoOpen: true}
	m.services.Onboarding = ob
	m.firstRun = true
	m.providerName = "echo"
	m.modelName = "echo"
	m.width, m.height, m.ready = 100, 40, true
	next, _ := m.handleCommand("/ftue")
	m = next.(Model)
	m = updateAppDrain(t, m, tea.KeyPressMsg{Code: 'f', Text: "f"})
	if ob.acks != 1 {
		t.Fatalf("acks = %d, want 1", ob.acks)
	}
	if m.firstRun {
		t.Fatal("firstRun should clear after finish ack")
	}
}

func TestHeaderShowsPhaseBadge(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m.providerName = "echo"
	m.modelName = "echo"
	m.agentName = "plan"
	m.applyEvent(protocol.PhaseChanged{
		Workflow: "plan-implement",
		Phase:    "plan",
		Index:    0,
		Gate:     "user",
	})
	header := ansi.Strip(m.headerView(120))
	if !strings.Contains(header, "phase") || !strings.Contains(header, "plan") {
		t.Fatalf("header missing phase badge:\n%s", header)
	}
	m.applyEvent(protocol.PhaseChanged{}) // clear
	header = ansi.Strip(m.headerView(120))
	if strings.Contains(header, "phase ") {
		t.Fatalf("phase badge persisted after clear:\n%s", header)
	}
}
