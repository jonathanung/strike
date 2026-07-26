package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// contextPaneBody renders session setup for the right-pane context window:
// provider/model, agent, effort, fast, auth detail, and skills count. Rows are
// dropped lowest-priority first when height is tight.
func (m Model) contextPaneBody(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	th := m.th.Resolve()
	st := th.S()
	space := themedSpace(th.Spacing.XS)
	ellipsis := th.Icons.Ellipsis

	type row struct {
		label, value string
		valueStyle   func(string) string
	}
	rows := make([]row, 0, 6)

	provider := m.providerName
	model := m.modelName
	if provider == "" {
		rows = append(rows, row{
			label: "model",
			value: "none" + space + "/provider",
			valueStyle: func(s string) string {
				return st.Muted.Render(s)
			},
		})
	} else {
		if model == "" {
			model = "default"
		}
		rows = append(rows, row{
			label: "model",
			value: provider + "/" + model,
			valueStyle: func(s string) string {
				return st.Accent.Render(s)
			},
		})
	}

	if m.agentName != "" && validAgentName(m.agentName) {
		rows = append(rows, row{
			label: "agent",
			value: sanitizeDisplayData(m.agentName),
			valueStyle: func(s string) string {
				return st.Text.Render(s)
			},
		})
	}
	if m.effort != protocol.EffortDefault {
		rows = append(rows, row{
			label: "effort",
			value: string(m.effort),
			valueStyle: func(s string) string {
				return st.Text.Render(s)
			},
		})
	}
	rows = append(rows, row{
		label: "autonomy",
		value: string(m.autonomy.Normalize()),
		valueStyle: func(s string) string {
			return st.Text.Render(s)
		},
	})
	if m.fastEnabled {
		rows = append(rows, row{
			label: "fast",
			value: "on",
			valueStyle: func(s string) string {
				return st.Warning.Render(s)
			},
		})
	}
	if m.showThinking {
		rows = append(rows, row{
			label: "thinking",
			value: "visible",
			valueStyle: func(s string) string {
				return st.Muted.Render(s)
			},
		})
	}
	if m.services.Auth != nil && m.providerName != "" {
		detail := strings.TrimSpace(m.services.Auth.Describe(m.providerName))
		if detail != "" {
			rows = append(rows, row{
				label: "auth",
				value: sanitizeDisplayData(detail),
				valueStyle: func(s string) string {
					return st.Muted.Render(s)
				},
			})
		}
	}
	skillCount := 0
	for _, skill := range m.skills {
		if validSkillName(skill.Name) {
			skillCount++
		}
	}
	if skillCount > 0 {
		rows = append(rows, row{
			label: "skills",
			value: strconv.Itoa(skillCount),
			valueStyle: func(s string) string {
				return st.Text.Render(s)
			},
		})
	}

	// Drop lowest-priority rows first (tail of the list) when height is tight.
	if len(rows) > height {
		rows = rows[:height]
	}

	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		label := st.Muted.Render(welcomeTruncate(r.label, width, ellipsis))
		labelW := ansi.StringWidth(ansi.Strip(label))
		gap := themedSpace(th.Spacing.SM)
		budget := max(0, width-labelW-ansi.StringWidth(gap))
		line := label
		if budget > 0 {
			line += gap + r.valueStyle(welcomeTruncate(r.value, budget, ellipsis))
		}
		if pad := width - ansi.StringWidth(ansi.Strip(line)); pad > 0 {
			line += themedSpace(pad)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// activityPaneBody shows a session tree when subagents exist, then parent tool
// activity, then idle tips. Never renders placeholder copy or child transcript.
func (m Model) activityPaneBody(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	th := m.th.Resolve()
	st := th.S()
	ic := iconsFor(th)
	space := themedSpace(th.Spacing.XS)
	ellipsis := th.Icons.Ellipsis

	lines := make([]string, 0, height)

	// Session tree (root + children) when any subagent is known.
	if len(m.listChildren(m.sessionID)) > 0 {
		treeH := height
		// Reserve at least one row for tools/tips when space allows.
		if height > 4 {
			treeH = min(height, max(2, len(ui.FlattenTree(m.sessionTreeNodes()))+1))
			if treeH > height-1 {
				treeH = height - 1
			}
		}
		tree := ui.Tree(th, ui.TreeOpts{
			Nodes:   m.sessionTreeNodes(),
			Cursor:  -1, // no interactive cursor in the activity summary
			Width:   width,
			Visible: treeH,
			Empty:   "",
		})
		if tree != "" {
			for _, row := range strings.Split(tree, "\n") {
				if len(lines) >= height {
					break
				}
				lines = append(lines, row)
			}
		}
	} else {
		// Flat fallback when only ephemeral children without ids exist.
		for i := len(m.children) - 1; i >= 0 && len(lines) < height; i-- {
			ch := m.children[i]
			if ch.status != "running" {
				continue
			}
			lines = append(lines, m.formatChildActivityLine(ch, width))
		}
		for i := len(m.children) - 1; i >= 0 && len(lines) < height; i-- {
			ch := m.children[i]
			if ch.status == "running" {
				continue
			}
			lines = append(lines, m.formatChildActivityLine(ch, width))
		}
	}

	// Parent tools (most recent first).
	if len(lines) < height {
		var tools []*toolCell
		for i := len(m.cells) - 1; i >= 0; i-- {
			switch c := m.cells[i].(type) {
			case *toolCell:
				tools = append(tools, c)
			case *exploreCell:
				for j := len(c.calls) - 1; j >= 0; j-- {
					if c.calls[j] != nil {
						tools = append(tools, c.calls[j])
					}
				}
			}
			if len(tools)+len(lines) >= height {
				break
			}
		}
		for _, tc := range tools {
			if len(lines) >= height {
				break
			}
			status := ic.Ellipsis
			statusStyle := st.Muted
			if tc.done {
				if tc.isError {
					status, statusStyle = ic.Err, st.Error
				} else {
					status, statusStyle = ic.OK, st.Success
				}
			}
			head := tc.name
			if tc.title != "" {
				head = tc.title
			}
			head = sanitizeDisplayData(head)
			prefix := ic.Tool + space
			suffix := space + status
			budget := max(0, width-ansi.StringWidth(prefix)-ansi.StringWidth(suffix))
			line := st.ToolLabel.Render(prefix) +
				st.Text.Render(welcomeTruncate(head, budget, ellipsis)) +
				statusStyle.Render(suffix)
			lines = append(lines, line)
		}
	}

	if len(lines) > 0 {
		return strings.Join(lines, "\n")
	}

	tips := []ui.KeyHint{
		{Key: "/", Label: "commands"},
		keyHint(m.keyMap.Palette),
		keyHint(m.keyMap.Agent),
		keyHint(m.keyMap.CycleWindowNext),
		keyHint(m.keyMap.ToggleOrientation),
		keyHint(m.keyMap.Newline),
		{Key: "ctrl+x down", Label: "subagent"},
	}
	if len(tips) > height {
		tips = tips[:height]
	}
	gap := themedSpace(th.Spacing.SM)
	out := make([]string, 0, len(tips))
	for _, tip := range tips {
		keyText := welcomeTruncate(tip.Key, width, ellipsis)
		budget := max(0, width-ansi.StringWidth(keyText)-ansi.StringWidth(gap))
		line := st.Accent.Render(keyText)
		if budget > 0 {
			line += st.Muted.Render(gap + welcomeTruncate(tip.Label, budget, ellipsis))
		}
		if pad := width - ansi.StringWidth(ansi.Strip(line)); pad > 0 {
			line += themedSpace(pad)
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func (m Model) formatChildActivityLine(ch childActivity, width int) string {
	th := m.th.Resolve()
	st := th.S()
	ic := iconsFor(th)
	space := themedSpace(th.Spacing.XS)
	ellipsis := th.Icons.Ellipsis

	statusGlyph, statusStyle := ic.Ellipsis, st.Muted
	switch ch.status {
	case "running":
		statusGlyph, statusStyle = ic.Ellipsis, st.AccentAlt
	case string(protocol.ChildStatusCompleted):
		statusGlyph, statusStyle = ic.OK, st.Success
	case string(protocol.ChildStatusFailed):
		statusGlyph, statusStyle = ic.Err, st.Error
	case string(protocol.ChildStatusCanceled):
		statusGlyph, statusStyle = ic.Info, st.Muted
	}

	agent := sanitizeDisplayData(ch.agent)
	if agent == "" {
		agent = "subagent"
	}
	detail := sanitizeDisplayData(ch.prompt)
	prefix := ic.Agent + space
	suffix := space + statusGlyph
	mid := agent
	if detail != "" {
		mid = agent + space + detail
	}
	budget := max(0, width-ansi.StringWidth(prefix)-ansi.StringWidth(suffix))
	return st.AccentAlt.Render(prefix) +
		st.Text.Render(welcomeTruncate(mid, budget, ellipsis)) +
		statusStyle.Render(suffix)
}
