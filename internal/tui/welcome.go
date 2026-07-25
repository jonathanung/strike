package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const welcomeCardMinWidth = 26

// welcomeView renders the empty-transcript dashboard directly into its allotted
// rectangle. Its cards are fixed-height so their borders survive short views.
func (m Model) welcomeView(width, height int) string {
	th := m.th.Resolve()
	if width <= 0 || height <= 0 {
		return ""
	}
	if height < 3 {
		return ui.Panel(th, ui.PanelOpts{Width: width, Height: height, Borderless: true}, m.welcomeKeys(width, height))
	}

	statuses := []host.ProviderStatus(nil)
	if m.services.Auth != nil {
		statuses = m.services.Auth.Statuses()
	}
	cards := m.welcomeCards(statuses)
	gap := th.Spacing.SM
	columns := 1
	if width >= 2*welcomeCardMinWidth+gap {
		columns = 2
	}
	for len(cards) > 1 && !welcomeFits(height, len(cards), columns, gap) {
		cards = welcomeDropCard(cards)
	}
	rows := (len(cards) + columns - 1) / columns
	if !welcomeFits(height, len(cards), columns, gap) {
		return ui.Panel(th, ui.PanelOpts{Width: width, Height: height, Borderless: true}, m.welcomeKeys(width, height))
	}

	rowHeights := welcomeRowHeights(cards, rows, columns, height-gap*(rows-1))
	rowWidth := width
	cardWidth := width
	if columns == 2 {
		cardWidth = (width - gap) / 2
	}
	blocks := make([]string, 0, rows)
	for row := 0; row < rows; row++ {
		start := row * columns
		end := min(start+columns, len(cards))
		parts := make([]string, 0, 2*(end-start)-1)
		for _, card := range cards[start:end] {
			if len(parts) > 0 {
				parts = append(parts, themedSpace(gap))
			}
			inner := ui.PanelInnerWidth(th, cardWidth)
			bodyRows := max(0, rowHeights[row]-2)
			parts = append(parts, ui.Panel(th, ui.PanelOpts{
				Title:   card.title,
				Width:   cardWidth,
				Height:  rowHeights[row],
				Focused: m.focus == focusLeft && m.modal == nil,
				Dim:     m.focus == focusRight || m.modal != nil,
				Tone:    ui.ToneDefault,
			}, card.body(inner, bodyRows)))
		}
		block := lipgloss.JoinHorizontal(lipgloss.Top, parts...)
		blocks = append(blocks, welcomePadBlock(block, rowWidth))
	}
	return welcomePadBlock(lipgloss.JoinVertical(lipgloss.Left, joinWelcomeRows(blocks, gap)...), width)
}

type welcomeCard struct {
	title   string
	desired int
	body    func(width, rows int) string
}

func (m Model) welcomeCards(statuses []host.ProviderStatus) []welcomeCard {
	validAgents := make([]string, 0, len(m.agents))
	for _, name := range m.agents {
		if validAgentName(name) {
			validAgents = append(validAgents, name)
		}
	}
	validSkills := make([]host.Skill, 0, len(m.skills))
	for _, skill := range m.skills {
		if validSkillName(skill.Name) {
			validSkills = append(validSkills, skill)
		}
	}
	selectedUnauthed := false
	for _, status := range statuses {
		if status.Name == m.providerName && !status.Authed {
			selectedUnauthed = true
			break
		}
	}
	cards := make([]welcomeCard, 0, 4)
	if m.providerName == "" || selectedUnauthed {
		cards = append(cards, welcomeCard{title: "get started", desired: 7, body: func(width, rows int) string {
			return m.welcomeProviders(statuses, width, rows)
		}})
	}
	cards = append(cards, welcomeCard{title: "keys", desired: 9, body: func(width, rows int) string {
		return m.welcomeKeys(width, rows)
	}})
	if len(validAgents) > 0 || len(validSkills) > 0 {
		cards = append(cards, welcomeCard{title: "agents & skills", desired: 8, body: func(width, rows int) string {
			return m.welcomeAgentsSkills(validAgents, validSkills, width, rows)
		}})
	}
	if len(m.entries) > 0 {
		cards = append(cards, welcomeCard{title: "recent prompts", desired: 5, body: m.welcomeRecent})
	}
	return cards
}

