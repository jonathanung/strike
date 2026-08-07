package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// homePromptMaxWidth caps the centered home composer so it reads as a focal
// input rather than a full-width bar.
const homePromptMaxWidth = 56

// homePromptMinWidth is the narrowest useful centered prompt box.
const homePromptMinWidth = 28

// showHomeLayout is the pre-first-prompt screen: empty root transcript only.
// After the first user/assistant/tool cell appears, the normal multi-pane
// layout takes over (#677). ctrl+l / focus-right from home opens the right
// pane column and keeps multi-pane with the launch stack on the left (#684).
// testForceMultiPane lets unit tests keep the multi-pane surface without
// seeding a fake user message.
func (m Model) showHomeLayout() bool {
	if m.testForceMultiPane || m.homePanesOpen {
		return false
	}
	return len(m.displayCells()) == 0 && !m.viewingChild()
}

// homePromptWidth is the outer width of the centered home prompt panel.
func homePromptWidth(screenWidth int) int {
	if screenWidth <= 0 {
		return 0
	}
	// Leave a little side margin so the box feels centered, not edge-to-edge.
	budget := screenWidth - 4
	if budget < homePromptMinWidth {
		return max(1, screenWidth)
	}
	if budget > homePromptMaxWidth {
		return homePromptMaxWidth
	}
	return budget
}

// homeContextBar is a single-row summary under the header (agent · autonomy ·
// auth · skills · permissions). Full-width thin panel matching the #677 mock.
func (m Model) homeContextBar(width int) string {
	if width <= 0 {
		return ""
	}
	th := m.th.Resolve()
	st := th.S()
	parts := m.homeContextParts(th)
	inner := ui.PanelInnerWidth(th, width)
	body := st.Muted.Render(welcomeTruncate(dotJoin(th, parts...), inner, th.Icons.Ellipsis))
	// Prefer a one-body-row panel; fall back to bare text when chrome won't fit.
	if width < ui.ChromeMinOuter(th) || m.height < compactHeight {
		return padHomeLine(th, body, width)
	}
	return ui.Panel(th, ui.PanelOpts{
		Title:  "context",
		Width:  width,
		Height: 3,
		Dim:    true,
	}, body)
}

// homeContextParts builds the muted summary segments for the home context bar.
func (m Model) homeContextParts(th theme.Theme) []string {
	th = th.Resolve()
	parts := make([]string, 0, 6)
	if m.agentName != "" && validAgentName(m.agentName) {
		parts = append(parts, sanitizeDisplayData(m.agentName)+" agent")
	}
	parts = append(parts, string(m.autonomy.Normalize()))
	if m.phaseName != "" || m.phaseWorkflow != "" {
		phase := m.phaseName
		if m.phaseWorkflow != "" && m.phaseName != "" {
			phase = m.phaseWorkflow + "/" + m.phaseName
		} else if m.phaseWorkflow != "" {
			phase = m.phaseWorkflow
		}
		if m.phaseGate != "" {
			phase += "/" + m.phaseGate
		}
		parts = append(parts, "phase "+sanitizeDisplayData(phase))
	}
	if m.services.Auth != nil && m.providerName != "" {
		if d := strings.TrimSpace(m.services.Auth.Describe(m.providerName)); d != "" {
			parts = append(parts, sanitizeDisplayData(d))
		}
	}
	skillCount := 0
	for _, skill := range m.skills {
		if validSkillName(skill.Name) {
			skillCount++
		}
	}
	if skillCount > 0 {
		parts = append(parts, itoa(skillCount)+" skills")
	}
	if m.permMode.Normalize() != protocol.PermissionModeDefault {
		parts = append(parts, m.permMode.Short())
	} else {
		parts = append(parts, "default permissions")
	}
	if iso := m.isolationLabel(); iso != "" {
		parts = append(parts, iso)
	}
	if m.firstRun {
		parts = append(parts, "first run")
	}
	if m.providerName == "" {
		parts = append(parts, "/provider to connect")
	}
	return parts
}

