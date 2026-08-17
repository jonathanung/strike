package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// errNoSettings is reported by the ctrl+d save path when no settings service
// is wired (a degraded frontend built without host.Services.Settings).
var errNoSettings = errors.New("saving defaults is unavailable")

// errNoCatalog is reported by the model picker when no catalog service is
// wired (a degraded frontend built without host.Services.Catalog).
var errNoCatalog = errors.New("model catalog is unavailable")

// saveDefaultsThroughCmd persists provider/model/agent/effort/mode defaults
// through the given settings service and reports the outcome as a
// defaultsSavedMsg. A nil service degrades to a graceful failure rather than a
// panic. Both the model's ctrl+d path and the picker modals share it.
func saveDefaultsThroughCmd(settings host.Settings, provider, model, agent, effort, mode, text string) tea.Cmd {
	return func() tea.Msg {
		if settings == nil {
			return defaultsSavedMsg{text: text, err: errNoSettings}
		}
		return defaultsSavedMsg{text: text, err: settings.SaveDefaults(provider, model, agent, effort, mode)}
	}
}

// headerChip is one budgeted header badge. Lower prio drops first under width
// pressure (Family quiet hierarchy: drop think → effort → phase → health-dot
// before core status chips).
type headerChip struct {
	prio int
	view string // rendered badge only (no leading gap)
}

