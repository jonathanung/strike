package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestUsageTotalsAccumulateKnownPartsOnly(t *testing.T) {
	var totals usageTotals
	totals.add(protocol.UsageReported{
		Input:         protocol.KnownTokens(100),
		Output:        protocol.KnownTokens(20),
		CacheRead:     protocol.KnownTokens(10),
		CacheCreation: protocol.KnownTokens(5),
		Used:          protocol.KnownTokens(135),
		Source:        protocol.UsageSourceActual,
	})
	totals.add(protocol.UsageReported{
		Input:  protocol.KnownTokens(50),
		Output: protocol.UnknownTokens(),
		Used:   protocol.KnownTokens(50),
		Source: protocol.UsageSourceEstimated,
	})
	if totals.Reports != 2 {
		t.Fatalf("reports = %d, want 2", totals.Reports)
	}
	if !totals.InputOK || totals.Input != 150 {
		t.Errorf("input = %d ok=%v, want 150 true", totals.Input, totals.InputOK)
	}
	if !totals.OutputOK || totals.Output != 20 {
		t.Errorf("output = %d ok=%v, want 20 true (second report omitted)", totals.Output, totals.OutputOK)
	}
	if !totals.CacheReadOK || totals.CacheRead != 10 {
		t.Errorf("cacheRead = %d ok=%v, want 10 true", totals.CacheRead, totals.CacheReadOK)
	}
	if !totals.CacheCreateOK || totals.CacheCreation != 5 {
		t.Errorf("cacheCreation = %d ok=%v, want 5 true", totals.CacheCreation, totals.CacheCreateOK)
	}
	if !totals.AnyActual || !totals.AnyEstimated {
		t.Errorf("source flags actual=%v estimated=%v", totals.AnyActual, totals.AnyEstimated)
	}
	if usageSourceLabel(totals) != "mixed (actual + estimated)" {
		t.Errorf("source label = %q", usageSourceLabel(totals))
	}
}

func TestEstimateUSDNoFabrication(t *testing.T) {
	empty := usageTotals{}
	if _, ok, _ := estimateUSD(empty, 3, 15, true); ok {
		t.Fatal("empty totals must not yield a cost")
	}
	if _, ok, _ := estimateUSD(usageTotals{InputOK: true, Input: 1_000_000}, 3, 15, false); ok {
		t.Fatal("missing catalog rate must not yield a cost")
	}
	usd, ok, partial := estimateUSD(usageTotals{InputOK: true, Input: 1_000_000}, 3, 15, true)
	if !ok || !partial || usd != 3 {
		t.Errorf("partial input-only = usd=%v ok=%v partial=%v, want 3 true true", usd, ok, partial)
	}
	usd, ok, partial = estimateUSD(usageTotals{
		InputOK: true, Input: 1_000_000,
		OutputOK: true, Output: 1_000_000,
	}, 2.5, 10, true)
	if !ok || partial || usd != 12.5 {
		t.Errorf("full = usd=%v ok=%v partial=%v, want 12.5 true false", usd, ok, partial)
	}
}

func TestCostSlashCommandOpensModalWithTotals(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.modelName = "echo"
	m.modelHasCost = true
	m.modelInputCost = 1
	m.modelOutputCost = 2
	m.applyEvent(protocol.UsageReported{
		Input:         protocol.KnownTokens(1000),
		Output:        protocol.KnownTokens(500),
		CacheRead:     protocol.KnownTokens(100),
		CacheCreation: protocol.KnownTokens(0),
		Used:          protocol.KnownTokens(1600),
		Source:        protocol.UsageSourceEstimated,
	})
	m.applyEvent(protocol.UsageReported{
		Input:  protocol.KnownTokens(200),
		Output: protocol.KnownTokens(50),
		Used:   protocol.KnownTokens(250),
		Source: protocol.UsageSourceEstimated,
	})
	if m.usageSession.Input != 1200 || m.usageSession.Output != 550 {
		t.Fatalf("session totals = in %d out %d", m.usageSession.Input, m.usageSession.Output)
	}

	m.composer.SetValue("/cost")
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	runAppCmd(t, cmd)
	assertNoAppOp(t, ops)

	cm, ok := m.modal.(*costModal)
	if !ok {
		t.Fatalf("modal type = %T, want *costModal", m.modal)
	}
	plain := ansi.Strip(cm.view(72, theme.Default()))
	for _, want := range []string{"Session totals", "1.2k", "550", "100", "estimated", "est. cost"} {
		if !strings.Contains(plain, want) {
			t.Errorf("cost modal missing %q:\n%s", want, plain)
		}
	}
	// Close on esc.
	next, _ := cm.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if next != nil {
		t.Fatal("esc did not close cost modal")
	}
}

func TestCostModalUnknownWhenNoUsage(t *testing.T) {
	m := newCostModal(usageTotals{}, protocol.TokenCount{}, protocol.TokenCount{}, protocol.TokenCount{}, protocol.TokenCount{}, protocol.TokenCount{}, "", "echo", "echo", 0, 0, false, 0, false)
	plain := ansi.Strip(m.view(60, theme.Default()))
	if !strings.Contains(plain, "No usage reported") {
		t.Fatalf("empty cost modal:\n%s", plain)
	}
	if strings.Contains(plain, "0/0") {
		t.Fatalf("fabricated 0/0:\n%s", plain)
	}
}