// homeRecentLine is a single muted row under the home prompt: recent · "…".
func (m Model) homeRecentLine(width int) string {
	if width <= 0 || len(m.entries) == 0 {
		return ""
	}
	th := m.th.Resolve()
	st := th.S()
	entry := welcomeDisplay(m.entries[len(m.entries)-1], width)
	if entry == "" {
		return ""
	}
	// Quote lightly for scanability; truncate the whole line to width.
	label := "recent"
	quoted := `"` + entry + `"`
	line := st.Muted.Render(label) +
		st.Muted.Render(themedSpace(th.Spacing.XS)+th.Icons.Dot+themedSpace(th.Spacing.XS)) +
		st.Text.Render(quoted)
	plain := label + " " + th.Icons.Dot + " " + quoted
	if ansi.StringWidth(plain) > width {
		budget := max(0, width-ansi.StringWidth(label+" "+th.Icons.Dot+" \"\""))
		quoted = `"` + welcomeTruncate(entry, budget, th.Icons.Ellipsis) + `"`
		line = st.Muted.Render(label) +
			st.Muted.Render(themedSpace(th.Spacing.XS)+th.Icons.Dot+themedSpace(th.Spacing.XS)) +
			st.Text.Render(quoted)
	}
	return padHomeLine(th, line, width)
}

// homeCenterBand vertically centers logo + completion popup + prompt (+ optional
// recent) in the allotted height, matching the pre-first-prompt mock (#677).
func (m Model) homeCenterBand(width, height, promptOuterH int, compact bool, popup string, popupH int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	th := m.th.Resolve()
	promptW := homePromptWidth(width)

	// Logo: full wordmark when space allows, else compact.
	logo := ""
	logoH := 0
	if height >= 12 && width >= 18 {
		logo = ui.Logo(th)
		logoH = 3
	} else if height >= 8 && width >= 12 {
		logo = ui.LogoCompact(th)
		logoH = 1
	}

	recent := m.homeRecentLine(min(width, promptW+8))
	recentH := 0
	if recent != "" {
		recentH = 1
	}

	// Tip strip above the centered prompt when composer is empty (#664).
	tip := ""
	tipH := 0
	if m.showComposerTip() && height >= promptOuterH+2 {
		tip = m.tipView(min(width, promptW+8))
		if tip != "" {
			tipH = 1
		}
	}

	// Gaps: one row under logo, one under prompt before recent.
	gapLogo, gapRecent := 1, 0
	if logoH > 0 {
		gapLogo = 1
	} else {
		gapLogo = 0
	}
	if recentH > 0 {
		gapRecent = 1
	}

	blockH := logoH + gapLogo + popupH + tipH + promptOuterH + gapRecent + recentH
	// If the band is too short, drop tip then logo then recent to keep the prompt.
	for blockH > height && tipH > 0 {
		tip, tipH = "", 0
		blockH = logoH + gapLogo + popupH + tipH + promptOuterH + gapRecent + recentH
	}
	for blockH > height && logoH > 0 {
		logo, logoH, gapLogo = "", 0, 0
		blockH = logoH + gapLogo + popupH + tipH + promptOuterH + gapRecent + recentH
	}
	for blockH > height && recentH > 0 {
		recent, recentH, gapRecent = "", 0, 0
		blockH = logoH + gapLogo + popupH + tipH + promptOuterH + gapRecent + recentH
	}
	if promptOuterH+popupH > height {
		promptOuterH = max(0, height-popupH)
		blockH = popupH + promptOuterH
		logo, logoH, gapLogo = "", 0, 0
		recent, recentH, gapRecent = "", 0, 0
		tip, tipH = "", 0
	}

	topPad := max(0, (height-blockH)/2)
	bottomPad := max(0, height-blockH-topPad)

	parts := make([]string, 0, height)
	for i := 0; i < topPad; i++ {
		parts = append(parts, themedSpace(width))
	}
	if logoH > 0 {
		parts = append(parts, centerHomeBlock(th, logo, width))
		for i := 0; i < gapLogo; i++ {
			parts = append(parts, themedSpace(width))
		}
	}
	if popupH > 0 && popup != "" {
		parts = append(parts, centerHomeBlock(th, popup, width))
	}
	if tipH > 0 && tip != "" {
		parts = append(parts, centerHomeBlock(th, tip, width))
	}
	prompt := m.composerView(compact, promptW, promptOuterH)
	parts = append(parts, centerHomeBlock(th, prompt, width))
	if recentH > 0 {
		for i := 0; i < gapRecent; i++ {
			parts = append(parts, themedSpace(width))
		}
		parts = append(parts, centerHomeBlock(th, recent, width))
	}
	for i := 0; i < bottomPad; i++ {
		parts = append(parts, themedSpace(width))
	}
	// Fit exactly to height (safety).
	out := strings.Join(parts, "\n")
	rows := strings.Split(out, "\n")
	if len(rows) > height {
		rows = rows[:height]
	}
	for len(rows) < height {
		rows = append(rows, themedSpace(width))
	}
	for i := range rows {
		rows[i] = padHomeLine(th, rows[i], width)
	}
	return strings.Join(rows, "\n")
}