// headerView is the one-line header strip: the compact wordmark, a shortened
// workdir path, provider/model and agent badges on the left, and dynamic
// agent-state status on the right. Status chrome tints from theme tokens via
// agentState. Under width pressure badges drop lowest-priority first.
func (m Model) headerView(width int) string {
	th := m.th.Resolve()
	ic := iconsFor(th)
	badgeGap := themedSpace(th.Spacing.SM)
	inlineGap := themedSpace(th.Spacing.XS)
	state := m.agentState()
	stateTone := agentStateTone(state)

	brand := LogoCompact(th)

	// Build ordered chips with drop priorities (lower = drop sooner).
	var chips []headerChip
	provider, model := m.chromeProviderModel()
	if provider == "" {
		chips = append(chips, headerChip{100, headerKicker(th, ui.ToneMuted, "no model")})
	} else {
		if model == "" {
			model = "default"
		}
		// Values stay mixed-case; kickers (no model, FAST, YOLO) are uppercase.
		chips = append(chips, headerChip{100, toneStyle(th, ui.ToneAccent).Render(provider + "/" + model)})
		if tone, ok := providerHealthTone(m); ok {
			chips = append(chips, headerChip{40, toneStyle(th, tone).Render(ic.Dot)})
		}
	}
	// Same display-safety gate as the palette and welcome card: agents are
	// not host-filtered, so every render site guards the name itself.
	if m.agentName != "" && validAgentName(m.agentName) {
		chips = append(chips, headerChip{90, toneStyle(th, stateTone).Render(ic.Agent + inlineGap + sanitizeDisplayData(m.agentName))})
	}
	if m.phaseName != "" || m.phaseWorkflow != "" {
		label := "phase" + inlineGap
		if m.phaseWorkflow != "" && m.phaseName != "" {
			label += sanitizeDisplayData(m.phaseWorkflow) + "/" + sanitizeDisplayData(m.phaseName)
		} else if m.phaseName != "" {
			label += sanitizeDisplayData(m.phaseName)
		} else {
			label += sanitizeDisplayData(m.phaseWorkflow)
		}
		if m.phaseGate != "" {
			label += inlineGap + sanitizeDisplayData(m.phaseGate)
		}
		tone := ui.ToneAccentAlt
		if m.phaseStatus != "" {
			// Resume recovery: fail-closed, no phase grants applied.
			tone = ui.ToneWarning
			label += inlineGap + sanitizeDisplayData(m.phaseStatus)
		}
		chips = append(chips, headerChip{30, toneStyle(th, tone).Render(label)})
	}
	if m.effort != protocol.EffortDefault {
		chips = append(chips, headerChip{20, headerKicker(th, ui.ToneMuted, "effort"+inlineGap+string(m.effort))})
	}
	// Normal posture stays out of the header; exceptional autonomy and permission
	// modes remain prominent because they change how the agent may act.
	if m.autonomy.Normalize() != protocol.AutonomySupervised {
		chips = append(chips, headerChip{85, headerKicker(th, ui.ToneAccentAlt, "auto"+inlineGap+m.autonomy.Short())})
	}
	if m.permMode.Normalize() != protocol.PermissionModeDefault {
		chips = append(chips, headerChip{85, headerKicker(th, permissionModeBadgeTone(m.permMode), m.permMode.Short())})
	}
	// Isolation ladder badge (E12.7): state the posture; do not grade it.
	if iso := m.isolationLabel(); iso != "" {
		chips = append(chips, headerChip{84, toneStyle(th, ui.ToneMuted).Render(iso)})
	}
	if secs := m.effectivePermissionAutoApproveSeconds(); secs > 0 {
		chips = append(chips, headerChip{80, headerKicker(th, ui.ToneWarning, "auto-allow"+inlineGap+itoa(secs)+"s")})
	}
	if label := m.pendingBlockingLabel(); label != "" {
		chips = append(chips, headerChip{80, headerKicker(th, ui.ToneWarning, label)})
	}
	if m.fastEnabled {
		chips = append(chips, headerChip{80, headerKicker(th, ui.ToneWarning, "fast")})
	}
	if m.showThinking {
		chips = append(chips, headerChip{10, headerKicker(th, ui.ToneMuted, "think")})
	}
	// Session cost envelope chip at 50/80/100% (#577).
	if chip, ok := sessionBudgetHeaderChip(th, m); ok {
		chips = append(chips, chip)
	}
	// Claim vs verified chip when independent gates last ran (#809 / #806).
	if chip, ok := verificationHeaderChip(th, m.lastVerification); ok {
		chips = append(chips, chip)
	}

	statusStyle := th.AgentStateStyle(state)
	var right string
	switch state {
	case theme.AgentStateWorking:
		if m.verifying && !m.turnRunning {
			right = m.spin.View() + inlineGap + statusStyle.Render(detailJoin(th, strings.ToUpper("verifying"), "gates"))
		} else {
			right = m.spin.View() + inlineGap + statusStyle.Render(m.workingStatusLabel(th))
		}
	case theme.AgentStateAttention:
		right = statusStyle.Render(detailJoin(th, strings.ToUpper(state.Label()), "respond to prompt"))
	case theme.AgentStateError:
		right = statusStyle.Render(strings.ToUpper(state.Label()))
	default:
		// Sticky canceled chrome after interrupt until the next turn (#809).
		if m.lastStopReason == "interrupted" {
			right = th.S().Warning.Render("CANCELED")
		} else {
			right = statusStyle.Render(strings.ToUpper(state.Label()))
		}
	}

	// Fit badges into width after brand + right + StatusBar mid gap.
	// Reserve a little room so path/meter can still appear when space allows.
	fixed := lipgloss.Width(brand) + lipgloss.Width(right) + 1
	badgeBudget := max(0, width-fixed)
	badges := fitHeaderChips(chips, badgeBudget, badgeGap, inlineGap)

	// Path sits between brand and badges when free cells remain.
	left := brand
	pathBudget := width - lipgloss.Width(brand) - lipgloss.Width(badges) - lipgloss.Width(right) - 1 - th.Spacing.XS
	if path := m.headerWorkDirLabel(th, pathBudget); path != "" {
		left += inlineGap + path
	}
	left += badges

	// Meter only when left chrome + right status leave room.
	meterBudget := width - lipgloss.Width(left) - lipgloss.Width(right) - 1 - th.Spacing.XS
	if meter := m.headerContextMeter(th, meterBudget); meter != "" {
		left += inlineGap + meter
	}
	return ui.StatusBar(m.th, max(1, width), left, right)
}

// fitHeaderChips joins chips with gaps, dropping lowest-priority chips first
// until the rendered string fits budget display cells.
func fitHeaderChips(chips []headerChip, budget int, firstGap, restGap string) string {
	if budget < 1 || len(chips) == 0 {
		return ""
	}
	active := append([]headerChip(nil), chips...)
	for len(active) > 0 {
		out := joinHeaderChips(active, firstGap, restGap)
		if lipgloss.Width(out) <= budget {
			return out
		}
		// Drop lowest priority (stable: first among ties).
		drop := 0
		for i := 1; i < len(active); i++ {
			if active[i].prio < active[drop].prio {
				drop = i
			}
		}
		active = append(active[:drop], active[drop+1:]...)
	}
	return ""
}

