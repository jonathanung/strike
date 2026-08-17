package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

const welcomeCardMinWidth = 26

// welcomeView renders the empty-transcript dashboard as the web empty-state
// (kicker + title + muted line) plus unboxed section kickers. No bento tiles.
func (m Model) welcomeView(width, height int) string {
	th := m.th.Resolve()
	if width <= 0 || height <= 0 {
		return ""
	}
	if height < 3 {
		return welcomePadBlock(m.welcomeKeys(width, height), width)
	}

	gap := th.Spacing.SM
	statuses := []host.ProviderStatus(nil)
	if m.services.Auth != nil {
		statuses = m.services.Auth.Statuses()
	}
	cards := m.welcomeCards(statuses)

	kickerText, title, muted := "01 / ready", "Direct the work.", "Describe an outcome. Type below."
	if m.firstRun {
		kickerText, title = "01 / first run", "Set up the workspace."
	}
	heroH := emptyStateHeight(kickerText, title, muted)
	if height < heroH+3 {
		heroH = 0
	}

	sectionAvail := func(hero int) int {
		avail := height - hero
		if hero > 0 && gap > 0 {
			avail -= gap
		}
		return avail
	}
	// Prefer keeping eligible sections over the empty-state hero.
	if heroH > 0 && !welcomeSectionFits(sectionAvail(heroH), cards, gap) {
		heroH = 0
	}
	for len(cards) > 1 && !welcomeSectionFits(sectionAvail(heroH), cards, gap) {
		cards = welcomeDropCard(cards)
	}
	if len(cards) == 0 {
		if heroH > 0 {
			return welcomePadBlock(emptyStateBlock(th, width, kickerText, title, muted), width)
		}
		return welcomePadBlock(m.welcomeKeys(width, height), width)
	}

	parts := make([]string, 0, height)
	used := 0
	if heroH > 0 {
		parts = append(parts, emptyStateBlock(th, width, kickerText, title, muted))
		used += heroH
		for i := 0; i < gap && used < height; i++ {
			parts = append(parts, themedSpace(width))
			used++
		}
	}

	remain := height - used
	sectionGapTotal := gap * max(0, len(cards)-1)
	bodyBudget := max(0, remain-len(cards)-sectionGapTotal)
	// Each section: 1 kicker row + at least 1 body row when space allows.
	bodyEach := make([]int, len(cards))
	sumBody := 0
	for i := range cards {
		bodyEach[i] = 1
		if cards[i].title == "recent prompts" {
			bodyEach[i] = 3
		}
		sumBody += bodyEach[i]
	}
	extra := bodyBudget - sumBody
	for extra > 0 {
		grew := false
		for i := range cards {
			if extra == 0 {
				break
			}
			want := max(1, cards[i].desired-1)
			if bodyEach[i] < want {
				bodyEach[i]++
				extra--
				grew = true
			}
		}
		if !grew {
			break
		}
	}

	for i, card := range cards {
		if used >= height {
			break
		}
		if i > 0 && gap > 0 && used < height {
			for g := 0; g < gap && used < height; g++ {
				parts = append(parts, themedSpace(width))
				used++
			}
		}
		if used >= height {
			break
		}
		parts = append(parts, padInspectorLine(th, welcomeCardTitle(th, card.title, card.tone), width))
		used++
		bodyRows := bodyEach[i]
		if used+bodyRows > height {
			bodyRows = max(0, height-used)
		}
		if bodyRows > 0 {
			parts = append(parts, card.body(width, bodyRows))
			used += bodyRows
		}
	}
	out := welcomePadBlock(strings.Join(parts, "\n"), width)
	rows := strings.Split(out, "\n")
	if len(rows) > height {
		rows = rows[:height]
	}
	for len(rows) < height {
		rows = append(rows, themedSpace(width))
	}
	for i := range rows {
		rows[i] = padInspectorLine(th, rows[i], width)
	}
	return strings.Join(rows, "\n")
}

func welcomeSectionFits(height int, cards []welcomeCard, gap int) bool {
	if len(cards) == 0 {
		return true
	}
	// 1 kicker + 1 body per section, plus inter-section gaps.
	need := 2*len(cards) + gap*max(0, len(cards)-1)
	return height >= need
}

type welcomeCard struct {
	title   string
	tone    ui.Tone // multi-accent title hierarchy; body stays default surface
	primary bool
	desired int
	body    func(width, rows int) string
}

// welcomeCardTitle paints the section kicker in its accent tone.
func welcomeCardTitle(th theme.Theme, title string, tone ui.Tone) string {
	th = th.Resolve()
	if title == "" {
		return ""
	}
	switch tone {
	case ui.ToneAccentAlt:
		return kicker(th.S().AccentAltStrong, title)
	case ui.ToneSuccess:
		return kicker(th.S().SuccessStrong, title)
	case ui.ToneMuted:
		return kicker(th.S().MutedStrong, title)
	default:
		return kicker(th.S().Title, title)
	}
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
		// Defense-in-depth: transcriptView gates firstRun to avoid welcome
		// on spawned roots (#1092). This branch still guards direct
		// welcomeView callers (tests, edge reflows).
		if m.firstRun {
			cards = append(cards, welcomeCard{title: "first run", tone: ui.ToneAccentAlt, primary: true, desired: 7, body: func(width, rows int) string {
				return m.welcomeFirstRun(width, rows)
			}})
		} else {
			cards = append(cards, welcomeCard{title: "get started", tone: ui.ToneAccentAlt, primary: true, desired: 9, body: func(width, rows int) string {
				return m.welcomeProviders(statuses, width, rows)
			}})
		}
	}
	cards = append(cards, welcomeCard{title: "keys", tone: ui.ToneAccent, desired: 10, body: func(width, rows int) string {
		return m.welcomeKeys(width, rows)
	}})
	if len(validAgents) > 0 || len(validSkills) > 0 {
		cards = append(cards, welcomeCard{title: "agents & skills", tone: ui.ToneSuccess, desired: 8, body: func(width, rows int) string {
			return m.welcomeAgentsSkills(validAgents, validSkills, width, rows)
		}})
	}
	if len(m.entries) > 0 {
		cards = append(cards, welcomeCard{title: "recent prompts", tone: ui.ToneMuted, desired: 5, body: m.welcomeRecent})
	}
	if len(cards) > 0 && !cards[0].primary {
		cards[0].primary = true
	}
	return cards
}

