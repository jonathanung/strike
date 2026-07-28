package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const legendModalMaxRows = 12

// legendEntry is one row in the UI legend (glyph/token + meaning).
type legendEntry struct {
	Category string
	// Glyph is resolved from theme at build time so the cheatsheet never
	// hardcodes glyphs that drift from Icons.
	Glyph       string
	Description string
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
		{"Transcript", ic.Prompt, "user prompt and composer marker"},
		{"Transcript", ic.Assistant, "assistant message label"},
		{"Transcript", ic.Tool, "tool call label"},
		{"Transcript", ic.ToolGuide, "tool transcript guide line"},
		{"Transcript", ic.Agent, "agent / persona marker"},
		{"Transcript", ic.Bolt, "brand motif"},
		// Outcomes
		{"Status", ic.OK, "success / completed"},
		{"Status", ic.Err, "error / failed"},
		{"Status", ic.Info, "informational"},
		// Agent runtime chrome (words + color roles)
		{"Agent state", theme.AgentStateReady.Label(), "idle, awaiting input (success color)"},
		{"Agent state", theme.AgentStateWorking.Label(), "turn or tool loop in flight (accent)"},
		{"Agent state", theme.AgentStateAttention.Label(), "needs you: permission, gate, or prompt (warning)"},
		{"Agent state", theme.AgentStateError.Label(), "failed turn, tool, or provider (error color)"},
		// Navigation / input
		{"Chrome", ic.Cursor, "list selection cursor"},
		{"Chrome", ic.InputCursor, "text input cursor"},
		{"Chrome", ic.FilterCursor, "active filter cursor"},
		{"Chrome", ic.FocusBar, "focused pane edge marker"},
		{"Chrome", ic.Dot, "inline separator between fields"},
		{"Chrome", ic.DetailSeparator, "label/detail separator"},
		{"Chrome", ic.Ellipsis, "truncated marker"},
		{"Chrome", ic.BadgeLeft + ic.Ellipsis + ic.BadgeRight, "status badge delimiters"},
		// Meters / trees
		{"Chrome", ic.MeterFill + ic.MeterEmpty, "context meter fill / empty"},
		{"Chrome", ic.TreeExpanded, "expanded tree node"},
		{"Chrome", ic.TreeCollapsed, "collapsed tree node"},
		{"Chrome", ic.LogoTopRule + " / " + ic.LogoBottomRule, "logo top / bottom rules"},
	}
	if spark := strings.TrimSpace(ic.Sparkline); spark != "" {
		// Show ends of the sparkline scale rather than the full bar set.
		runes := []rune(spark)
		sample := spark
		if len(runes) >= 2 {
			sample = string(runes[0]) + ic.Ellipsis + string(runes[len(runes)-1])
		}
		entries = append(entries, legendEntry{"Chrome", sample, "activity sparkline (low to high)"})
	}
	return entries
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

func (m *legendModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
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
		if msg.Type == tea.KeyRunes {
			m.filter += string(msg.Runes)
			m.cursor = 0
		}
	}
	return m, nil
}

func (m *legendModal) view(width int, th theme.Theme) string {
	// Rebuild from the active theme so a mid-session theme switch cannot leave
	// stale glyphs in the legend.
	m.entries = buildLegendEntries(th)

	list := m.filtered()
	if m.cursor >= len(list) {
		m.cursor = max(0, len(list)-1)
	}
	items := make([]ui.ListItem, len(list))
	for i, entry := range list {
		items[i] = ui.ListItem{
			Label:  sanitizeDisplayData(entry.Glyph),
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
