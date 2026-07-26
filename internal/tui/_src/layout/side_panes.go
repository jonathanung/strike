package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
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