func joinHeaderChips(chips []headerChip, firstGap, restGap string) string {
	if len(chips) == 0 {
		return ""
	}
	var b strings.Builder
	for i, c := range chips {
		if i == 0 {
			b.WriteString(firstGap)
		} else {
			b.WriteString(restGap)
		}
		b.WriteString(c.view)
	}
	return b.String()
}

// headerWorkDirLabel is the muted, home-shortened cwd for the header brand row.
// budget is free display cells; below minBudget the path is omitted so badges
// and status stay intact. Long paths middle-ellipsis to keep the leaf segment.
func (m Model) headerWorkDirLabel(th theme.Theme, budget int) string {
	const minBudget = 6
	if budget < minBudget {
		return ""
	}
	raw := strings.TrimSpace(m.workDir)
	if raw == "" {
		return ""
	}
	th = th.Resolve()
	label := shortenHomePath(sanitizeDisplayData(raw))
	if label == "" {
		return ""
	}
	label = truncateMiddle(label, budget, th.Icons.Ellipsis)
	if label == "" {
		return ""
	}
	return th.S().Muted.Render(label)
}

// shortenHomePath replaces a leading home directory with ~ for compact display.
// Non-home absolute paths and relative paths are cleaned and returned as-is.
func shortenHomePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	clean := filepath.Clean(path)
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return clean
	}
	home = filepath.Clean(home)
	if clean == home {
		return "~"
	}
	prefix := home + string(os.PathSeparator)
	if strings.HasPrefix(clean, prefix) {
		return "~" + clean[len(home):]
	}
	return clean
}

// workingStatusLabel is the header right-side text while a turn runs, e.g.
// "working (12s · 3 tool calls) — esc". Elapsed time is always included so
// long silent waits stay obvious even before tools or answer text arrive.
func (m Model) workingStatusLabel(th theme.Theme) string {
	th = th.Resolve()
	elapsed := time.Duration(0)
	if !m.turnStartedAt.IsZero() {
		elapsed = time.Since(m.turnStartedAt)
	}
	parts := []string{formatCompactDuration(elapsed)}
	switch {
	case m.toolCallsThisTurn == 1:
		parts = append(parts, "1 tool call")
	case m.toolCallsThisTurn > 1:
		parts = append(parts, fmt.Sprintf("%d tool calls", m.toolCallsThisTurn))
	}
	inner := dotJoin(th, parts...)
	return detailJoin(th, strings.ToUpper(theme.AgentStateWorking.Label())+" ("+inner+")", "esc")
}

// transcriptView renders the empty dashboard directly; populated transcripts
// use a kicker row plus left-accent messages (no boxed session tile).
func (m Model) transcriptView(compact bool, width, height int) string {
	if height <= 0 {
		return ""
	}
	if len(m.displayCells()) == 0 {
		if m.replayLoading() {
			body := m.th.Resolve().S().Muted.Render("loading session…")
			if compact {
				return body
			}
			return m.transcriptChrome(width, height, body)
		}
		if m.viewingChild() {
			// Empty subagent log still shows a kicker (not the root welcome card).
			// Distinguish live children so a brief empty poll is not alarming.
			msg := "subagent transcript empty"
			if m.childIsRunning(m.viewingID) {
				msg = "subagent running…"
			}
			body := m.th.Resolve().S().Muted.Render(msg)
			if compact {
				return body
			}
			return m.transcriptChrome(width, height, body)
		}
		// Past-onboarding (spawned root or multi-pane): show a simple
		// placeholder instead of the full welcome dashboard. The welcome
		// cards are only relevant during first-run onboarding (#1092).
		if !m.firstRun {
			label := shortSessionID(m.sessionID)
			if label == "" {
				label = "session"
			}
			body := m.th.Resolve().S().Muted.Render("new agent " + label)
			if compact {
				return body
			}
			return m.transcriptChrome(width, height, body)
		}
		return m.welcomeView(width, height)
	}
	body := m.viewport.View()
	if compact {
		return body
	}
	return m.transcriptChrome(width, height, body)
}