func TestDoctorModalLayerBreakdownAndRedactedPreview(t *testing.T) {
	ev := protocol.EffectivePrompt{
		Layers: []protocol.PromptLayerInfo{
			{Kind: protocol.PromptLayerShared, Source: "builtin:shared", Mode: protocol.PromptLayerAppend, Chars: 4000, EstTokens: 1000, Preview: "You are strike"},
			{Kind: protocol.PromptLayerMemory, Source: "memory:keys", Mode: protocol.PromptLayerAppend, Chars: 8000, EstTokens: 2000, Pinned: true, Preview: "token=[REDACTED] keep"},
		},
		SystemChars:    12000,
		MessageCount:   4,
		FromLastStream: true,
		ExcludedKinds:  []string{protocol.PromptLayerLean},
		PinnedKinds:    []string{protocol.PromptLayerMemory},
		Attribution: protocol.RequestTokenAttribution{
			System:      protocol.KnownTokens(3000),
			Tools:       protocol.KnownTokens(1200),
			Messages:    protocol.KnownTokens(800),
			ToolResults: protocol.KnownTokens(400),
			Total:       protocol.KnownTokens(5400),
			Source:      protocol.UsageSourceEstimated,
		},
	}
	m := newDoctorModal(ev, 20_000, true)
	// Scroll through the full body so layers + attribution are both covered.
	var plain strings.Builder
	for i := 0; i < 12; i++ {
		plain.WriteString(ansi.Strip(m.view(72, theme.Default())))
		plain.WriteByte('\n')
		m.scroll++
	}
	got := plain.String()
	for _, want := range []string{
		"Context doctor", "last request", "12000 chars", "4 msgs",
		"shared", "project_memory", "memory:keys", "[REDACTED]",
		"~tok", "warning", "pin", "excluded", "lean_code", "pinned",
		"Request input", "tool_results", "estimated", "not provider-measured",
		"~3k", "~1.2k", "~2k",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor missing %q:\n%s", want, got)
		}
	}
	// Must not invent a bare measured token occupancy line as "0/0".
	if strings.Contains(got, "0/0") {
		t.Fatalf("fabricated 0/0:\n%s", got)
	}
}

func TestDoctorModalOmitsAttributionWhenEmpty(t *testing.T) {
	ev := protocol.EffectivePrompt{
		Layers: []protocol.PromptLayerInfo{
			{Kind: protocol.PromptLayerShared, Source: "builtin:shared", Mode: protocol.PromptLayerAppend, Chars: 100},
		},
		SystemChars:  100,
		MessageCount: 1,
	}
	m := newDoctorModal(ev, 200_000, true)
	plain := ansi.Strip(m.view(60, theme.Default()))
	if strings.Contains(plain, "Request input") {
		t.Fatalf("empty attribution should omit request block:\n%s", plain)
	}
}

func TestDoctorModalNoWarningWhenSmall(t *testing.T) {
	ev := protocol.EffectivePrompt{
		Layers: []protocol.PromptLayerInfo{
			{Kind: protocol.PromptLayerShared, Source: "builtin:shared", Mode: protocol.PromptLayerAppend, Chars: 100},
		},
		SystemChars:  100,
		MessageCount: 1,
	}
	m := newDoctorModal(ev, 200_000, true)
	plain := ansi.Strip(m.view(60, theme.Default()))
	if strings.Contains(strings.ToLower(plain), "warning") {
		t.Fatalf("unexpected warning for small prompt:\n%s", plain)
	}
}

func TestRecordUsageOnModelAndResume(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.UsageReported{
		Input:  protocol.KnownTokens(10),
		Output: protocol.KnownTokens(5),
		Used:   protocol.KnownTokens(15),
		Source: protocol.UsageSourceActual,
	})
	if !m.usageUsed.Known || m.usageUsed.N != 15 {
		t.Fatalf("last used = %+v", m.usageUsed)
	}
	if m.usageSession.Reports != 1 || m.usageSession.Input != 10 {
		t.Fatalf("session = %+v", m.usageSession)
	}
	// clearUsage keeps session totals.
	m.clearUsage()
	if m.usageUsed.Known {
		t.Fatal("last used should clear")
	}
	if m.usageSession.Reports != 1 {
		t.Fatalf("session totals cleared unexpectedly: %+v", m.usageSession)
	}

	// seedFromReplay aggregates.
	m2, _ := newAppTestModel(nil, nil)
	seedFromReplay(&m2, []protocol.Event{
		protocol.UsageReported{Input: protocol.KnownTokens(7), Output: protocol.KnownTokens(3), Used: protocol.KnownTokens(10), Source: protocol.UsageSourceActual},
		protocol.UsageReported{Input: protocol.KnownTokens(1), Output: protocol.KnownTokens(1), Used: protocol.KnownTokens(2), Source: protocol.UsageSourceActual},
	})
	if m2.usageSession.Reports != 2 || m2.usageSession.Input != 8 || m2.usageSession.Output != 4 {
		t.Fatalf("replay totals = %+v", m2.usageSession)
	}
	if !m2.usageUsed.Known || m2.usageUsed.N != 2 {
		t.Fatalf("replay last used = %+v", m2.usageUsed)
	}
}
