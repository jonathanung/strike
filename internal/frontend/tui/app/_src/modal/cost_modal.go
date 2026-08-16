package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// costModal shows session token totals and optional per-turn rows from
// accumulated UsageReported events. Unknown parts stay explicit.
type costModal struct {
	totals            usageTotals
	last              usageTurn
	hasLast           bool
	provider, model   string
	inputCost         float64
	outputCost        float64
	hasCost           bool
	contextLimit      int
	contextLimitKnown bool
	lastUsed          protocol.TokenCount
	scroll            int
}

func newCostModal(
	totals usageTotals,
	lastInput, lastOutput, lastCacheRead, lastCacheCreation, lastUsed protocol.TokenCount,
	lastSource string,
	provider, model string,
	inputCost, outputCost float64, hasCost bool,
	contextLimit int, contextLimitKnown bool,
) *costModal {
	m := &costModal{
		totals:            totals,
		provider:          provider,
		model:             model,
		inputCost:         inputCost,
		outputCost:        outputCost,
		hasCost:           hasCost,
		contextLimit:      contextLimit,
		contextLimitKnown: contextLimitKnown,
		lastUsed:          lastUsed,
	}
	if lastInput.Known || lastOutput.Known || lastUsed.Known || lastCacheRead.Known || lastCacheCreation.Known || lastSource != "" {
		m.hasLast = true
		m.last = usageTurn{
			Input: lastInput, Output: lastOutput,
			CacheRead: lastCacheRead, CacheCreation: lastCacheCreation,
			Used: lastUsed, Source: lastSource,
		}
	}
	return m
}

func (m *costModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "q" || msg.String() == "enter" {
		return nil, nil
	}
	switch msg.String() {
	case "up", "ctrl+p", "k":
		if m.scroll > 0 {
			m.scroll--
		}
	case "down", "ctrl+n", "j":
		m.scroll++
	}
	return m, nil
}

func (m *costModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	dash := th.Icons.DetailSeparator
	inner := max(1, ui.PanelInnerWidth(th, width))
	if width < 4 {
		inner = max(1, width)
	}

	lines := m.bodyLines(th, dash)
	const maxBody = 16
	if m.scroll > max(0, len(lines)-maxBody) {
		m.scroll = max(0, len(lines)-maxBody)
	}
	visible := lines
	if len(lines) > maxBody {
		end := min(len(lines), m.scroll+maxBody)
		visible = lines[m.scroll:end]
	}
	body := strings.Join(visible, "\n")
	// Soft-wrap long plain lines to the dialog inner width.
	wrapped := make([]string, 0, len(visible))
	for _, line := range strings.Split(body, "\n") {
		wrapped = append(wrapped, lipgloss.NewStyle().Width(inner).Render(line))
	}
	body = strings.Join(wrapped, "\n")

	if width < 4 {
		return body
	}
	hint := dotJoin(th, "esc close")
	if len(lines) > maxBody {
		hint = dotJoin(th, "↑/↓ scroll", "esc close")
	}
	_ = st
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Session cost",
		Hint:  hint,
		Width: width,
	}, body)
}

func (m *costModal) bodyLines(th theme.Theme, dash string) []string {
	th = th.Resolve()
	st := th.S()
	var lines []string

	modelLine := dash
	if m.provider != "" {
		modelLine = sanitizeDisplayData(m.provider)
		if m.model != "" {
			modelLine += "/" + sanitizeDisplayData(m.model)
		}
	}
	lines = append(lines, st.Muted.Render("model")+themedSpace(th.Spacing.SM)+st.Text.Render(modelLine))

	if m.totals.empty() {
		lines = append(lines, "")
		lines = append(lines, st.Muted.Render("No usage reported yet."))
		lines = append(lines, st.Muted.Render("Totals stay unknown until a provider emits token counts."))
		return lines
	}

	lines = append(lines, "")
	lines = append(lines, st.Title.Render("Session totals"))
	lines = append(lines, costKV(th, "reports", fmt.Sprintf("%d", m.totals.Reports)))
	lines = append(lines, costKV(th, "input", formatTokenOrUnknown(m.totals.Input, m.totals.InputOK, dash)))
	lines = append(lines, costKV(th, "output", formatTokenOrUnknown(m.totals.Output, m.totals.OutputOK, dash)))
	lines = append(lines, costKV(th, "cache read", formatTokenOrUnknown(m.totals.CacheRead, m.totals.CacheReadOK, dash)))
	lines = append(lines, costKV(th, "cache write", formatTokenOrUnknown(m.totals.CacheCreation, m.totals.CacheCreateOK, dash)))
	lines = append(lines, costKV(th, "source", usageSourceLabel(m.totals)))

	usd, usdOK, partial := estimateUSD(m.totals, m.inputCost, m.outputCost, m.hasCost)
	switch {
	case !m.hasCost:
		lines = append(lines, costKV(th, "est. cost", dash+" (no catalog rate)"))
	case !usdOK:
		lines = append(lines, costKV(th, "est. cost", dash+" (token parts unknown)"))
	default:
		label := formatSessionCostUSD(usd)
		if partial {
			label += " (partial)"
		}
		if m.totals.AnyEstimated {
			label = dotJoin(th, label, "estimated tokens")
		}
		lines = append(lines, costKV(th, "est. cost", label))
		lines = append(lines, st.Muted.Render("Rates: "+formatModelCost(m.inputCost, m.outputCost)+" per 1M in/out"))
	}

	if m.hasLast {
		lines = append(lines, "")
		lines = append(lines, st.Title.Render("Last request"))
		lines = append(lines, costKV(th, "input", formatTokenCount(m.last.Input, dash)))
		lines = append(lines, costKV(th, "output", formatTokenCount(m.last.Output, dash)))
		if m.last.CacheRead.Known || m.last.CacheCreation.Known {
			lines = append(lines, costKV(th, "cache read", formatTokenCount(m.last.CacheRead, dash)))
			lines = append(lines, costKV(th, "cache write", formatTokenCount(m.last.CacheCreation, dash)))
		}
		usedPair := formatContextTokenPair(m.lastUsed, m.contextLimit, m.contextLimitKnown, dash)
		lines = append(lines, costKV(th, "context", usedPair))
		if m.last.Source != "" {
			lines = append(lines, costKV(th, "source", m.last.Source))
		}
	}

	if len(m.totals.Turns) > 1 {
		lines = append(lines, "")
		lines = append(lines, st.Title.Render("Recent reports"))
		for i, turn := range m.totals.Turns {
			n := m.totals.Reports - len(m.totals.Turns) + i + 1
			in := formatTokenCount(turn.Input, dash)
			out := formatTokenCount(turn.Output, dash)
			src := turn.Source
			if src == "" {
				src = dash
			}
			row := fmt.Sprintf("%d. ", n) + dotJoin(th, "in "+in, "out "+out, src)
			lines = append(lines, st.Muted.Render(row))
		}
	}
	return lines
}

func costKV(th theme.Theme, label, value string) string {
	th = th.Resolve()
	st := th.S()
	return st.Muted.Render(label) + themedSpace(th.Spacing.SM) + st.Text.Render(value)
}
