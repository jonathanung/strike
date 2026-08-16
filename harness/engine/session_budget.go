package engine

import (
	"fmt"
	"sync"

	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// EstimateUsageCostFunc estimates USD for one provider usage report.
// Returns 0 when pricing is unknown — callers must not invent a dollar figure.
type EstimateUsageCostFunc func(providerName, model string, u provider.Usage) float64

// sessionBudget is the shared outer cost envelope for a root session and its
// children (#577). Zero MaxCostUSD means unlimited. Created once on the root
// engine and inherited via Options.SessionBudget.
type sessionBudget struct {
	mu sync.Mutex

	maxCostUSD float64
	estimate   EstimateUsageCostFunc

	costUSD   float64
	costKnown bool
	// warned tracks which cost thresholds already emitted (50/80/100).
	warned map[string]bool
	// exhausted is sticky once the hard cost ceiling is crossed.
	exhausted bool
}

func newSessionBudget(maxCostUSD float64, estimate EstimateUsageCostFunc) *sessionBudget {
	if maxCostUSD < 0 {
		maxCostUSD = 0
	}
	return &sessionBudget{
		maxCostUSD: maxCostUSD,
		estimate:   estimate,
		warned:     make(map[string]bool),
	}
}

// Exhausted reports whether the session cost envelope has been spent.
func (b *sessionBudget) Exhausted() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exhausted
}

// MaxCostUSD returns the configured ceiling (0 = unlimited).
func (b *sessionBudget) MaxCostUSD() float64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.maxCostUSD
}

// CostUSD returns accumulated estimated cost and whether any priced usage landed.
func (b *sessionBudget) CostUSD() (float64, bool) {
	if b == nil {
		return 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.costUSD, b.costKnown
}

// sessionBudgetNote is the outcome of recording one usage report against the
// shared envelope. Warnings lists newly crossed thresholds (50/80/100).
type sessionBudgetNote struct {
	CostUSD    float64
	MaxCostUSD float64
	DeltaUSD   float64
	CostKnown  bool
	Exhausted  bool
	// Warnings are newly crossed levels this note (ordered 50 → 80 → 100).
	Warnings []string
}

// noteUsage estimates cost for u and accumulates it. Returns zero-value when
// the budget is nil or pricing is unavailable for this report.
func (b *sessionBudget) noteUsage(providerName, model string, u provider.Usage) sessionBudgetNote {
	if b == nil {
		return sessionBudgetNote{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	out := sessionBudgetNote{
		CostUSD:    b.costUSD,
		MaxCostUSD: b.maxCostUSD,
		CostKnown:  b.costKnown,
		Exhausted:  b.exhausted,
	}
	if b.estimate == nil || providerName == "" || model == "" {
		return out
	}
	delta := b.estimate(providerName, model, u)
	if delta <= 0 {
		// Unknown pricing or free — keep prior totals.
		return out
	}
	b.costUSD += delta
	b.costKnown = true
	out.DeltaUSD = delta
	out.CostUSD = b.costUSD
	out.CostKnown = true

	if b.maxCostUSD <= 0 {
		return out
	}
	ratio := b.costUSD / b.maxCostUSD
	for _, level := range []struct {
		key string
		at  float64
	}{
		{protocol.SessionBudgetLevel50, 0.50},
		{protocol.SessionBudgetLevel80, 0.80},
		{protocol.SessionBudgetLevel100, 1.00},
	} {
		if ratio+1e-12 < level.at {
			continue
		}
		if b.warned[level.key] {
			continue
		}
		b.warned[level.key] = true
		out.Warnings = append(out.Warnings, level.key)
	}
	if ratio >= 1.0 {
		b.exhausted = true
		out.Exhausted = true
	}
	out.MaxCostUSD = b.maxCostUSD
	return out
}

// turnTokenTracker accumulates used tokens within one engine turn for the
// optional maxTurnTokens ceiling (#577). Not shared across engines.
type turnTokenTracker struct {
	max  int
	used int
	// warned tracks 50/80/100 for this turn only.
	warned map[string]bool
	// exhausted sticky for the turn once the ceiling is crossed.
	exhausted bool
}

func newTurnTokenTracker(max int) *turnTokenTracker {
	if max < 0 {
		max = 0
	}
	return &turnTokenTracker{
		max:    max,
		warned: make(map[string]bool),
	}
}

func (t *turnTokenTracker) reset() {
	if t == nil {
		return
	}
	t.used = 0
	t.exhausted = false
	t.warned = make(map[string]bool)
}

type turnTokenNote struct {
	Used      int
	Max       int
	Exhausted bool
	Warnings  []string
}

func (t *turnTokenTracker) note(tokens int) turnTokenNote {
	if t == nil || t.max <= 0 || tokens <= 0 {
		if t == nil {
			return turnTokenNote{}
		}
		return turnTokenNote{Used: t.used, Max: t.max, Exhausted: t.exhausted}
	}
	t.used += tokens
	out := turnTokenNote{Used: t.used, Max: t.max, Exhausted: t.exhausted}
	ratio := float64(t.used) / float64(t.max)
	for _, level := range []struct {
		key string
		at  float64
	}{
		{protocol.SessionBudgetLevel50, 0.50},
		{protocol.SessionBudgetLevel80, 0.80},
		{protocol.SessionBudgetLevel100, 1.00},
	} {
		if ratio+1e-12 < level.at {
			continue
		}
		if t.warned[level.key] {
			continue
		}
		t.warned[level.key] = true
		out.Warnings = append(out.Warnings, level.key)
	}
	if ratio >= 1.0 {
		t.exhausted = true
		out.Exhausted = true
	}
	return out
}

func (t *turnTokenTracker) Exhausted() bool {
	if t == nil {
		return false
	}
	return t.exhausted
}

func formatBudgetUSD(usd float64) string {
	if usd < 0 {
		usd = 0
	}
	if usd > 0 && usd < 0.01 {
		return "<$0.01"
	}
	s := fmt.Sprintf("%.4f", usd)
	// trim trailing zeros
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	if s == "" || s == "-" {
		s = "0"
	}
	return "$" + s
}

func sessionBudgetCostMessage(level string, cost, max float64, exhausted bool) string {
	pct := level
	if exhausted || level == protocol.SessionBudgetLevel100 {
		return fmt.Sprintf("session cost budget exhausted (%s / %s USD)", formatBudgetUSD(cost), formatBudgetUSD(max))
	}
	return fmt.Sprintf("session cost budget %s%%: %s / %s USD", pct, formatBudgetUSD(cost), formatBudgetUSD(max))
}

func sessionBudgetTurnMessage(level string, used, max int, exhausted bool) string {
	if exhausted || level == protocol.SessionBudgetLevel100 {
		return fmt.Sprintf("per-turn token budget exhausted (%d / %d tokens)", used, max)
	}
	return fmt.Sprintf("per-turn token budget %s%%: %d / %d tokens", level, used, max)
}
