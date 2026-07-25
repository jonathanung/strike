package tui

import (
	"errors"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// errNoSettings is reported by the ctrl+d save path when no settings service
// is wired (a degraded frontend built without host.Services.Settings).
var errNoSettings = errors.New("saving defaults is unavailable")

// errNoCatalog is reported by the model picker when no catalog service is
// wired (a degraded frontend built without host.Services.Catalog).
var errNoCatalog = errors.New("model catalog is unavailable")

// saveDefaultsThroughCmd persists provider/model/agent/effort defaults through
// the given settings service and reports the outcome as a defaultsSavedMsg. A
// nil service degrades to a graceful failure rather than a panic. Both the
// model's ctrl+d path and the picker modals share it.
func saveDefaultsThroughCmd(settings host.Settings, provider, model, agent, effort, text string) tea.Cmd {
	return func() tea.Msg {
		if settings == nil {
			return defaultsSavedMsg{text: text, err: errNoSettings}
		}
		return defaultsSavedMsg{text: text, err: settings.SaveDefaults(provider, model, agent, effort)}
	}
}

// headerView is the one-line header strip: the compact wordmark, the current
// provider/model and agent badges on the left, and dynamic agent-state status
// on the right. Status chrome tints from theme tokens via agentState.
func (m Model) headerView(width int) string {
	th := m.th.Resolve()
	ic := iconsFor(th)
	badgeGap := themedSpace(th.Spacing.SM)
	inlineGap := themedSpace(th.Spacing.XS)
	state := m.agentState()
	stateTone := agentStateTone(state)

	left := ui.LogoCompact(th)
	if m.providerName == "" {
		left += badgeGap + ui.Badge(th, ui.ToneMuted, "no model")
	} else {
		model := m.modelName
		if model == "" {
			model = "default"
		}
		left += badgeGap + ui.Badge(th, ui.ToneAccent, m.providerName+"/"+model)
	}
	// Same display-safety gate as the palette and welcome card: agents are
	// not host-filtered, so every render site guards the name itself.
	// Agent badge tone follows live runtime state (tokenized coloring).
	if m.agentName != "" && validAgentName(m.agentName) {
		left += inlineGap + ui.Badge(th, stateTone, ic.Agent+inlineGap+sanitizeDisplayData(m.agentName))
	}
	// Only shown once a level is set — an unset dial means "whatever the
	// provider does by default", which is not worth a badge.
	if m.effort != protocol.EffortDefault {
		left += inlineGap + ui.Badge(th, ui.ToneMuted, "effort"+inlineGap+string(m.effort))
	}
	if m.fastEnabled {
		// Warning tone: priority tier is a cost-visible session preference.
		left += inlineGap + ui.Badge(th, ui.ToneWarning, "fast")
	}

	statusStyle := th.AgentStateStyle(state)
	var right string
	switch state {
	case theme.AgentStateWorking:
		right = m.spin.View() + inlineGap + statusStyle.Render(state.Label()+" — esc interrupts")
	case theme.AgentStateAttention:
		right = statusStyle.Render(state.Label() + " — respond to prompt")
	case theme.AgentStateError:
		right = statusStyle.Render(state.Label())
	default:
		right = statusStyle.Render(state.Label())
	}
	return ui.StatusBar(m.th, max(1, width), left, right)
}

// transcriptView renders the empty dashboard directly; populated transcripts
// retain their session panel.
func (m Model) transcriptView(compact bool, width, height int) string {
	if height <= 0 {
		return ""
	}
	if len(m.cells) == 0 {
		return m.welcomeView(width, height)
	}
	body := m.viewport.View()
	if compact {
		return body
	}
	return ui.Panel(m.th, ui.PanelOpts{
		Title:   "session",
		Footer:  m.transcriptFooter(),
		Width:   width,
		Height:  height,
		Focused: m.focus == focusLeft && m.modal == nil,
		Dim:     m.focus == focusRight || m.modal != nil,
	}, body)
}

// transcriptFooter shows a scroll indicator when the transcript overflows its
// viewport, and nothing otherwise.
func (m Model) transcriptFooter() string {
	if m.viewport.Height <= 0 || m.viewport.TotalLineCount() <= m.viewport.Height {
		return ""
	}
	return dotJoin(m.th, strconv.Itoa(int(m.viewport.ScrollPercent()*100))+"%", "pgup/pgdn")
}

// noticeView renders the reserved feedback row: errors in the error tone,
// everything else as an informational line. An empty notice yields a blank
// row, keeping the layout budget stable.
func (m Model) noticeView(width int) string {
	level := ui.LevelInfo
	if m.noticeErr {
		level = ui.LevelError
	}
	return ui.Notice(m.th, level, m.notice, max(1, width))
}

// composerView renders the focused composer: the textarea inside a titled
// panel, or bare in compact mode.
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
		contentHeight = ui.PanelInnerHeight(width, height)
	}
	composer.SetHeight(contentHeight)
	composer.View()
	composer, _ = composer.Update(nil)
	if !visualFocus {
		composer.Blur()
	}
	var footer string
	if !borderless {
		footer = composerFooter(m.th, width)
	}
	return ui.Panel(m.th, ui.PanelOpts{
		Title:      "prompt" + themedSpace(m.th.Resolve().Spacing.XS) + m.themeIcons().Prompt,
		Footer:     footer,
		Width:      width,
		Height:     height,
		Borderless: borderless,
		Focused:    m.focus == focusLeft && m.modal == nil,
		Dim:        m.focus == focusRight || m.modal != nil,
	}, composer.View())
}

