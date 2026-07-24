package tui

import (
	"strings"

	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// welcomeView is the empty-transcript dashboard: a bento grid of cards that
// makes a fresh session inviting and orienting. It is recomputed on every
// resize (refreshViewport rebuilds it against the current viewport width) so
// the cards repack for the terminal.
func (m Model) welcomeView(width int) string {
	cards := []ui.Card{
		{Title: "strike", Body: ui.Logo(m.th), Width: 24, Tone: ui.ToneAccent},
		{Title: "get started", Body: m.welcomeProviders(), Width: 38},
		{Title: "keys", Body: m.welcomeKeys(), Width: 26},
		{Title: "agents & skills", Body: m.welcomeAgentsSkills(), Width: 30},
	}
	if len(m.entries) > 0 {
		cards = append(cards, ui.Card{Title: "recent prompts", Body: m.welcomeRecent(), Width: 38})
	}
	return ui.Bento(m.th, width, 2, cards)
}

// welcomeProviders lists every provider with its credential state, so the
// first thing a new user sees is which providers are ready and how to pick one.
func (m Model) welcomeProviders() string {
	ic := m.themeIcons()
	st := m.th.S()
	var lines []string
	if m.services.Auth != nil {
		for _, s := range m.services.Auth.Statuses() {
			glyph, style := ic.Info, st.Muted
			if s.Authed {
				glyph, style = ic.OK, st.Success
			}
			lines = append(lines, style.Render(glyph)+" "+st.Text.Render(s.Name)+st.Muted.Render("  "+s.Detail))
		}
	}
	if len(lines) == 0 {
		lines = append(lines, st.Muted.Render("no providers configured"))
	}
	lines = append(lines, st.AccentAlt.Render("/provider")+st.Muted.Render(" to choose one"))
	return strings.Join(lines, "\n")
}

// welcomeKeys is the short list of the most useful keybindings.
func (m Model) welcomeKeys() string {
	st := m.th.S()
	rows := [][2]string{
		{"enter", "send"},
		{"ctrl+k", "palette"},
		{"tab", "switch agent"},
		{"ctrl+d", "save defaults"},
		{"esc", "interrupt"},
	}
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, st.Accent.Render(r[0])+st.Muted.Render("  "+r[1]))
	}
	return strings.Join(lines, "\n")
}

// welcomeAgentsSkills lists the selectable agents and the available slash-command
// skills, drawn straight from the host services.
func (m Model) welcomeAgentsSkills() string {
	ic := m.themeIcons()
	st := m.th.S()
	var lines []string
	lines = append(lines, st.Muted.Render("agents"))
	if len(m.agents) == 0 {
		lines = append(lines, "  "+st.Muted.Render("(none)"))
	}
	for _, name := range m.agents {
		if !validAgentName(name) {
			continue
		}
		lines = append(lines, "  "+st.AccentAlt.Render(ic.Agent+" ")+st.Text.Render(name))
	}
	lines = append(lines, st.Muted.Render("skills"))
	shown := 0
	for _, s := range m.skills {
		if !validSkillName(s.Name) {
			continue
		}
		lines = append(lines, "  "+st.Accent.Render("/"+sanitizeDisplayData(s.Name)))
		if shown++; shown >= 4 {
			break
		}
	}
	if shown == 0 {
		lines = append(lines, "  "+st.Muted.Render("(none)"))
	}
	return strings.Join(lines, "\n")
}

// welcomeRecent shows the last few prompt-history entries, newest first, as a
// jumping-off point.
func (m Model) welcomeRecent() string {
	st := m.th.S()
	var lines []string
	for i := len(m.entries) - 1; i >= 0 && len(lines) < 3; i-- {
		entry := sanitizeDisplayData(strings.ReplaceAll(m.entries[i], "\n", " "))
		lines = append(lines, st.Muted.Render(m.themeIcons().Dot+" ")+st.Text.Render(entry))
	}
	return strings.Join(lines, "\n")
}
