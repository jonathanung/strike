package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

// kicker paints an uppercase web-style section label using a resolved style.
func kicker(style lipgloss.Style, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return style.Render(strings.ToUpper(text))
}

// headerKicker is a header/runtime chip: uppercase, no surface pill.
func headerKicker(th theme.Theme, tone ui.Tone, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return toneStyle(th, tone).Render(strings.ToUpper(text))
}

func toneStyle(th theme.Theme, tone ui.Tone) lipgloss.Style {
	st := th.Resolve().S()
	switch tone {
	case ui.ToneAccent:
		return st.Accent
	case ui.ToneAccentAlt:
		return st.AccentAlt
	case ui.ToneSuccess:
		return st.Success
	case ui.ToneWarning:
		return st.Warning
	case ui.ToneError:
		return st.Error
	case ui.ToneDanger:
		return st.Danger
	case ui.ToneMuted:
		return st.Muted
	default:
		return st.Text
	}
}

// messagePrefix is the web .message left-accent rule plus the XS gap.
func messagePrefix(th theme.Theme, style lipgloss.Style) string {
	th = th.Resolve()
	return style.Render(th.Icons.FocusBar) + themedSpace(th.Spacing.XS)
}

// messageHead is the accent rule plus an uppercase role kicker.
func messageHead(th theme.Theme, style lipgloss.Style, label string) string {
	return messagePrefix(th, style) + style.Render(strings.ToUpper(strings.TrimSpace(label)))
}

// inspectorInnerWidth is the body width inside a left-rule inspector pane.
func inspectorInnerWidth(th theme.Theme, width int) int {
	th = th.Resolve()
	if width < 3 {
		return max(0, width)
	}
	return max(1, width-1-th.Spacing.XS)
}

// inspectorInnerHeight is the body height under a title kicker and optional footer.
func inspectorInnerHeight(height int, footer bool) int {
	if height <= 0 {
		return 0
	}
	chrome := 1
	if footer {
		chrome++
	}
	if height <= chrome {
		return 0
	}
	return height - chrome
}

// inspectorFrame is the web inspector chrome: a 1px left rule, uppercase
// kicker, and optional muted footer. No boxed ┌┐ tile.
func inspectorFrame(th theme.Theme, title, footer, body string, width, height int, focused, dim bool) string {
	th = th.Resolve()
	if width <= 0 || height <= 0 {
		return ""
	}
	st := th.S()
	ruleStyle := st.Border
	titleStyle := st.Muted
	switch {
	case focused:
		ruleStyle = st.BorderFocus
		titleStyle = st.Accent
	case dim:
		ruleStyle = st.BorderMuted
	}
	rule := ruleStyle.Render(th.Icons.ToolGuide)
	gap := themedSpace(th.Spacing.XS)
	inner := inspectorInnerWidth(th, width)
	hasFooter := strings.TrimSpace(footer) != ""
	bodyH := inspectorInnerHeight(height, hasFooter)

	rows := make([]string, 0, height)
	rows = append(rows, inspectorRow(th, width, rule, gap, kicker(titleStyle, title), inner, dim))
	bodyRows := strings.Split(body, "\n")
	if body == "" {
		bodyRows = nil
	}
	if bodyH > 0 {
		if len(bodyRows) > bodyH {
			bodyRows = bodyRows[:bodyH]
		}
		for _, line := range bodyRows {
			rows = append(rows, inspectorRow(th, width, rule, gap, line, inner, dim))
		}
		for len(rows) < 1+bodyH {
			rows = append(rows, inspectorRow(th, width, rule, gap, "", inner, dim))
		}
	}
	if hasFooter && len(rows) < height {
		rows = append(rows, inspectorRow(th, width, rule, gap, footer, inner, dim))
	}
	for len(rows) < height {
		rows = append(rows, inspectorRow(th, width, rule, gap, "", inner, dim))
	}
	if len(rows) > height {
		rows = rows[:height]
	}
	return strings.Join(rows, "\n")
}

func inspectorRow(th theme.Theme, width int, rule, gap, content string, inner int, dim bool) string {
	th = th.Resolve()
	if width < 3 {
		return padInspectorLine(th, content, width)
	}
	line := rule + gap + truncateStyled(th, content, inner)
	line = padInspectorLine(th, line, width)
	if dim {
		return th.S().SurfaceMuted.Width(width).MaxWidth(width).Render(line)
	}
	return line
}

func padInspectorLine(th theme.Theme, line string, width int) string {
	if width <= 0 {
		return ""
	}
	w := ansi.StringWidth(ansi.Strip(line))
	if w > width {
		return ansi.Truncate(ansi.Strip(line), width, th.Resolve().Icons.Ellipsis)
	}
	if pad := width - w; pad > 0 {
		return line + themedSpace(pad)
	}
	return line
}

// emptyStateBlock is the web empty-state voice: acid kicker, large title, muted line.
func emptyStateBlock(th theme.Theme, width int, kickerText, title, muted string) string {
	th = th.Resolve()
	if width <= 0 {
		return ""
	}
	st := th.S()
	lines := make([]string, 0, 3)
	if kickerText != "" {
		lines = append(lines, padInspectorLine(th, kicker(st.Accent, kickerText), width))
	}
	if title != "" {
		lines = append(lines, padInspectorLine(th, st.Title.Render(title), width))
	}
	if muted != "" {
		lines = append(lines, padInspectorLine(th, st.Muted.Render(welcomeTruncate(muted, width, th.Icons.Ellipsis)), width))
	}
	return strings.Join(lines, "\n")
}

func emptyStateHeight(kickerText, title, muted string) int {
	n := 0
	if kickerText != "" {
		n++
	}
	if title != "" {
		n++
	}
	if muted != "" {
		n++
	}
	return n
}
