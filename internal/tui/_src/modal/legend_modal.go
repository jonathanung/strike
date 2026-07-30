package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const legendModalMaxRows = 12

// legendPaint selects the theme style used for a legend glyph sample.
type legendPaint int

const (
	legendPaintDefault legendPaint = iota
	legendPaintUser
	legendPaintAssistant
	legendPaintTool
	legendPaintSuccess
	legendPaintError
	legendPaintWarning
	legendPaintAccent
	legendPaintAccentAlt
	legendPaintSelected
	legendPaintBorderFocus
	legendPaintInputCursor
	legendPaintMuted
	legendPaintAgentReady
	legendPaintAgentWorking
	legendPaintAgentAttention
	legendPaintAgentError
)

// legendEntry is one row in the UI legend (glyph/token + meaning).
type legendEntry struct {
	Category string
	// Glyph is resolved from theme at build time so the cheatsheet never
	// hardcodes glyphs that drift from Icons.
	Glyph       string
	Description string
	// Paint colors the glyph sample to match live chrome (status, roles, …).
	Paint legendPaint
}

// legendModal is a filterable cheatsheet of TUI icons, status labels, and chrome.
type legendModal struct {
	entries []legendEntry
	filter  string
	cursor  int
}

func newLegendModal(th theme.Theme) *legendModal {
	return &legendModal{entries: buildLegendEntries(th)}
}

func buildLegendEntries(th theme.Theme) []legendEntry {
	th = th.Resolve()
	ic := th.Icons
	entries := []legendEntry{
		// Transcript / roles
		{"Transcript", ic.Prompt, "user prompt and composer marker", legendPaintUser},
		{"Transcript", ic.Assistant, "assistant message label", legendPaintAssistant},
		{"Transcript", ic.Tool, "tool call label", legendPaintTool},
		{"Transcript", ic.ToolGuide, "tool transcript guide line", legendPaintTool},
		{"Transcript", ic.Agent, "agent / persona marker", legendPaintAccent},
		{"Transcript", ic.Bolt, "brand motif", legendPaintWarning},
		// Outcomes
		{"Status", ic.OK, "success / completed", legendPaintSuccess},
		{"Status", ic.Err, "error / failed", legendPaintError},
		{"Status", ic.Info, "informational", legendPaintWarning},
		// Agent runtime chrome (words + color roles)
		{"Agent state", theme.AgentStateReady.Label(), "idle, awaiting input (success color)", legendPaintAgentReady},
		{"Agent state", theme.AgentStateWorking.Label(), "turn or tool loop in flight (accent)", legendPaintAgentWorking},
		{"Agent state", theme.AgentStateAttention.Label(), "permission, gate, or prompt (yellow)", legendPaintAgentAttention},
		{"Agent state", theme.AgentStateError.Label(), "failed turn, tool, or provider (error color)", legendPaintAgentError},
		// Navigation / input
		{"Chrome", ic.Cursor, "list selection cursor", legendPaintSelected},
		{"Chrome", ic.InputCursor, "text input cursor", legendPaintInputCursor},
		{"Chrome", ic.FilterCursor, "active filter cursor", legendPaintInputCursor},
		{"Chrome", ic.FocusBar, "focused pane edge marker", legendPaintBorderFocus},
		{"Chrome", ic.Dot, "inline separator between fields", legendPaintMuted},
		{"Chrome", ic.DetailSeparator, "label/detail separator", legendPaintMuted},
		{"Chrome", ic.Ellipsis, "truncated marker", legendPaintMuted},
		{"Chrome", ic.BadgeLeft + ic.Ellipsis + ic.BadgeRight, "status badge delimiters", legendPaintMuted},
		// Meters / trees
		{"Chrome", ic.MeterFill + ic.MeterEmpty, "context meter fill / empty", legendPaintAccent},
		{"Chrome", ic.TreeExpanded, "expanded tree node", legendPaintDefault},
		{"Chrome", ic.TreeCollapsed, "collapsed tree node", legendPaintDefault},
		{"Chrome", ic.LogoTopRule + " / " + ic.LogoBottomRule, "logo top / bottom rules", legendPaintMuted},
	}
	if spark := strings.TrimSpace(ic.Sparkline); spark != "" {
		// Show ends of the sparkline scale rather than the full bar set.
		runes := []rune(spark)
		sample := spark
		if len(runes) >= 2 {
			sample = string(runes[0]) + ic.Ellipsis + string(runes[len(runes)-1])
		}
		entries = append(entries, legendEntry{"Chrome", sample, "activity sparkline (low to high)", legendPaintAccentAlt})
	}
	return entries
}