// transcriptChrome is the session kicker plus body — web .message hierarchy,
// not a ┌┐ panel.
func (m Model) transcriptChrome(width, height int, body string) string {
	if height <= 0 {
		return ""
	}
	th := m.th.Resolve()
	titleText := m.sessionPanelTitle()
	title := th.S().Muted.Render(titleText)
	switch strings.ToLower(strings.TrimSpace(titleText)) {
	case "session", "subagent":
		title = kicker(th.S().Muted, titleText)
	}
	footer := m.transcriptFooter()
	head := ui.StatusBar(th, max(1, width), title, footer)
	if height == 1 {
		return head
	}
	bodyRows := strings.Split(body, "\n")
	if body == "" {
		bodyRows = nil
	}
	need := height - 1
	if len(bodyRows) > need {
		bodyRows = bodyRows[:need]
	}
	for len(bodyRows) < need {
		bodyRows = append(bodyRows, themedSpace(width))
	}
	for i := range bodyRows {
		bodyRows[i] = padInspectorLine(th, bodyRows[i], width)
	}
	return head + "\n" + strings.Join(bodyRows, "\n")
}

// sessionPanelTitle is the transcript chrome label: auto-title when set.
// While viewing a subagent, the child title is shown with a marker.
func (m Model) sessionPanelTitle() string {
	if m.viewingChild() {
		title := strings.TrimSpace(m.viewTitle)
		if title == "" {
			title = "subagent"
		}
		return sanitizeTitleTopic(title)
	}
	if topic := strings.TrimSpace(m.titleTopic); topic != "" {
		return sanitizeTitleTopic(topic)
	}
	return "session"
}

// transcriptFooter shows a scroll indicator when the transcript overflows its
// viewport, and nothing otherwise.
func (m Model) transcriptFooter() string {
	if m.viewport.Height() <= 0 || m.viewport.TotalLineCount() <= m.viewport.Height() {
		return ""
	}
	return dotJoin(m.th, strconv.Itoa(int(m.viewport.ScrollPercent()*100))+"%", "pgup/pgdn", keyHint(m.keyMap.JumpBottom).Key)
}

// maxNoticeRows caps how many layout rows a wrapped notice may occupy so the
// transcript keeps a usable share of the screen.
const maxNoticeRows = 5

// noticeView renders the reserved feedback region: errors in the error tone,
// everything else as informational. Text wraps up to maxRows lines. An empty
// notice yields "" (the layout still reserves a blank row when budgeted).
func (m Model) noticeView(width, maxRows int) string {
	level := ui.LevelInfo
	if m.noticeErr {
		level = ui.LevelError
	}
	if maxRows < 1 {
		maxRows = 1
	}
	return ui.NoticeLines(m.th, level, m.notice, max(1, width), maxRows)
}

// noticeRowsFor returns the layout height for the current notice at width:
// 0 when empty (blank reservation), else the wrapped line count capped at
// maxNoticeRows.
func (m Model) noticeRowsFor(width int) int {
	if m.notice == "" {
		return 0
	}
	out := m.noticeView(width, maxNoticeRows)
	if out == "" {
		return 0
	}
	return strings.Count(out, "\n") + 1
}

// composerInputMode is the live input mode for chrome: chat, shell (!), or
// command (/). Empty drafts default to chat (#678).
func (m Model) composerInputMode() string {
	v := strings.TrimLeft(m.composer.Value(), " \t")
	switch {
	case strings.HasPrefix(v, "!"):
		return "shell"
	case strings.HasPrefix(v, "/"):
		return "command"
	default:
		return "chat"
	}
}