func welcomeFits(height, cards, columns, gap int) bool {
	rows := (cards + columns - 1) / columns
	return height >= 3*rows+gap*(rows-1)
}

func welcomeDropCard(cards []welcomeCard) []welcomeCard {
	for _, title := range []string{"recent prompts", "agents & skills", "get started"} {
		for i, card := range cards {
			if card.title == title {
				return append(cards[:i:i], cards[i+1:]...)
			}
		}
	}
	return cards
}

func welcomeRowHeights(cards []welcomeCard, rows, columns, available int) []int {
	heights := make([]int, rows)
	desired := make([]int, rows)
	for i := range heights {
		heights[i] = 3
		desired[i] = 3
		for _, card := range cards[i*columns : min((i+1)*columns, len(cards))] {
			desired[i] = max(desired[i], card.desired)
		}
	}
	for remaining, next := available-3*rows, 0; remaining > 0; remaining-- {
		selected := -1
		for offset := range heights {
			row := (next + offset) % rows
			if heights[row] < desired[row] {
				selected = row
				break
			}
		}
		if selected < 0 {
			selected = next % rows
		}
		heights[selected]++
		next = (selected + 1) % rows
	}
	return heights
}

func joinWelcomeRows(rows []string, gap int) []string {
	if len(rows) < 2 || gap <= 0 {
		return rows
	}
	joined := make([]string, 0, len(rows)+(len(rows)-1)*gap)
	for i, row := range rows {
		if i > 0 {
			joined = append(joined, make([]string, gap)...)
		}
		joined = append(joined, row)
	}
	return joined
}

func welcomePadBlock(block string, width int) string {
	if width <= 0 {
		return ""
	}
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		if pad := width - ansi.StringWidth(line); pad > 0 {
			lines[i] = line + themedSpace(pad)
		}
	}
	return strings.Join(lines, "\n")
}

