package engine

import (
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

// TestChildUsageSnapshot covers ChildCompleted.Usage (#1962): absent when
// no stream reported usage, token sums when one did, and a cost only when
// pricing produced one.
func TestChildUsageSnapshot(t *testing.T) {
	priced := func(_, _ string, u provider.Usage) float64 {
		return float64(u.InputTokens+u.OutputTokens) / 1000
	}
	unpriced := func(_, _ string, _ provider.Usage) float64 { return 0 }
	tests := []struct {
		name     string
		estimate EstimateUsageCostFunc
		events   []protocol.Event
		want     *protocol.ChildUsage
	}{
		{
			name:   "no usage reported",
			events: []protocol.Event{protocol.TurnStarted{}, protocol.TurnCompleted{StopReason: "end_turn"}},
			want:   nil,
		},
		{
			name: "all counts unknown",
			events: []protocol.Event{
				protocol.UsageReported{},
			},
			want: nil,
		},
		{
			name: "used totals summed without pricing",
			events: []protocol.Event{
				protocol.UsageReported{Used: protocol.KnownTokens(120)},
				protocol.UsageReported{Used: protocol.KnownTokens(80)},
			},
			want: &protocol.ChildUsage{Tokens: 200},
		},
		{
			name: "measured zero is reported, not absent",
			events: []protocol.Event{
				protocol.UsageReported{Input: protocol.KnownTokens(0), Output: protocol.KnownTokens(0)},
			},
			want: &protocol.ChildUsage{Tokens: 0},
		},
		{
			name:     "parts summed and priced",
			estimate: priced,
			events: []protocol.Event{
				protocol.UsageReported{Input: protocol.KnownTokens(1000), Output: protocol.KnownTokens(500)},
				protocol.UsageReported{Input: protocol.KnownTokens(500)},
			},
			want: &protocol.ChildUsage{Tokens: 2000, CostUSD: ptrFloat(2.0)},
		},
		{
			name:     "pricing without a rate leaves cost absent",
			estimate: unpriced,
			events: []protocol.Event{
				protocol.UsageReported{Input: protocol.KnownTokens(10), Output: protocol.KnownTokens(5)},
			},
			want: &protocol.ChildUsage{Tokens: 15},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &childHandle{
				id:     "c1",
				eng:    &Engine{opts: Options{EstimateUsageCost: tt.estimate}},
				budget: newChildBudget(tool.AgentBudgetLimits{}, "obj", time.Now()),
			}
			for _, ev := range tt.events {
				h.noteEvent(ev)
			}
			got := h.usageSnapshot()
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("usage = %+v, want %+v", got, tt.want)
			}
			if got == nil {
				return
			}
			if got.Tokens != tt.want.Tokens {
				t.Fatalf("tokens = %d, want %d", got.Tokens, tt.want.Tokens)
			}
			if (got.CostUSD == nil) != (tt.want.CostUSD == nil) {
				t.Fatalf("cost = %v, want %v", got.CostUSD, tt.want.CostUSD)
			}
			if got.CostUSD != nil && *got.CostUSD != *tt.want.CostUSD {
				t.Fatalf("cost = %v, want %v", *got.CostUSD, *tt.want.CostUSD)
			}
		})
	}
}

func TestChildUsageSnapshotNilSafe(t *testing.T) {
	var h *childHandle
	if got := h.usageSnapshot(); got != nil {
		t.Fatalf("nil handle usage = %+v, want nil", got)
	}
	if got := (&childHandle{}).usageSnapshot(); got != nil {
		t.Fatalf("no budget usage = %+v, want nil", got)
	}
}

func ptrFloat(v float64) *float64 { return &v }