// composerView renders the focused composer: the textarea inside a titled
// panel, or bare in compact mode. Title carries mode, prompt glyph, queue /
// attachment / pending-approval chips so the box reads as the main input (#678).
func (m Model) composerView(compact bool, width, height int) string {
	if height <= 0 {
		return ""
	}
	composer := m.composer
	visualFocus := composer.Focused() && m.modal == nil && m.focus == focusLeft
	composer.Focus()
	composer, _ = composer.Update(nil)

	borderless := compact || height < 3
	contentHeight := height
	if !borderless {
		contentHeight = ui.PanelInnerHeightFor(m.th, width, height)
	}
	composer.SetHeight(contentHeight)
	// Stronger prompt marker when focused (#678).
	th := m.th.Resolve()
	if visualFocus {
		styles := composer.Styles()
		styles.Focused.Prompt = th.S().AccentStrong
		styles.Blurred.Prompt = th.S().AccentStrong
		composer.SetStyles(styles)
		composer.Prompt = th.Icons.Prompt + themedSpace(th.Spacing.XS)
	}
	composer.View()
	composer, _ = composer.Update(nil)
	if !visualFocus {
		composer.Blur()
	}
	var footer string
	if !borderless {
		// Composer chrome footer stays minimal; global footer is context-sensitive (#679).
		footer = composerFooter(m.th, m.keyMap, ui.PanelInnerWidth(m.th, width), len(m.inputQueue) > 0 && m.composer.Value() == "")
	}
	title := m.composerTitle(th, visualFocus)
	focused := m.focus == focusLeft && m.modal == nil
	return ui.Panel(m.th, ui.PanelOpts{
		Title:      title,
		Footer:     footer,
		Width:      width,
		Height:     height,
		Borderless: borderless,
		// Focused outline (BorderFocus) is the primary affordance; body stays
		// Surface so the textarea remains readable (#678).
		Focused: focused,
		Dim:     m.focus == focusRight || m.modal != nil,
	}, composer.View())
}

// composerTitle is the web .composer label: muted uppercase INSTRUCTION
// kicker, with mode/status chips after it.
func (m Model) composerTitle(th theme.Theme, focused bool) string {
	th = th.Resolve()
	st := th.S()
	labelStyle := st.Muted
	if focused {
		labelStyle = st.Accent
	}
	title := kicker(labelStyle, "instruction")
	if mode := m.composerInputMode(); mode != "chat" {
		title += themedSpace(th.Spacing.SM) + kicker(st.Muted, mode)
	}
	// Send-state chip when draft is non-empty and left-focused.
	if focused && strings.TrimSpace(m.composer.Value()) != "" && m.modal == nil {
		label := "ready"
		if m.turnRunning {
			label = "queue"
		}
		title += themedSpace(th.Spacing.SM) + headerKicker(th, ui.ToneSuccess, label)
	}
	if n := len(m.pendingImages); n > 0 {
		title += themedSpace(th.Spacing.SM) + headerKicker(th, ui.ToneAccentAlt, itoa(n)+" img")
	}
	if badge := m.inputQueueBadge(); badge != "" {
		title += themedSpace(th.Spacing.SM) + badge
	}
	if label := m.pendingBlockingLabel(); label != "" {
		title += themedSpace(th.Spacing.SM) + headerKicker(th, ui.ToneWarning, label)
	}
	return title
}

// composerFooter advertises send/newline when the panel has room for a footer.
// When queueEdit is set, also advertise backspace to pop the last queued prompt
// and /queue for the full browser.
// width is the panel footer budget (PanelInnerWidth); always a single line.
func composerFooter(th theme.Theme, keys keyMap, width int, queueEdit bool) string {
	send, nl := keyHint(keys.Send), keyHint(keys.Newline)
	hints := []ui.KeyHint{send, nl}
	if queueEdit {
		hints = append(hints,
			ui.KeyHint{Key: "bksp", Label: "pop last"},
			ui.KeyHint{Key: "/queue", Label: "manage"},
		)
	}
	return ui.KeyHints(th, max(1, width), hints)
}

