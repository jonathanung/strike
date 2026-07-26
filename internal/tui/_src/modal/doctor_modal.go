package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// Rough chars-per-token for size warnings only. Never shown as measured tokens.
const doctorCharsPerTokenEst = 4

// Doctor pressure thresholds (fraction of context window for ~token estimate).
const (
	doctorWarnRatio  = 0.50
	doctorErrorRatio = 0.80
)

// doctorModal is the context doctor: effective-prompt layer breakdown with
// size warnings. Layer previews are already redacted by the engine.
type doctorModal struct {
	ev                protocol.EffectivePrompt
	contextLimit      int
	contextLimitKnown bool
	scroll            int
}

func newDoctorModal(ev protocol.EffectivePrompt, contextLimit int, contextLimitKnown bool) *doctorModal {
	return &doctorModal{
		ev:                ev,
		contextLimit:      contextLimit,
		contextLimitKnown: contextLimitKnown,
	}
}

func (m *doctorModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
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

func (m *doctorModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	inner := max(1, ui.PanelInnerWidth(th, width))
	if width < 4 {
		inner = max(1, width)
	}
	lines := m.bodyLines(th)
	const maxBody = 18
	if m.scroll > max(0, len(lines)-maxBody) {
		m.scroll = max(0, len(lines)-maxBody)
	}
	visible := lines
	if len(lines) > maxBody {
		end := min(len(lines), m.scroll+maxBody)
		visible = lines[m.scroll:end]
	}
	wrapped := make([]string, 0, len(visible))
	for _, line := range visible {
		wrapped = append(wrapped, lipgloss.NewStyle().Width(inner).Render(line))
	}
	body := strings.Join(wrapped, "\n")
	if width < 4 {
		return body
	}
	hint := dotJoin(th, "esc close")
	if len(lines) > maxBody {
		hint = dotJoin(th, "↑/↓ scroll", "esc close")
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Context doctor",
		Hint:  hint,
		Width: width,
		Tone:  m.dialogTone(),
	}, body)
}

func (m *doctorModal) dialogTone() ui.Tone {
	level := m.pressureLevel()
	switch level {
	case ui.LevelError:
		return ui.ToneError
	case ui.LevelWarning:
		return ui.ToneWarning
	default:
		return ui.ToneDefault
	}
}

func (m *doctorModal) pressureLevel() ui.Level {
	if !m.contextLimitKnown || m.contextLimit <= 0 || m.ev.SystemChars <= 0 {
		return ui.LevelInfo
	}
	est := m.ev.SystemChars / doctorCharsPerTokenEst
	ratio := float64(est) / float64(m.contextLimit)
	switch {
	case ratio >= doctorErrorRatio:
		return ui.LevelError
	case ratio >= doctorWarnRatio:
		return ui.LevelWarning
	default:
		return ui.LevelInfo
	}
}

func (m *doctorModal) bodyLines(th theme.Theme) []string {
	th = th.Resolve()
	st := th.S()
	ev := m.ev
	var lines []string

	scope := "current composition"
	if ev.FromLastStream {
		scope = "last request"
	}
	lines = append(lines, st.Muted.Render("scope")+themedSpace(th.Spacing.SM)+st.Text.Render(scope))
	lines = append(lines, costKV(th, "system", fmt.Sprintf("%d chars", ev.SystemChars)))
	// History is message count only — no fabricated token occupancy.
	lines = append(lines, costKV(th, "history", fmt.Sprintf("%d msgs", ev.MessageCount)))

	if m.contextLimitKnown && m.contextLimit > 0 {
		pair := fmt.Sprintf("%s context window", ui.FormatTokens(m.contextLimit))
		lines = append(lines, costKV(th, "limit", pair))
		if ev.SystemChars > 0 {
			est := ev.SystemChars / doctorCharsPerTokenEst
			// Explicit estimate marker — not measured tokens.
			lines = append(lines, costKV(th, "system ~tok", fmt.Sprintf("~%s (chars/%d est.)", ui.FormatTokens(est), doctorCharsPerTokenEst)))
		}
	}

	for _, w := range m.warnings(th) {
		lines = append(lines, w)
	}

	lines = append(lines, "")
	lines = append(lines, st.Title.Render("Layers"))
	if len(ev.Layers) == 0 {
		lines = append(lines, st.Muted.Render("(no layers)"))
		return lines
	}

	total := ev.SystemChars
	if total <= 0 {
		for _, layer := range ev.Layers {
			total += layer.Chars
		}
	}
	for i, layer := range ev.Layers {
		kind := sanitizeDisplayData(layer.Kind)
		source := sanitizeDisplayData(layer.Source)
		mode := sanitizeDisplayData(layer.Mode)
		head := fmt.Sprintf("%d. %s [%s]", i+1, kind, mode)
		lines = append(lines, st.Text.Render(head))
		parts := []string{source, fmt.Sprintf("%d chars", layer.Chars)}
		if total > 0 && layer.Chars > 0 {
			parts = append(parts, fmt.Sprintf("%d%%", (layer.Chars*100)/total))
		}
		detail := "   " + dotJoin(th, parts...)
		lines = append(lines, st.Muted.Render(detail))
		if preview := strings.TrimSpace(layer.Preview); preview != "" {
			// Previews are engine-redacted; still strip controls for display.
			prev := sanitizeDisplayData(preview)
			if len([]rune(prev)) > 80 {
				prev = string([]rune(prev)[:80]) + th.Icons.Ellipsis
			}
			lines = append(lines, st.Muted.Render("   "+prev))
		}
	}
	return lines
}

func (m *doctorModal) warnings(th theme.Theme) []string {
	th = th.Resolve()
	st := th.S()
	var out []string
	ev := m.ev

	if m.contextLimitKnown && m.contextLimit > 0 && ev.SystemChars > 0 {
		est := ev.SystemChars / doctorCharsPerTokenEst
		ratio := float64(est) / float64(m.contextLimit)
		switch {
		case ratio >= doctorErrorRatio:
			out = append(out, "")
			out = append(out, st.Error.Render(fmt.Sprintf(
				"warning: system prompt ~%s tok est. is ≥%d%% of the %s context window",
				ui.FormatTokens(est), int(doctorErrorRatio*100), ui.FormatTokens(m.contextLimit),
			)))
		case ratio >= doctorWarnRatio:
			out = append(out, "")
			out = append(out, st.Warning.Render(fmt.Sprintf(
				"warning: system prompt ~%s tok est. is ≥%d%% of the %s context window",
				ui.FormatTokens(est), int(doctorWarnRatio*100), ui.FormatTokens(m.contextLimit),
			)))
		}
	}

	// Flag a dominant layer without inventing tokens.
	total := ev.SystemChars
	if total <= 0 {
		for _, layer := range ev.Layers {
			total += layer.Chars
		}
	}
	if total > 0 {
		for _, layer := range ev.Layers {
			if layer.Chars*100/total >= 60 && layer.Chars >= 2000 {
				out = append(out, "")
				out = append(out, st.Warning.Render(fmt.Sprintf(
					"warning: layer %q is %d%% of system prompt (%d chars)",
					sanitizeDisplayData(layer.Kind), layer.Chars*100/total, layer.Chars,
				)))
			}
		}
	}
	return out
}
