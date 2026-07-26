package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// contextStateMsg is a snapshot of model-owned context meter state, broadcast
// to every right-pane window so the context window stays in sync without
// holding a back-reference to Model.
type contextStateMsg struct {
	WorkDir, SessionID, SessionTitle string
	Provider, Model, Agent           string
	AgentState                       string
	Input, Output, Used              protocol.TokenCount
	Source                           string
	ContextLimit                     int
	ContextLimitKnown                bool
	OutputLimit                      int
	OutputLimitKnown                 bool
}

// contextWindow is the default right-pane surface: cwd, session, model/agent,
// usage meter with breakdown, and an empty todos placeholder.
type contextWindow struct {
	state  contextStateMsg
	width  int
	height int
}

func newContextWindow() contextWindow {
	return contextWindow{}
}

func (w contextWindow) id() string { return "context" }

func (w contextWindow) title() string { return "context" }

func (w contextWindow) init() tea.Cmd { return nil }

func (w contextWindow) update(msg tea.Msg) (window, tea.Cmd) {
	if s, ok := msg.(contextStateMsg); ok {
		w.state = s
	}
	return w, nil
}

func (w contextWindow) resize(width, height int) window {
	w.width, w.height = max(0, width), max(0, height)
	return w
}

func (w contextWindow) view(th theme.Theme) string {
	if w.width <= 0 {
		return ""
	}
	th = th.Resolve()
	st := th.S()
	s := w.state
	dash := th.Icons.DetailSeparator

	lines := make([]string, 0, 16)
	lines = append(lines, contextKVLine(th, w.width, "directory", contextOrDash(s.WorkDir, dash)))
	lines = append(lines, contextKVLine(th, w.width, "session", contextSessionValue(s.SessionTitle, s.SessionID, w.width, th.Icons.Ellipsis, dash)))
	lines = append(lines, contextKVLine(th, w.width, "model", contextModelValue(s.Provider, s.Model, dash)))
	agentVal := dash
	if s.Agent != "" && validAgentName(s.Agent) {
		agentVal = sanitizeDisplayData(s.Agent)
		if s.AgentState != "" {
			agentVal += themedSpace(th.Spacing.XS) + st.Muted.Render("("+s.AgentState+")")
		}
	}
	lines = append(lines, contextKVLine(th, w.width, "agent", agentVal))

	pair := formatContextTokenPair(s.Used, s.ContextLimit, s.ContextLimitKnown, dash)
	ratio := contextUsageRatio(s.Used, s.ContextLimit, s.ContextLimitKnown)
	barWidth := min(12, max(4, w.width/3))
	if w.width < 20 {
		barWidth = 0
	}
	contextVal := st.Text.Render(pair)
	if barWidth > 0 {
		contextVal = ui.Meter(th, barWidth, ratio) + themedSpace(th.Spacing.XS) + contextVal
	}
	lines = append(lines, contextKVLine(th, w.width, "context", contextVal))

	if s.Source != "" {
		lines = append(lines, contextKVLine(th, w.width, "source", s.Source))
	}
	if s.Input.Known {
		lines = append(lines, contextKVLine(th, w.width, "input", ui.FormatTokens(s.Input.N)))
	}
	if s.Output.Known {
		lines = append(lines, contextKVLine(th, w.width, "output", ui.FormatTokens(s.Output.N)))
	}
	if s.OutputLimitKnown {
		lines = append(lines, contextKVLine(th, w.width, "out limit", ui.FormatTokens(s.OutputLimit)))
	}

	lines = append(lines, "")
	lines = append(lines, wrapWindowText(st.Muted.Render("todos"), w.width))
	lines = append(lines, wrapWindowText(st.Muted.Render("No active todos"), w.width))

	if w.height > 0 && len(lines) > w.height {
		lines = lines[:w.height]
	}
	return strings.Join(lines, "\n")
}

func contextOrDash(value, dash string) string {
	if strings.TrimSpace(value) == "" {
		return dash
	}
	return value
}