func (p legendPaint) style(th theme.Theme) lipgloss.Style {
	th = th.Resolve()
	st := th.S()
	switch p {
	case legendPaintUser:
		return st.UserLabel
	case legendPaintAssistant:
		return st.AssistantLabel
	case legendPaintTool:
		return st.ToolLabel
	case legendPaintSuccess:
		return st.Success
	case legendPaintError:
		return st.Error
	case legendPaintWarning:
		return st.Warning
	case legendPaintAccent:
		return st.Accent
	case legendPaintAccentAlt:
		return st.AccentAlt
	case legendPaintSelected:
		return st.Selected
	case legendPaintBorderFocus:
		return st.BorderFocus
	case legendPaintInputCursor:
		return st.InputCursor
	case legendPaintMuted:
		return st.Muted
	case legendPaintAgentReady:
		return th.AgentStateStyle(theme.AgentStateReady)
	case legendPaintAgentWorking:
		return th.AgentStateStyle(theme.AgentStateWorking)
	case legendPaintAgentAttention:
		return th.AgentStateStyle(theme.AgentStateAttention)
	case legendPaintAgentError:
		return th.AgentStateStyle(theme.AgentStateError)
	default:
		return st.Text
	}
}

func (m *legendModal) filtered() []legendEntry {
	query := strings.ToLower(strings.TrimSpace(m.filter))
	if query == "" {
		return m.entries
	}
	buckets := [3][]legendEntry{}
	for _, entry := range m.entries {
		rank := legendMatchRank(entry, query)
		if rank >= 0 {
			buckets[rank] = append(buckets[rank], entry)
		}
	}
	matches := make([]legendEntry, 0, len(m.entries))
	for _, bucket := range buckets {
		matches = append(matches, bucket...)
	}
	return matches
}

func legendMatchRank(entry legendEntry, query string) int {
	fields := []string{entry.Glyph, entry.Description, entry.Category}
	best := -1
	for _, field := range fields {
		field = strings.ToLower(field)
		rank := -1
		switch {
		case field == query:
			rank = 0
		case strings.HasPrefix(field, query):
			rank = 1
		case strings.Contains(field, query):
			rank = 1
		case orderedSubsequence(field, query):
			rank = 2
		}
		if rank >= 0 && (best < 0 || rank < best) {
			best = rank
		}
	}
	return best
}

func (m *legendModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	list := m.filtered()
	if isEscape(msg) || msg.String() == "q" {
		return nil, nil
	}
	switch msg.String() {
	case "up", "ctrl+p":
		if len(list) > 0 {
			m.cursor = (m.cursor + len(list) - 1) % len(list)
		}
	case "down", "ctrl+n":
		if len(list) > 0 {
			m.cursor = (m.cursor + 1) % len(list)
		}
	case "backspace":
		runes := []rune(m.filter)
		if len(runes) > 0 {
			m.filter = string(runes[:len(runes)-1])
			m.cursor = 0
		}
	case "enter":
		return nil, nil
	default:
		if len(msg.Text) > 0 {
			m.filter += msg.Text
			m.cursor = 0
		}
	}
	return m, nil
}

func (m *legendModal) view(width int, th theme.Theme) string {
	// Rebuild from the active theme so a mid-session theme switch cannot leave
	// stale glyphs or colors in the legend.
	m.entries = buildLegendEntries(th)

	list := m.filtered()
	if m.cursor >= len(list) {
		m.cursor = max(0, len(list)-1)
	}
	items := make([]ui.ListItem, len(list))
	for i, entry := range list {
		glyph := sanitizeDisplayData(entry.Glyph)
		items[i] = ui.ListItem{
			// Prefix keeps semantic color; List would recolor Label on select.
			Prefix: entry.Paint.style(th).Render(glyph),
			Detail: sanitizeDisplayData(detailJoin(th, entry.Category, entry.Description)),
		}
	}

	listWidth := max(1, ui.PanelInnerWidth(th, width))
	if width < 4 {
		listWidth = max(1, width)
	}
	body := ui.List(th, ui.ListOpts{
		Items:      items,
		Cursor:     m.cursor,
		Width:      listWidth,
		Visible:    legendModalMaxRows,
		ShowFilter: true,
		Filter:     sanitizeDisplayData(m.filter),
		Total:      len(m.entries),
		Empty:      "no matching legend entries",
	})
	if width < 4 {
		return body
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Legend",
		Hint:  dotJoin(th, "type to filter", "up/down move", "esc close"),
		Width: width,
	}, body)
}
