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
	if m.phaseName != "" || m.phaseWorkflow != "" {
		phaseVal := sanitizeDisplayData(m.phaseName)
		if m.phaseWorkflow != "" && m.phaseName != "" {
			phaseVal = sanitizeDisplayData(m.phaseWorkflow) + "/" + sanitizeDisplayData(m.phaseName)
		} else if m.phaseWorkflow != "" {
			phaseVal = sanitizeDisplayData(m.phaseWorkflow)
		}
		if m.phaseGate != "" {
			phaseVal += " / " + sanitizeDisplayData(m.phaseGate)
		}
		if m.phaseStatus != "" {
			phaseVal += " / " + sanitizeDisplayData(m.phaseStatus)
		}
		rows = append(rows, row{
			label: "phase",
			value: phaseVal,
			valueStyle: func(s string) string {
				if m.phaseStatus != "" {
					return st.Warning.Render(s)
				}
				return st.AccentAlt.Render(s)
			},
		})
		if grants := m.activePhaseGrantsLabel(); grants != "" {
			rows = append(rows, row{
				label: "grants",
				value: grants,
				valueStyle: func(s string) string {
					return st.Warning.Render(s)
				},
			})
		}
	}
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

// contextPaneContentRows is the number of label/value rows the context body
// would paint (uncapped). Used for content-sized stack flex (#680).
func (m Model) contextPaneContentRows() int {
	n := 1 // model row always present
	if m.agentName != "" && validAgentName(m.agentName) {
		n++
	}
	if m.effort != protocol.EffortDefault {
		n++
	}
	n++ // autonomy
	if m.phaseName != "" || m.phaseWorkflow != "" {
		n++ // phase
		if m.activePhaseGrantsLabel() != "" {
			n++ // grants
		}
	}
	if m.fastEnabled {
		n++
	}
	if m.showThinking {
		n++
	}
	if m.services.Auth != nil && m.providerName != "" {
		if d := strings.TrimSpace(m.services.Auth.Describe(m.providerName)); d != "" {
			n++
		}
	}
	skillCount := 0
	for _, skill := range m.skills {
		if validSkillName(skill.Name) {
			skillCount++
		}
	}
	if skillCount > 0 {
		n++
	}
	return n
}

// activePhaseGrantsLabel summarizes pending effective phase permission grants
// from the host catalog for the active phase. Empty when none or unavailable.
func (m Model) activePhaseGrantsLabel() string {
	if m.phaseStatus != "" {
		return "none (recovery)"
	}
	if m.services.Workflows == nil || m.phaseWorkflow == "" || m.phaseName == "" {
		return ""
	}
	w, ok := m.services.Workflows.Get(m.phaseWorkflow)
	if !ok {
		return ""
	}
	for _, p := range w.Phases {
		if p.Name != m.phaseName {
			continue
		}
		if len(p.Permissions) == 0 {
			return ""
		}
		return sanitizeDisplayData(formatPhaseGrantsShort(p.Permissions))
	}
	return ""
}

// memberPreferredSizes returns per-member outer size hints for stack flex
// (#680). Values <=0 mean flex (absorb remainder). pairHorizontal prefers
// equal widths (no content signal), so returns nil for equal split.
func (m Model) memberPreferredSizes(g windowGroup, outerW, outerH int, compact, pairHorizontal bool) []int {
	n := len(g.members)
	if n < 2 || pairHorizontal {
		return nil
	}
	chrome := 0
	if !compact {
		chrome = 2 // top + bottom panel edge
	}
	// Cap any single preferred pane so flex siblings still get minStackMemberOuter.
	maxPref := max(minStackMemberOuter, outerH-minStackMemberOuter*(n-1))
	pref := make([]int, n)
	for i, wi := range g.members {
		var w window
		if wi >= 0 && wi < len(m.windows.windows) {
			w = m.windows.windows[wi]
		}
		if w == nil {
			pref[i] = 0 // flex
			continue
		}
		switch w.id() {
		case "context":
			body := m.contextPaneContentRows()
			pref[i] = min(maxPref, chrome+max(1, body))
		case "telemetry", "system":
			// CPU / RAM / disk — three metric rows (+ optional error line).
			pref[i] = min(maxPref, chrome+3)
		case "activity":
			// Activity is the flex feed: grow into leftover space.
			pref[i] = 0
		case queueWindowID:
			// Prefer content height when sparse; flex when empty so activity keeps room.
			body := m.queuePaneContentRows()
			if body <= 1 && len(m.inputQueue) == 0 && len(m.loops) == 0 &&
				strings.TrimSpace(m.queueLabel) == "" && len(m.queuePools) == 0 {
				pref[i] = min(maxPref, chrome+1)
			} else {
				pref[i] = min(maxPref, chrome+max(1, body))
			}
		default:
			if pw, ok := w.(pluginPaneWindow); ok && pw.prefHeight > 0 {
				pref[i] = min(maxPref, chrome+max(1, pw.prefHeight))
			} else {
				// Agents/files/etc. share space equally (flex) unless sparse.
				pref[i] = 0
			}
		}
	}
	// Ensure at least one flex member so remainder has a home.
	hasFlex := false
	for _, p := range pref {
		if p <= 0 {
			hasFlex = true
			break
		}
	}
	if !hasFlex {
		pref[n-1] = 0
	}
	return pref
}