func (m Model) welcomeProviders(statuses []host.ProviderStatus, width, rows int) string {
	th := m.th.Resolve()
	st := th.S()
	space := themedSpace(th.Spacing.XS)
	ordered := append([]host.ProviderStatus(nil), statuses...)
	for i, status := range ordered {
		if status.Name == m.providerName && !status.Authed {
			ordered = append([]host.ProviderStatus{status}, append(ordered[:i], ordered[i+1:]...)...)
			break
		}
	}
	lines := make([]string, 0, min(5, rows))
	for _, status := range ordered {
		if len(lines) == 4 || len(lines) >= rows {
			break
		}
		glyph, style := th.Icons.Info, st.Muted
		if status.Authed {
			glyph, style = th.Icons.OK, st.Success
		}
		name := welcomeDisplay(status.Name, width)
		detail := welcomeDisplay(status.Detail, width)
		text := glyph + space + name
		if detail != "" {
			text += themedSpace(th.Spacing.SM) + detail
		}
		lines = append(lines, style.Render(welcomeTruncate(text, width, th.Icons.Ellipsis)))
	}
	if len(lines) == 0 && rows > 1 {
		lines = append(lines, st.Muted.Render(welcomeTruncate("no providers configured", width, th.Icons.Ellipsis)))
	}
	if len(lines) < rows {
		action := welcomeTruncate("/provider"+space+"to choose one", width, th.Icons.Ellipsis)
		command, detail, hasDetail := strings.Cut(action, space)
		line := st.AccentAlt.Render(command)
		if hasDetail {
			line += st.Muted.Render(space + detail)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m Model) welcomeKeys(size ...int) string {
	width, rows := 80, 8
	if len(size) > 0 {
		width = size[0]
	}
	if len(size) > 1 {
		rows = size[1]
	}
	th := m.th.Resolve()
	st := th.S()
	gap := themedSpace(th.Spacing.SM)
	bindings := []key.Binding{m.keyMap.FocusLeft, m.keyMap.FocusRight, m.keyMap.CycleWindowNext, m.keyMap.CycleWindowPrev, m.keyMap.Palette, m.keyMap.KeyHelp, m.keyMap.Send, m.keyMap.Interrupt}
	lines := make([]string, 0, min(rows, len(bindings)))
	for _, binding := range bindings {
		if len(lines) >= rows {
			break
		}
		help := binding.Help()
		keyText := welcomeTruncate(welcomeDisplay(help.Key, width), width, th.Icons.Ellipsis)
		budget := max(0, width-ansi.StringWidth(keyText)-len(gap))
		lines = append(lines, st.Accent.Render(keyText)+st.Muted.Render(gap+welcomeTruncate(welcomeDisplay(help.Desc, width), budget, th.Icons.Ellipsis)))
	}
	return strings.Join(lines, "\n")
}

func (m Model) welcomeAgentsSkills(agents []string, skills []host.Skill, width, rows int) string {
	th := m.th.Resolve()
	st := th.S()
	indent := themedSpace(th.Spacing.SM)
	space := themedSpace(th.Spacing.XS)
	lines := make([]string, 0, min(6, rows))
	add := func(line string) {
		if len(lines) < rows && len(lines) < 6 {
			lines = append(lines, line)
		}
	}
	if len(agents) > 0 && len(skills) > 0 {
		add(st.Muted.Render("agents"))
		add(st.AccentAlt.Render(welcomeTruncate(indent+th.Icons.Agent+space+welcomeDisplay(agents[0], width), width, th.Icons.Ellipsis)))
		add(st.Muted.Render("skills"))
		add(st.Accent.Render(welcomeTruncate(indent+"/"+welcomeDisplay(skills[0].Name, width), width, th.Icons.Ellipsis)))
		for _, name := range agents[1:] {
			add(st.AccentAlt.Render(welcomeTruncate(indent+th.Icons.Agent+space+welcomeDisplay(name, width), width, th.Icons.Ellipsis)))
		}
		for _, skill := range skills[1:] {
			add(st.Accent.Render(welcomeTruncate(indent+"/"+welcomeDisplay(skill.Name, width), width, th.Icons.Ellipsis)))
		}
	} else if len(agents) > 0 {
		add(st.Muted.Render("agents"))
		for _, name := range agents {
			add(st.AccentAlt.Render(welcomeTruncate(indent+th.Icons.Agent+space+welcomeDisplay(name, width), width, th.Icons.Ellipsis)))
		}
	} else {
		add(st.Muted.Render("skills"))
		for _, skill := range skills {
			add(st.Accent.Render(welcomeTruncate(indent+"/"+welcomeDisplay(skill.Name, width), width, th.Icons.Ellipsis)))
		}
	}
	return strings.Join(lines, "\n")
}

func (m Model) welcomeRecent(width, rows int) string {
	th := m.th.Resolve()
	st := th.S()
	prefix := th.Icons.Dot + themedSpace(th.Spacing.XS)
	budget := max(0, width-ansi.StringWidth(prefix))
	lines := make([]string, 0, min(3, rows))
	for i := len(m.entries) - 1; i >= 0 && len(lines) < 3 && len(lines) < rows; i-- {
		entry := welcomeDisplay(m.entries[i], width)
		lines = append(lines, st.Muted.Render(prefix)+st.Text.Render(ansi.Truncate(entry, budget, th.Icons.Ellipsis)))
	}
	return strings.Join(lines, "\n")
}

func welcomeDisplay(value string, width int) string {
	value = strings.NewReplacer("\n", " ", "\r", " ").Replace(value)
	return welcomeTruncate(sanitizeDisplayData(value), width, "")
}

func welcomeTruncate(value string, width int, ellipsis string) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(value, width, ellipsis)
}