// centerHomeBlock centers each line of block within width using themed spaces.
func centerHomeBlock(th theme.Theme, block string, width int) string {
	if width <= 0 {
		return ""
	}
	th = th.Resolve()
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		w := ansi.StringWidth(ansi.Strip(line))
		if w >= width {
			lines[i] = padHomeLine(th, line, width)
			continue
		}
		left := (width - w) / 2
		right := width - w - left
		lines[i] = themedSpace(left) + line + themedSpace(right)
	}
	return strings.Join(lines, "\n")
}

func padHomeLine(th theme.Theme, line string, width int) string {
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

// homeComposerRows is the textarea row count used while the home layout is
// active (same visual-line logic as reflow, but width is the centered box).
func (m Model) homeComposerRows(promptOuterW int, compact bool) int {
	contentW := promptOuterW
	if !compact && promptOuterW >= 3 {
		contentW = ui.PanelInnerWidth(m.th, promptOuterW)
	}
	return composerVisualRows(m.composer.Value(), max(1, contentW))
}

// composerVisualRows counts wrapped visual lines for the composer value.
func composerVisualRows(value string, contentWidth int) int {
	// Local import-free line count via textarea would pull bubbles; mirror
	// reflow's clamp without mutating the live composer.
	if contentWidth < 1 {
		contentWidth = 1
	}
	visualLines := 0
	for _, line := range strings.Split(value, "\n") {
		// Approximate wrap: rune width / contentWidth (good enough for budget).
		rw := max(1, ansi.StringWidth(line))
		visualLines += max(1, (rw+contentWidth-1)/contentWidth)
		if visualLines >= composerMaxHeight {
			return composerMaxHeight
		}
	}
	if visualLines == 0 {
		visualLines = 1
	}
	return min(composerMaxHeight, max(composerMinHeight, visualLines))
}

// homeLayoutBudget returns region heights for the home screen. Sum equals height.
type homeLayout struct {
	header   int
	context  int
	center   int // logo + prompt + recent (flex)
	notice   int
	popup    int
	hints    int
	danger   int
	composer int // outer prompt height inside center (for reflow/composer SetHeight)
	compact  bool
}

func computeHomeLayout(width, height, composerRows, popupHeight int, danger bool, noticeRows int, hasContext bool) homeLayout {
	height = max(0, height)
	h := homeLayout{
		compact: width < compactWidth || height < compactHeight,
		header:  1,
		hints:   1,
		popup:   max(0, popupHeight),
		notice:  max(0, noticeRows),
	}
	if danger {
		h.danger = 1
	}
	// Context bar: 3-row panel when chrome fits, else 1 bare row.
	if hasContext {
		if h.compact || width < 6 {
			h.context = 1
		} else {
			h.context = 3
		}
	}
	composerBorder := 0
	if !h.compact {
		composerBorder = 2
	}
	h.composer = max(1, composerRows) + composerBorder
	// Center gets everything left after fixed chrome.
	fixed := h.header + h.context + h.notice + h.popup + h.hints + h.danger
	h.center = max(0, height-fixed)
	// Ensure center can hold the prompt; shrink context/notice/popup under pressure.
	shortfall := max(0, h.composer-h.center)
	reduce := func(region *int, floor int) {
		if shortfall == 0 {
			return
		}
		delta := min(shortfall, max(0, *region-floor))
		*region -= delta
		shortfall -= delta
		h.center += delta
	}
	reduce(&h.popup, 0)
	reduce(&h.notice, 0)
	reduce(&h.context, 0)
	reduce(&h.hints, 0)
	reduce(&h.header, 0)
	reduce(&h.danger, 0)
	if h.center < h.composer {
		h.composer = h.center
	}
	return h
}

// renderHomeFrame paints the pre-first-prompt home screen (#677).
func (m Model) renderHomeFrame() string {
	width, height := m.width, m.height
	compact := width < compactWidth || height < compactHeight
	promptW := homePromptWidth(width)
	composerRows := m.homeComposerRows(promptW, compact)
	popupHeight := 0
	if m.completion != nil && m.modal == nil {
		// Budget the popup as part of the centered logo/prompt band.
		n := len(m.completion.Candidates)
		if n == 0 && m.completion.emptyHint != "" {
			n = 1
		}
		borderRows := 0
		if !compact && promptW >= ui.ChromeMinOuter(m.th) {
			borderRows = 2
		}
		popupHeight = min(completionMaxRows, n) + borderRows
		if n == 0 {
			popupHeight = 0
		}
	}
	noticeRows := m.noticeRowsFor(width)
	hl := computeHomeLayout(width, height, composerRows, popupHeight, m.showDangerBanner(), noticeRows, true)

	parts := make([]string, 0, 8)
	if hl.header > 0 {
		parts = append(parts, m.headerView(width))
	}
	if hl.context > 0 {
		bar := m.homeContextBar(width)
		// Fit to budgeted rows.
		rows := strings.Split(bar, "\n")
		if len(rows) > hl.context {
			rows = rows[:hl.context]
		}
		for len(rows) < hl.context {
			rows = append(rows, themedSpace(width))
		}
		parts = append(parts, strings.Join(rows, "\n"))
	}
	if hl.notice > 0 {
		parts = append(parts, m.noticeView(width, hl.notice))
	}
	popup := ""
	if hl.popup > 0 && m.completion != nil && m.modal == nil {
		popup = m.completion.view(promptW, hl.popup, m.th)
	}
	if hl.center+hl.popup > 0 {
		parts = append(parts, m.homeCenterBand(width, hl.center+hl.popup, hl.composer, hl.compact, popup, hl.popup))
	}
	if hl.hints > 0 {
		parts = append(parts, m.hintsView(width))
	}
	if hl.danger > 0 {
		if w := m.dangerView(width); w != "" {
			parts = append(parts, w)
		}
	}
	content := strings.Join(parts, "\n")
	contentHeight := hl.header + hl.context + hl.notice + hl.popup + hl.center
	if m.modal != nil {
		var overlay string
		if sm, ok := m.modal.(interface{ setHostSize(int, int) }); ok {
			sm.setHostSize(width, contentHeight)
			overlay = m.modal.view(largeModalOuterWidth(width), m.th)
		} else {
			overlay = m.modal.view(max(8, ui.ModalWidth(width)), m.th)
		}
		// Rebuild content without footer for overlay host.
		hostParts := parts
		// Strip footer rows (hints + danger) from overlay host.
		nFooter := 0
		if hl.hints > 0 {
			nFooter++
		}
		if hl.danger > 0 {
			nFooter++
		}
		if nFooter > 0 && len(hostParts) >= nFooter {
			hostParts = hostParts[:len(hostParts)-nFooter]
		}
		host := strings.Join(hostParts, "\n")
		host = ui.OverlayCenter(m.th, host, overlay, width, contentHeight)
		footerParts := []string{}
		if hl.hints > 0 {
			footerParts = append(footerParts, m.hintsView(width))
		}
		if hl.danger > 0 {
			if w := m.dangerView(width); w != "" {
				footerParts = append(footerParts, w)
			}
		}
		if len(footerParts) > 0 {
			host += "\n" + strings.Join(footerParts, "\n")
		}
		content = host
	}
	return ui.Canvas(m.th, width, height, content)
}
