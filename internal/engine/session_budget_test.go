package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func fixedCost(inputPerM, outputPerM float64) EstimateUsageCostFunc {
	return func(_, _ string, u provider.Usage) float64 {
		in := float64(u.InputTokens) / 1e6 * inputPerM
		out := float64(u.OutputTokens) / 1e6 * outputPerM
		cache := float64(u.CacheReadTokens+u.CacheCreationTokens) / 1e6 * inputPerM
		return in + out + cache
	}
}

func TestSessionBudgetThresholdsAndExhaustion(t *testing.T) {
	// $1 input / MTok → 500_000 input tokens = $0.50
	b := newSessionBudget(1.0, fixedCost(1.0, 1.0))

	n1 := b.noteUsage("echo", "echo", provider.Usage{InputTokens: 500_000})
	if n1.CostUSD < 0.49 || n1.CostUSD > 0.51 {
		t.Fatalf("cost after 50%% = %v", n1.CostUSD)
	}
	if len(n1.Warnings) != 1 || n1.Warnings[0] != protocol.SessionBudgetLevel50 {
		t.Fatalf("warnings@50 = %#v", n1.Warnings)
	}
	if n1.Exhausted {
		t.Fatal("should not be exhausted at 50%")
	}

	// Cross 80% and 100% in one jump.
	n2 := b.noteUsage("echo", "echo", provider.Usage{InputTokens: 600_000})
	if !n2.Exhausted {
		t.Fatal("want exhausted")
	}
	want := map[string]bool{
		protocol.SessionBudgetLevel80:  true,
		protocol.SessionBudgetLevel100: true,
	}
	got := map[string]bool{}
	for _, w := range n2.Warnings {
		got[w] = true
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("missing warning %s in %#v", k, n2.Warnings)
		}
	}
	for _, w := range n2.Warnings {
		if w == protocol.SessionBudgetLevel50 {
			t.Fatal("50 should not re-fire")
		}
	}
	if !b.Exhausted() {
		t.Fatal("Exhausted() sticky")
	}
}

func TestSessionBudgetNoPricingNoOp(t *testing.T) {
	b := newSessionBudget(1.0, nil)
	n := b.noteUsage("echo", "echo", provider.Usage{InputTokens: 1_000_000})
	if n.CostKnown || n.Exhausted || len(n.Warnings) != 0 {
		t.Fatalf("unexpected note %#v", n)
	}
}

func TestSessionBudgetUnlimitedTracksCost(t *testing.T) {
	b := newSessionBudget(0, fixedCost(1.0, 0))
	n := b.noteUsage("p", "m", provider.Usage{InputTokens: 1_000_000})
	if n.CostUSD != 1.0 || !n.CostKnown {
		t.Fatalf("note %#v", n)
	}
	if n.Exhausted || len(n.Warnings) != 0 {
		t.Fatalf("unlimited should not warn/exhaust: %#v", n)
	}
}

func TestTurnTokenTrackerThresholds(t *testing.T) {
	tr := newTurnTokenTracker(100)
	n := tr.note(50)
	if len(n.Warnings) != 1 || n.Warnings[0] != protocol.SessionBudgetLevel50 {
		t.Fatalf("50 warnings %#v", n.Warnings)
	}
	n = tr.note(30) // 80
	if len(n.Warnings) != 1 || n.Warnings[0] != protocol.SessionBudgetLevel80 {
		t.Fatalf("80 warnings %#v", n.Warnings)
	}
	n = tr.note(20) // 100
	if !n.Exhausted || !tr.Exhausted() {
		t.Fatal("want exhausted")
	}
	if len(n.Warnings) != 1 || n.Warnings[0] != protocol.SessionBudgetLevel100 {
		t.Fatalf("100 warnings %#v", n.Warnings)
	}
	tr.reset()
	if tr.Exhausted() || tr.used != 0 {
		t.Fatal("reset failed")
	}
}

func TestChildBudgetNoteCost(t *testing.T) {
	now := time.Now()
	b := newChildBudget(tool.AgentBudgetLimits{MaxCostUSD: 0.01}, "obj", now)
	b.noteCost(0.005, now)
	if b.costUSD != 0.005 {
		t.Fatalf("cost=%v", b.costUSD)
	}
	trip, kind, _, _ := b.evaluate(now, now)
	if trip {
		t.Fatalf("should not trip yet kind=%q", kind)
	}
	b.noteCost(0.006, now)
	trip, kind, reason, _ := b.evaluate(now, now)
	if !trip || kind != "cost_usd" {
		t.Fatalf("trip=%v kind=%q reason=%q", trip, kind, reason)
	}
	if !strings.Contains(reason, "cost budget") {
		t.Fatalf("reason=%q", reason)
	}
}

func TestFormatBudgetUSD(t *testing.T) {
	if got := formatBudgetUSD(0); got != "$0" {
		t.Fatalf("0 → %q", got)
	}
	if got := formatBudgetUSD(0.001); got != "<$0.01" {
		t.Fatalf("subcent → %q", got)
	}
	if got := formatBudgetUSD(1.5); got != "$1.5" {
		t.Fatalf("1.5 → %q", got)
	}
}
