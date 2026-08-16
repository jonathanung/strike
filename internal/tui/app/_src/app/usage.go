package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// maxCostTurnRows caps per-turn lines in the /cost modal.
const maxCostTurnRows = 8

// usageTurn is one UsageReported snapshot retained for optional per-turn display.
type usageTurn struct {
	Input, Output, CacheRead, CacheCreation, Used protocol.TokenCount
	Source                                        string
}

// usageTotals accumulates session-scoped token counts from UsageReported events.
// Known=false on a total means no report ever supplied that part — never treat
// an absent total as a measured zero.
type usageTotals struct {
	Reports                                       int
	Input, Output, CacheRead, CacheCreation       int
	InputOK, OutputOK, CacheReadOK, CacheCreateOK bool
	AnyEstimated, AnyActual                       bool
	Turns                                         []usageTurn
}

func (t *usageTotals) add(ev protocol.UsageReported) {
	if t == nil {
		return
	}
	t.Reports++
	addPart := func(ok *bool, sum *int, part protocol.TokenCount) {
		if !part.Known {
			return
		}
		if !*ok {
			*ok = true
			*sum = part.N
			return
		}
		*sum += part.N
	}
	addPart(&t.InputOK, &t.Input, ev.Input)
	addPart(&t.OutputOK, &t.Output, ev.Output)
	addPart(&t.CacheReadOK, &t.CacheRead, ev.CacheRead)
	addPart(&t.CacheCreateOK, &t.CacheCreation, ev.CacheCreation)
	switch ev.Source {
	case protocol.UsageSourceEstimated:
		t.AnyEstimated = true
	case protocol.UsageSourceActual:
		t.AnyActual = true
	}
	turn := usageTurn{
		Input:         ev.Input,
		Output:        ev.Output,
		CacheRead:     ev.CacheRead,
		CacheCreation: ev.CacheCreation,
		Used:          ev.Used,
		Source:        ev.Source,
	}
	t.Turns = append(t.Turns, turn)
	if len(t.Turns) > maxCostTurnRows {
		t.Turns = t.Turns[len(t.Turns)-maxCostTurnRows:]
	}
}

func (t usageTotals) empty() bool {
	return t.Reports == 0
}

// formatTokenOrUnknown renders a known count or an explicit unknown label.
func formatTokenOrUnknown(n int, ok bool, unknown string) string {
	if !ok {
		return unknown
	}
	return ui.FormatTokens(n)
}

// formatTokenCount renders a protocol.TokenCount or unknown.
func formatTokenCount(tc protocol.TokenCount, unknown string) string {
	return formatTokenOrUnknown(tc.N, tc.Known, unknown)
}

// estimateUSD returns session cost in USD when catalog rates and known token
// parts allow. ok is false when pricing or both token sides are unknown —
// never invent a dollar figure from missing data. partial is true when only
// one of input/output contributed.
func estimateUSD(t usageTotals, inputPerM, outputPerM float64, hasCost bool) (usd float64, ok bool, partial bool) {
	if !hasCost {
		return 0, false, false
	}
	if t.InputOK {
		usd += float64(t.Input) * inputPerM / 1_000_000
		ok = true
	}
	if t.OutputOK {
		usd += float64(t.Output) * outputPerM / 1_000_000
		ok = true
	}
	if ok && (!t.InputOK || !t.OutputOK) {
		partial = true
	}
	return usd, ok, partial
}

// formatSessionCostUSD renders a dollar amount without false precision.
func formatSessionCostUSD(usd float64) string {
	if usd < 0 {
		usd = 0
	}
	// Sub-cent sessions stay visible without inventing micro-precision.
	if usd > 0 && usd < 0.01 {
		return "<$0.01"
	}
	s := fmt.Sprintf("%.4f", usd)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		s = "0"
	}
	return "$" + s
}

// usageSourceLabel summarizes actual/estimated mix for the session.
func usageSourceLabel(t usageTotals) string {
	switch {
	case t.AnyActual && t.AnyEstimated:
		return "mixed (actual + estimated)"
	case t.AnyEstimated:
		return protocol.UsageSourceEstimated
	case t.AnyActual:
		return protocol.UsageSourceActual
	default:
		return "unknown"
	}
}

// applySessionBudgetWarning records envelope progress and surfaces a notice /
// transcript line so the hard stop is never silent (#577).
func (m *Model) applySessionBudgetWarning(ev protocol.SessionBudgetWarning) tea.Cmd {
	if ev.Kind == protocol.SessionBudgetKindCostUSD || ev.Kind == "" {
		if ev.MaxCostUSD > 0 {
			m.sessionBudgetMaxUSD = ev.MaxCostUSD
			m.sessionBudgetCostUSD = ev.CostUSD
			m.sessionBudgetKnown = true
		}
	}
	if ev.Level != "" {
		m.sessionBudgetLevel = ev.Level
	}
	if ev.Exhausted || ev.Level == protocol.SessionBudgetLevel100 {
		m.sessionBudgetExhausted = true
	}
	msg := ev.Message
	if msg == "" {
		switch ev.Kind {
		case protocol.SessionBudgetKindTurnTokens:
			msg = fmt.Sprintf("turn token budget %s%%: %d / %d", ev.Level, ev.TokensUsed, ev.MaxTokens)
		default:
			msg = fmt.Sprintf("session cost budget %s%%", ev.Level)
		}
	}
	critical := ev.Exhausted || ev.Level == protocol.SessionBudgetLevel100 || ev.Level == protocol.SessionBudgetLevel80
	if m.turnRunning {
		m.cells = append(m.cells, &errorCell{text: msg})
	} else {
		m.setNotice(msg, critical)
	}
	return nil
}