func contextModelValue(provider, model, dash string) string {
	if provider == "" {
		return dash
	}
	if model == "" {
		return provider
	}
	return provider + "/" + model
}

func contextSessionValue(title, id string, width int, ellipsis, dash string) string {
	// Prefer auto-title; fall back to the session id fragment.
	label := strings.TrimSpace(title)
	if label == "" {
		label = id
	}
	if label == "" {
		return dash
	}
	// Leave room for the "session" label and gap on a typical pane.
	budget := max(8, width-10)
	if strings.TrimSpace(title) != "" {
		label = sanitizeDisplayData(label)
		if ansi.StringWidth(label) <= budget {
			return label
		}
		return ansi.Truncate(label, budget, ellipsis)
	}
	return truncateMiddle(label, budget, ellipsis)
}

// formatContextTokenPair renders used/limit with "—" for unknown sides.
// Unknown is never shown as a measured zero.
func formatContextTokenPair(used protocol.TokenCount, limit int, limitKnown bool, dash string) string {
	usedStr := dash
	if used.Known {
		usedStr = ui.FormatTokens(used.N)
	}
	limitStr := dash
	if limitKnown {
		limitStr = ui.FormatTokens(limit)
	}
	return usedStr + "/" + limitStr
}

// contextUsageRatio is used/limit when both are known and limit > 0; otherwise
// -1 so Meter draws a hollow unknown bar.
func contextUsageRatio(used protocol.TokenCount, limit int, limitKnown bool) float64 {
	if !used.Known || !limitKnown || limit <= 0 {
		return -1
	}
	r := float64(used.N) / float64(limit)
	if r > 1 {
		return 1
	}
	if r < 0 {
		return 0
	}
	return r
}

// contextKVLine renders "label  value" with a muted label, truncating to width.
func contextKVLine(th theme.Theme, width int, label, value string) string {
	th = th.Resolve()
	st := th.S()
	gap := themedSpace(th.Spacing.SM)
	labelPart := st.Muted.Render(label)
	// value may already include styled segments (meter); measure and pad.
	line := labelPart + gap + value
	if lipgloss.Width(line) <= width {
		return wrapWindowText(line, width)
	}
	// Drop to value-only when the row is too tight for a label.
	labelW := lipgloss.Width(labelPart) + lipgloss.Width(gap)
	if width <= labelW+2 {
		return wrapWindowText(truncateStyled(th, value, width), width)
	}
	return wrapWindowText(labelPart+gap+truncateStyled(th, value, width-labelW), width)
}

func truncateStyled(th theme.Theme, s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, th.Resolve().Icons.Ellipsis)
}

// truncateMiddle keeps the start and end of s when it exceeds maxWidth,
// inserting ellipsis in the middle. Used for long session ids.
func truncateMiddle(s string, maxWidth int, ellipsis string) string {
	if maxWidth <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= maxWidth {
		return s
	}
	ellW := ansi.StringWidth(ellipsis)
	if ellW >= maxWidth {
		return ansi.Truncate(s, maxWidth, "")
	}
	keep := maxWidth - ellW
	left := keep / 2
	right := keep - left
	if left <= 0 || right <= 0 {
		return ansi.Truncate(s, maxWidth, ellipsis)
	}
	// Walk runes for display-width budgets.
	var start string
	width := 0
	for _, r := range s {
		rw := ansi.StringWidth(string(r))
		if width+rw > left {
			break
		}
		start += string(r)
		width += rw
	}
	var end string
	width = 0
	runes := []rune(s)
	for i := len(runes) - 1; i >= 0; i-- {
		rw := ansi.StringWidth(string(runes[i]))
		if width+rw > right {
			break
		}
		end = string(runes[i]) + end
		width += rw
	}
	return start + ellipsis + end
}

func wrapWindowText(text string, width int) string {
	return lipgloss.NewStyle().Width(max(1, width)).Render(text)
}
