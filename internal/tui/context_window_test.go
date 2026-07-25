package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestFormatContextTokenPairUnknownIsNotZeroSlashZero(t *testing.T) {
	dash := theme.Default().Resolve().Icons.DetailSeparator
	got := formatContextTokenPair(protocol.UnknownTokens(), 0, false, dash)
	if got == "0/0" {
		t.Fatalf("unknown pair fabricated %q", got)
	}
	if !strings.Contains(got, dash) {
		t.Errorf("unknown pair = %q, want dash for unknown sides", got)
	}
	// Known zero is allowed and distinct from unknown.
	zero := formatContextTokenPair(protocol.KnownTokens(0), 0, true, dash)
	if zero != "0/0" {
		t.Errorf("known zero pair = %q, want 0/0", zero)
	}
}

func TestContextUsageRatioUnknown(t *testing.T) {
	if r := contextUsageRatio(protocol.UnknownTokens(), 1000, true); r != -1 {
		t.Errorf("unknown used ratio = %v, want -1", r)
	}
	if r := contextUsageRatio(protocol.KnownTokens(100), 0, false); r != -1 {
		t.Errorf("unknown limit ratio = %v, want -1", r)
	}
	if r := contextUsageRatio(protocol.KnownTokens(50), 100, true); r != 0.5 {
		t.Errorf("known ratio = %v, want 0.5", r)
	}
}

func TestContextWindowViewUnknownUsageNotZeroSlashZero(t *testing.T) {
	th := theme.Default()
	w := newContextWindow().resize(40, 16)
	updated, _ := w.update(contextStateMsg{
		WorkDir: "/tmp", SessionID: "s1",
		Provider: "echo", Model: "echo",
		// All unknown: no fabricated 0/0 occupancy.
	})
	plain := ansi.Strip(updated.view(th))
	if strings.Contains(plain, "0/0") {
		t.Fatalf("context view fabricated 0/0 for unknown usage:\n%s", plain)
	}
	// With known usage + limit, compact token pair appears.
	updated, _ = w.update(contextStateMsg{
		WorkDir: "/tmp", SessionID: "s1",
		Provider: "echo", Model: "echo",
		Used:              protocol.KnownTokens(1500),
		ContextLimit:      100_000,
		ContextLimitKnown: true,
		Input:             protocol.KnownTokens(1000),
		Output:            protocol.KnownTokens(500),
		Source:            protocol.UsageSourceActual,
	})
	plain = ansi.Strip(updated.view(th))
	if !strings.Contains(plain, "1.5k") {
		t.Errorf("known usage missing from view:\n%s", plain)
	}
	if !strings.Contains(plain, "100k") {
		t.Errorf("context limit missing from view:\n%s", plain)
	}
	if !strings.Contains(plain, "actual") {
		t.Errorf("source missing from view:\n%s", plain)
	}
}

func TestUsageReportedUpdatesModelAndContextView(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.providerName = "echo"
	m.modelName = "echo"

	// Unknown before any usage event.
	if m.usageUsed.Known {
		t.Fatal("usageUsed should start unknown")
	}
	snap := m.contextStateSnapshot()
	pair := formatContextTokenPair(snap.Used, snap.ContextLimit, snap.ContextLimitKnown, "—")
	if pair == "0/0" {
		t.Fatalf("snapshot pair fabricated 0/0 before usage: %q", pair)
	}

	m.applyEvent(protocol.UsageReported{
		Input:  protocol.KnownTokens(100),
		Output: protocol.KnownTokens(20),
		Used:   protocol.KnownTokens(120),
		Source: protocol.UsageSourceEstimated,
	})
	if !m.usageUsed.Known || m.usageUsed.N != 120 {
		t.Errorf("usageUsed = %+v, want known 120", m.usageUsed)
	}
	if m.usageSource != protocol.UsageSourceEstimated {
		t.Errorf("usageSource = %q", m.usageSource)
	}

	// Deliver limits without inventing usage zeros.
	m = updateApp(t, m, contextLimitsMsg{
		provider: "echo", model: "echo",
		contextTokens: 200_000, contextOK: true,
		outputTokens:  8_192, outputOK: true,
	})
	if !m.contextLimitKnown || m.contextLimit != 200_000 {
		t.Errorf("contextLimit = %d known=%v", m.contextLimit, m.contextLimitKnown)
	}

	// Broadcast into the context window and assert display.
	var ctxWin contextWindow
	found := false
	for _, w := range m.windows.windows {
		if c, ok := w.(contextWindow); ok {
			ctxWin = c
			found = true
			break
		}
	}
	if !found {
		t.Fatal("context window not in registry")
	}
	ctxWin = ctxWin.resize(40, 20).(contextWindow)
	updated, _ := ctxWin.update(m.contextStateSnapshot())
	plain := ansi.Strip(updated.view(theme.Default()))
	if strings.Contains(plain, "0/0") {
		t.Fatalf("view shows fabricated 0/0:\n%s", plain)
	}
	if !strings.Contains(plain, "120") {
		t.Errorf("expected used count in view:\n%s", plain)
	}
	if !strings.Contains(plain, "200k") {
		t.Errorf("expected context limit in view:\n%s", plain)
	}
}
