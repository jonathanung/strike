package tui

import (
	"errors"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// errNoSettings is reported by the ctrl+d save path when no settings service
// is wired (a degraded frontend built without host.Services.Settings).
var errNoSettings = errors.New("saving defaults is unavailable")

// errNoCatalog is reported by the model picker when no catalog service is
// wired (a degraded frontend built without host.Services.Catalog).
var errNoCatalog = errors.New("model catalog is unavailable")

// saveDefaultsThroughCmd persists provider/model/agent defaults through the
// given settings service and reports the outcome as a defaultsSavedMsg. A nil
// service degrades to a graceful failure rather than a panic. Both the model's
// ctrl+d path and the picker modals share it.
func saveDefaultsThroughCmd(settings host.Settings, provider, model, agent, text string) tea.Cmd {
	return func() tea.Msg {
		if settings == nil {
			return defaultsSavedMsg{text: text, err: errNoSettings}
		}
		return defaultsSavedMsg{text: text, err: settings.SaveDefaults(provider, model, agent)}
	}
}

// headerView is the one-line header strip: the compact wordmark, the current
// provider/model and agent badges on the left, and the turn-running status on
// the right.
func (m Model) headerView(width int) string {
	ic := m.themeIcons()
	st := m.th.S()

	left := ui.LogoCompact(m.th)
	if m.providerName == "" {
		left += "  " + ui.Badge(m.th, ui.ToneMuted, "no model")
	} else {
		model := m.modelName
		if model == "" {
			model = "default"
		}
		left += "  " + ui.Badge(m.th, ui.ToneAccent, m.providerName+"/"+model)
	}
	// Same display-safety gate as the palette and welcome card: agents are
	// not host-filtered, so every render site guards the name itself.
	if m.agentName != "" && validAgentName(m.agentName) {
		left += " " + ui.Badge(m.th, ui.ToneAccentAlt, ic.Agent+" "+sanitizeDisplayData(m.agentName))
	}

	right := ""
	if m.turnRunning {
		right = m.spin.View() + " " + st.Warning.Render("working — esc interrupts")
	}
	return ui.StatusBar(m.th, max(1, width), left, right)
}

// transcriptView renders the transcript region: the scrolling viewport wrapped
// in a titled panel (or bare in compact mode). The title reads "welcome" while
// the transcript is empty (the welcome dashboard is showing) and "session"
// once cells stream in.
func (m Model) transcriptView(compact bool, height int) string {
	body := m.viewport.View()
	if compact {
		return body
	}
	title := "session"
	if len(m.cells) == 0 {
		title = "welcome"
	}
	return ui.Panel(m.th, ui.PanelOpts{
		Title:  title,
		Footer: m.transcriptFooter(),
		Width:  m.width,
		Height: height,
	}, body)
}

// transcriptFooter shows a scroll indicator when the transcript overflows its
// viewport, and nothing otherwise.
func (m Model) transcriptFooter() string {
	if m.viewport.Height <= 0 || m.viewport.TotalLineCount() <= m.viewport.Height {
		return ""
	}
	return strconv.Itoa(int(m.viewport.ScrollPercent()*100)) + "% · pgup/pgdn"
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
func (m Model) composerView(compact bool) string {
	if compact {
		return m.composer.View()
	}
	return ui.Panel(m.th, ui.PanelOpts{
		Title:   "prompt " + m.themeIcons().Prompt,
		Width:   m.width,
		Focused: true,
	}, m.composer.View())
}

// hintsView is the keybinding footer. ui.KeyHints drops whole hints that do
// not fit rather than cutting mid-hint.
func (m Model) hintsView(width int) string {
	return ui.KeyHints(m.th, max(1, width), []ui.KeyHint{
		{Key: "enter", Label: "send"},
		{Key: "alt+enter", Label: "newline"},
		{Key: "ctrl+k", Label: "palette"},
		{Key: "tab", Label: "agent"},
		{Key: "ctrl+d", Label: "save defaults"},
		{Key: "esc", Label: "interrupt"},
		{Key: "pgup/pgdn", Label: "scroll"},
	})
}

// dangerView is the permissions-bypassed banner, shown only under
// --dangerously-skip-permissions. A non-positive width (pre-ready) renders the
// full text unclamped so the warning is never lost at startup.
func (m Model) dangerView(width int) string {
	if !m.dangerouslySkipPermissions {
		return ""
	}
	style := m.th.S().Error.Bold(true)
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