// composerFooter advertises send/newline when the panel has room for a footer.
func composerFooter(th theme.Theme, width int) string {
	_ = width
	return dotJoin(th, "enter send", "shift+enter newline")
}

// rightPaneView frames the active window. Context and activity bodies are
// Model-driven; other windows render their own content. The app owns the
// shared pane chrome and focus state.
func (m Model) rightPaneView(width, height int, compact bool) string {
	var title, body string
	if active := m.windows.active(); active != nil {
		title = active.title()
		innerW, innerH := width, height
		if nw, ok := active.(namedWindow); ok {
			if nw.width > 0 {
				innerW = nw.width
			} else {
				innerW = ui.PanelInnerWidth(m.th, width)
			}
			if nw.height > 0 {
				innerH = nw.height
			} else {
				innerH = ui.PanelInnerHeight(width, height)
			}
		} else {
			innerW = ui.PanelInnerWidth(m.th, width)
			innerH = ui.PanelInnerHeight(width, height)
		}
		if compact {
			innerW, innerH = width, height
		}
		switch active.id() {
		case "context":
			body = m.contextPaneBody(max(0, innerW), max(0, innerH))
		case "activity":
			body = m.activityPaneBody(max(0, innerW), max(0, innerH))
		default:
			body = active.view(m.th)
		}
	}
	return ui.Panel(m.th, ui.PanelOpts{
		Title:      title,
		Width:      max(0, width),
		Height:     max(0, height),
		Borderless: compact,
		Focused:    m.focus == focusRight && m.modal == nil,
		Dim:        m.focus == focusLeft || m.modal != nil,
	}, body)
}

func paneGutter(th theme.Theme, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	row := themedSpace(width)
	return strings.TrimSuffix(strings.Repeat(row+"\n", height), "\n")
}

// hintsView is the keybinding footer. ui.KeyHints drops whole hints that do
// not fit rather than cutting mid-hint.
func (m Model) hintsView(width int) string {
	hints := []ui.KeyHint{
		{Key: keyHint(m.keyMap.FocusLeft).Key + "/" + keyHint(m.keyMap.FocusRight).Key, Label: "panes"},
		{Key: keyHint(m.keyMap.CycleWindowNext).Key + "/" + keyHint(m.keyMap.CycleWindowPrev).Key, Label: "windows"},
		keyHint(m.keyMap.Palette),
		keyHint(m.keyMap.KeyHelp),
		keyHint(m.keyMap.Interrupt),
	}
	if m.focus == focusLeft {
		hints = append(hints,
			keyHint(m.keyMap.Send),
			keyHint(m.keyMap.Newline),
			keyHint(m.keyMap.Agent),
			keyHint(m.keyMap.SaveDefaults),
			ui.KeyHint{Key: keyHint(m.keyMap.ScrollUp).Key + "/" + keyHint(m.keyMap.ScrollDown).Key, Label: "scroll"},
		)
	}
	return ui.KeyHints(m.th, max(1, width), hints)
}

func keyHint(binding key.Binding) ui.KeyHint {
	help := binding.Help()
	return ui.KeyHint{Key: help.Key, Label: help.Desc}
}

// dangerView is the permissions-bypassed banner, shown only under
// --dangerously-skip-permissions. A non-positive width (pre-ready) renders the
// full text unclamped so the warning is never lost at startup.
func (m Model) dangerView(width int) string {
	if !m.dangerouslySkipPermissions {
		return ""
	}
	style := m.th.S().DangerStrong
	if width > 0 {
		style = style.MaxWidth(width)
	}
	return style.Render("DANGER: permissions bypassed")
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