func welcomeFits(height, cards, columns, gap int) bool {
	rows := (cards + columns - 1) / columns
	return height >= 3*rows+gap*(rows-1)
}

func welcomeDropCard(cards []welcomeCard) []welcomeCard {
	for _, title := range []string{"recent prompts", "agents & skills", "keys"} {
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

// welcomeFirstRun is the empty-transcript onboarding card for a fresh install.
func (m Model) welcomeFirstRun(width, rows int) string {
	th := m.th.Resolve()
	st := th.S()
	type step struct {
		head, detail string
	}
	steps := []step{
		{"1. Sign in", "/auth or pick a provider"},
		{"2. Project setup", "/init writes AGENTS.md"},
		{"3. Choose a model", "/model"},
		{"4. Send a message", "type below, enter to send"},
	}
	lines := make([]string, 0, min(len(steps), rows))
	for _, s := range steps {
		if len(lines) >= rows {
			break
		}
		head := st.Accent.Render(welcomeTruncate(s.head, width, th.Icons.Ellipsis))
		if s.detail == "" {
			lines = append(lines, head)
			continue
		}
		// Hierarchy: numbered step accented, detail muted on the same or next row.
		gap := themedSpace(th.Spacing.SM)
		headW := ansi.StringWidth(ansi.Strip(head))
		budget := max(0, width-headW-ansi.StringWidth(gap))
		if budget >= 8 {
			lines = append(lines, head+st.Muted.Render(gap+welcomeTruncate(s.detail, budget, th.Icons.Ellipsis)))
		} else {
			lines = append(lines, head)
			if len(lines) < rows {
				lines = append(lines, st.Muted.Render(welcomeTruncate(s.detail, width, th.Icons.Ellipsis)))
			}
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
	lines := make([]string, 0, min(6, rows))
	if rows > 0 {
		action := welcomeTruncate("/provider"+space+"connect a model", width, th.Icons.Ellipsis)
		command, detail, hasDetail := strings.Cut(action, space)
		line := st.AccentStrong.Render(command)
		if hasDetail {
			line += st.Text.Render(space + detail)
		}
		lines = append(lines, line)
	}
	providerBudget := 5
	providers := 0
	for _, status := range ordered {
		if providers >= providerBudget || len(lines) >= rows {
			break
		}
		glyph := th.Icons.Info
		nameStyle, detailStyle := st.Muted, st.Muted
		if status.Authed {
			glyph, nameStyle, detailStyle = th.Icons.OK, st.Success, st.Muted
		}
		name := welcomeDisplay(status.Name, width)
		detail := welcomeDisplay(status.Detail, width)
		prefix := glyph + space
		budget := max(0, width-ansi.StringWidth(prefix))
		namePart := nameStyle.Render(welcomeTruncate(name, budget, th.Icons.Ellipsis))
		line := st.Muted.Render(prefix) + namePart
		if detail != "" {
			used := ansi.StringWidth(ansi.Strip(line))
			gap := themedSpace(th.Spacing.SM)
			rest := max(0, width-used-ansi.StringWidth(gap))
			if rest > 2 {
				line += detailStyle.Render(gap + welcomeTruncate(detail, rest, th.Icons.Ellipsis))
			}
		}
		lines = append(lines, line)
		providers++
	}
	if providers == 0 && len(lines) == 0 && rows > 1 {
		lines = append(lines, st.Muted.Render(welcomeTruncate("no providers configured", width, th.Icons.Ellipsis)))
	}
	if len(lines) < rows && m.agentsMDMissing() {
		action := welcomeTruncate("/init"+space+"project AGENTS.md", width, th.Icons.Ellipsis)
		command, detail, hasDetail := strings.Cut(action, space)
		line := st.Accent.Render(command)
		if hasDetail {
			line += st.Muted.Render(space + detail)
		}
		lines = append(lines, line)
	}
	if len(lines) < rows {
		tip := welcomeTruncate(dotJoin(th, "type below", "enter to send"), width, th.Icons.Ellipsis)
		lines = append(lines, st.Muted.Render(tip))
	}
	return strings.Join(lines, "\n")
}

// agentsMDMissing reports whether /init would create AGENTS.md. Unknown or
// unavailable init services are treated as not missing (no CTA).
func (m Model) agentsMDMissing() bool {
	if m.services.Init == nil {
		return false
	}
	exists, _, err := m.services.Init.Exists()
	return err == nil && !exists
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
	bindings := []key.Binding{
		m.keyMap.FocusLeft, m.keyMap.FocusRight,
		m.keyMap.CycleWindowNext, m.keyMap.CycleWindowPrev,
		m.keyMap.ToggleOrientation,
		m.keyMap.ExternalEditor,
		m.keyMap.Palette, m.keyMap.KeyHelp, m.keyMap.Interrupt,
	}
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