// rightPaneView frames the active window, or a stacked group of related panes
// when space allows. Context and activity bodies are Model-driven; other
// windows render their own content. The app owns shared chrome and focus.
func (m Model) rightPaneView(width, height int, compact bool) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	g := m.windows.activeGroup()
	pairHorizontal := m.splitOrientation == orientVertical
	stackGutter := m.th.Resolve().Spacing.XS
	pref := m.memberPreferredSizes(g, width, height, compact, pairHorizontal)
	slots := computeMemberSlots(width, height, stackGutter, len(g.members), pairHorizontal, pref)
	if compact || len(slots) == 0 || len(g.members) < 2 {
		return m.rightPaneSingle(width, height, compact, m.windows.active())
	}
	parts := make([]string, 0, len(g.members)*2-1)
	activeIdx := 0
	if len(m.windows.windows) > 0 {
		activeIdx = m.windows.index % len(m.windows.windows)
	}
	for i, wi := range g.members {
		if i > 0 {
			if pairHorizontal {
				parts = append(parts, paneGutter(m.th, stackGutter, height))
			} else {
				parts = append(parts, paneGutter(m.th, width, stackGutter))
			}
		}
		var w window
		if wi >= 0 && wi < len(m.windows.windows) {
			w = m.windows.windows[wi]
		}
		slot := slots[i]
		focused := m.focus == focusRight && m.modal == nil && wi == activeIdx
		dim := !focused
		parts = append(parts, m.rightPaneSingle(slot.width, slot.height, compact, w, focused, dim))
	}
	if pairHorizontal {
		return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// rightPaneSingle frames one window. optionalFocus overrides (focused, dim);
// when omitted, focus follows the right pane aggregate.
func (m Model) rightPaneSingle(width, height int, compact bool, active window, focusOverride ...bool) string {
	var title, body, footer string
	focused := m.focus == focusRight && m.modal == nil
	dim := m.focus == focusLeft || m.modal != nil
	if len(focusOverride) >= 2 {
		focused, dim = focusOverride[0], focusOverride[1]
	}
	if active != nil {
		title = active.title()
		// Agents concurrent-root controls only when this pane is the agents tree.
		// Always paint the footer (focused or dim) so pane keybinds stay visible
		// out of focus; KeyHints keeps the chrome row single-line.
		if !compact && active.id() == agentsWindowID {
			footer = agentsPaneFooter(m.th, inspectorInnerWidth(m.th, width))
		}
		if !compact && active.id() == queueWindowID {
			footer = queuePaneFooter(m.th, inspectorInnerWidth(m.th, width))
		}
		innerW, innerH := width, height
		hasFooter := footer != ""
		if nw, ok := active.(namedWindow); ok {
			if nw.width > 0 {
				innerW = nw.width
			} else {
				innerW = inspectorInnerWidth(m.th, width)
			}
			if nw.height > 0 {
				innerH = nw.height
			} else {
				innerH = inspectorInnerHeight(height, hasFooter)
			}
		} else {
			innerW = inspectorInnerWidth(m.th, width)
			innerH = inspectorInnerHeight(height, hasFooter)
		}
		if compact {
			innerW, innerH = width, height
		}
		// Prefer dimensions last applied by resize so stacked members match slots.
		switch active.id() {
		case "context":
			body = m.contextPaneBody(max(0, innerW), max(0, innerH))
		case "activity":
			body = m.activityPaneBody(max(0, innerW), max(0, innerH))
		case queueWindowID:
			body = m.queuePaneBody(max(0, innerW), max(0, innerH))
		default:
			body = active.view(m.th)
		}
	}
	if compact {
		return ui.Panel(m.th, ui.PanelOpts{
			Title:      title,
			Footer:     footer,
			Width:      max(0, width),
			Height:     max(0, height),
			Borderless: true,
			Focused:    focused,
			Dim:        dim,
		}, body)
	}
	return inspectorFrame(m.th, title, footer, body, max(0, width), max(0, height), focused, dim)
}

func paneGutter(th theme.Theme, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	row := themedSpace(width)
	return strings.TrimSuffix(strings.Repeat(row+"\n", height), "\n")
}

// hintsView is the context-sensitive keybinding footer (#679). Only controls
// relevant to the focused region are shown so the row stays scannable.
func (m Model) hintsView(width int) string {
	return ui.KeyHints(m.th, max(1, width), m.footerHints())
}

// footerHints selects the hint set for the current focus / home / modal state.
func (m Model) footerHints() []ui.KeyHint {
	// Modal owns the screen — keep a minimal escape hatch.
	if m.modal != nil {
		return []ui.KeyHint{
			{Key: "esc", Label: "close"},
			keyHint(m.keyMap.KeyHelp),
		}
	}
	// Home / left composer: send-oriented chrome.
	if m.showHomeLayout() || m.focus == focusLeft {
		hints := []ui.KeyHint{
			keyHint(m.keyMap.Send),
			keyHint(m.keyMap.Newline),
			keyHint(m.keyMap.ExternalEditor),
		}
		if m.turnRunning {
			hints = append(hints, keyHint(m.keyMap.Interrupt))
		} else {
			hints = append(hints, ui.KeyHint{Key: "esc", Label: "cancel"})
		}
		hints = append(hints,
			keyHint(m.keyMap.Palette),
			keyHint(m.keyMap.KeyHelp),
		)
		if m.showHomeLayout() {
			// Lean home: ctrl+l opens the right pane column (#684).
			hints = append(hints, keyHint(m.keyMap.FocusRight))
		} else {
			// Multi-pane left: scroll transcript + focus right.
			hints = append(hints,
				ui.KeyHint{Key: keyHint(m.keyMap.ScrollUp).Key + "/" + keyHint(m.keyMap.ScrollDown).Key, Label: "scroll"},
				keyHint(m.keyMap.FocusRight),
			)
		}
		return hints
	}
	// Right pane: navigation for the active window / group.
	hints := []ui.KeyHint{
		{Key: "↑↓", Label: "select"},
		{Key: "enter", Label: "open"},
		keyHint(m.keyMap.FocusLeft),
		{Key: keyHint(m.keyMap.CycleWindowNext).Key + "/" + keyHint(m.keyMap.CycleWindowPrev).Key, Label: "next pane"},
		keyHint(m.keyMap.Palette),
		keyHint(m.keyMap.KeyHelp),
	}
	return hints
}

func keyHint(binding key.Binding) ui.KeyHint {
	help := binding.Help()
	return ui.KeyHint{Key: help.Key, Label: help.Desc}
}

// showDangerBanner reports whether the bottom danger row should be reserved.
func (m Model) showDangerBanner() bool {
	return m.dangerouslySkipPermissions || m.permMode.Normalize() == protocol.PermissionModeYolo
}

// dangerView is the permissions-bypassed banner for --dangerously-skip-permissions
// or session yolo mode. A non-positive width (pre-ready) renders the full text
// unclamped so the warning is never lost at startup.
func (m Model) dangerView(width int) string {
	var text string
	switch {
	case m.dangerouslySkipPermissions:
		text = "DANGER: permissions bypassed"
	case m.permMode.Normalize() == protocol.PermissionModeYolo:
		text = "DANGER: yolo mode — permission asks skipped"
	default:
		return ""
	}
	style := m.th.S().DangerStrong
	if width > 0 {
		style = style.MaxWidth(width)
	}
	return style.Render(text)
}

// permissionModeBadgeTone maps posture to a status badge tone.
func permissionModeBadgeTone(mode protocol.PermissionMode) ui.Tone {
	switch mode.Normalize() {
	case protocol.PermissionModePlan:
		return ui.ToneAccentAlt
	case protocol.PermissionModeSoftApprove:
		return ui.ToneWarning
	case protocol.PermissionModeAcceptEdits:
		return ui.ToneWarning
	case protocol.PermissionModeYolo:
		return ui.ToneDanger
	default:
		return ui.ToneMuted
	}
}

// isolationLabel returns the E12.7 posture badge text (muted; not a grade).
func (m Model) isolationLabel() string {
	if m.isolation != "" {
		return protocol.IsolationShort(m.isolation)
	}
	// Fallback: env (container launch) then recompute from host dials.
	if p, ok := protocol.ParseIsolationEnv(os.Getenv(protocol.IsolationEnvKey)); ok {
		return protocol.IsolationShort(p)
	}
	return protocol.IsolationShort(protocol.ComputeIsolation(false, false, m.permMode, m.sandboxMode))
}

// headerContextMeter is the compact usage chip for the status bar: a short
// meter plus used/limit figures. Unknown sides render as the theme detail
// separator (—), never as measured zero. budget is the free cells between left
// badges and right status (after gaps); below minPair the chip is omitted,
// below minBarPair only the figures are shown, otherwise the bar scales down.
func (m Model) headerContextMeter(th theme.Theme, budget int) string {
	if !m.hasContextMeter() {
		return ""
	}
	const (
		minPair    = 8  // pair-only floor (e.g. "1.2k/200k")
		minBarPair = 14 // room for a short bar + gap + pair
		maxBarW    = 8
		minBarW    = 4
	)
	if budget < minPair {
		return ""
	}
	th = th.Resolve()
	dash := th.Icons.DetailSeparator
	pair := formatContextTokenPair(m.usageUsed, m.contextLimit, m.contextLimitKnown, dash)
	// Prefer Used; fall back to input+output sum only when Used is unknown but
	// both sides are known so the header still has a numerator.
	used := m.usageUsed
	if !used.Known && m.usageInput.Known && m.usageOutput.Known {
		used = protocol.KnownTokens(m.usageInput.N + m.usageOutput.N)
		pair = formatContextTokenPair(used, m.contextLimit, m.contextLimitKnown, dash)
	}
	ratio := contextUsageRatio(used, m.contextLimit, m.contextLimitKnown)
	pairStyled := th.S().Text.Render(pair)
	if budget < minBarPair {
		return pairStyled
	}
	// Scale bar into whatever remains after pair + gap; drop bar if too tight.
	barW := budget - lipgloss.Width(pairStyled) - th.Spacing.XS
	if barW < minBarW {
		return pairStyled
	}
	if barW > maxBarW {
		barW = maxBarW
	}
	return ui.Meter(th, barW, ratio) + themedSpace(th.Spacing.XS) + pairStyled
}

// themeIcons returns the model's glyph set, falling back to the defaults for a
// zero-value theme.
func (m Model) themeIcons() theme.Icons {
	return iconsFor(m.th)
}

// iconsFor returns a theme's glyph set, falling back to theme.DefaultIcons()
// for a zero-value theme so cells and views stay usable without a configured
// theme.
func iconsFor(th theme.Theme) theme.Icons {
	if th.Icons.Cursor == "" {
		return theme.DefaultIcons()
	}
	return th.Icons
}

// sessionBudgetHeaderChip renders a status-bar badge when the session cost
// envelope has crossed a warning threshold (#577).
func sessionBudgetHeaderChip(th theme.Theme, m Model) (headerChip, bool) {
	if !m.sessionBudgetKnown || m.sessionBudgetMaxUSD <= 0 {
		return headerChip{}, false
	}
	level := m.sessionBudgetLevel
	if level == "" {
		ratio := m.sessionBudgetCostUSD / m.sessionBudgetMaxUSD
		switch {
		case ratio >= 1.0 || m.sessionBudgetExhausted:
			level = protocol.SessionBudgetLevel100
		case ratio >= 0.80:
			level = protocol.SessionBudgetLevel80
		case ratio >= 0.50:
			level = protocol.SessionBudgetLevel50
		default:
			return headerChip{}, false
		}
	}
	tone := ui.ToneMuted
	label := "budget " + level + "%"
	switch level {
	case protocol.SessionBudgetLevel50:
		tone = ui.ToneWarning
	case protocol.SessionBudgetLevel80:
		tone = ui.ToneWarning
		label = "budget 80%"
	case protocol.SessionBudgetLevel100:
		tone = ui.ToneDanger
		if m.sessionBudgetExhausted {
			label = "budget max"
		} else {
			label = "budget 100%"
		}
	}
	// Prefer compact cost pair when space allows via badge text.
	pair := formatSessionCostUSD(m.sessionBudgetCostUSD) + "/" + formatSessionCostUSD(m.sessionBudgetMaxUSD)
	if level != "" {
		label = pair
		switch level {
		case protocol.SessionBudgetLevel50:
			tone = ui.ToneWarning
		case protocol.SessionBudgetLevel80:
			tone = ui.ToneWarning
		default:
			tone = ui.ToneDanger
		}
	}
	return headerChip{75, headerKicker(th, tone, label)}, true
}
