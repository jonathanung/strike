package tui

import (
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	lg "charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/charmbracelet/lipgloss"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// localWorkingSpinnerFPS is a mild ~4 FPS local animation — below MiniDot's
// 10 FPS so Working chrome stays inside the coalesce budget (#497).
const localWorkingSpinnerFPS = time.Second / 4

// staticWorkingChrome prefers a non-ticking working glyph. SSH sessions and
// STRIKE_WORKING_CHROME=static opt in; STRIKE_WORKING_CHROME=animate forces
// ticks even over SSH (for local debugging of remote-looking envs).
func staticWorkingChrome() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("STRIKE_WORKING_CHROME"))) {
	case "static", "0", "off", "false":
		return true
	case "animate", "1", "on", "true":
		return false
	}
	// OpenSSH sets these on the remote side of an interactive session.
	return os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != ""
}

// adaptiveStyle builds a lipgloss v2 style from a v1 AdaptiveColor token so
// bubbles v2 (lipgloss v2) can be themed without migrating the whole theme
// package (E13.2).
func adaptiveStyle(c lipgloss.AdaptiveColor) lg.Style {
	return lg.NewStyle().Foreground(compat.AdaptiveColor{
		Light: lg.Color(c.Light),
		Dark:  lg.Color(c.Dark),
	})
}

func newComposer(th theme.Theme) textarea.Model {
	ta := textarea.New()
	ta.Placeholder = "Ask anything…  (/ commands, ! shell)"
	ta.MaxHeight = composerMaxHeight
	ta.SetHeight(composerMinHeight)
	ta.ShowLineNumbers = false
	ta.SetVirtualCursor(true)
	styleComposer(&ta, th)
	ta.Focus()
	return ta
}

// styleComposer applies resolved theme tokens to an existing textarea so
// appearance toggles can restyle without dropping value or cursor state.
func styleComposer(ta *textarea.Model, th theme.Theme) {
	th = th.Resolve()
	styles := textarea.DefaultDarkStyles()
	input := adaptiveStyle(th.Text)
	prompt := adaptiveStyle(th.Accent)
	placeholder := adaptiveStyle(th.TextMuted)
	styles.Focused = textarea.StyleState{
		Base:             input,
		Text:             input,
		CursorLine:       input,
		Placeholder:      placeholder,
		Prompt:           prompt,
		LineNumber:       placeholder,
		CursorLineNumber: prompt,
		EndOfBuffer:      placeholder,
	}
	styles.Blurred = styles.Focused
	styles.Cursor.Color = compat.AdaptiveColor{Light: lg.Color(th.Accent.Light), Dark: lg.Color(th.Accent.Dark)}
	styles.Cursor.Blink = false
	ta.SetStyles(styles)
	ta.Prompt = th.Icons.Prompt + themedSpace(th.Spacing.XS)
}

func newTextInput(th theme.Theme, placeholder string) textinput.Model {
	th = th.Resolve()
	in := textinput.New()
	in.Placeholder = placeholder
	in.Prompt = th.Icons.InputCursor + themedSpace(th.Spacing.XS)
	in.SetVirtualCursor(true)
	styles := textinput.DefaultDarkStyles()
	input := adaptiveStyle(th.Text)
	prompt := adaptiveStyle(th.Accent)
	ph := adaptiveStyle(th.TextMuted)
	styles.Focused = textinput.StyleState{
		Prompt:      prompt,
		Text:        input,
		Placeholder: ph,
	}
	styles.Blurred = styles.Focused
	styles.Cursor.Color = compat.AdaptiveColor{Light: lg.Color(th.Accent.Light), Dark: lg.Color(th.Accent.Dark)}
	styles.Cursor.Blink = false
	in.SetStyles(styles)
	return in
}

func newSpinner(th theme.Theme) spinner.Model {
	sp := spinner.New()
	styleSpinner(&sp, th)
	return sp
}

// styleSpinner reapplies theme spinner tokens after an appearance change.
// SSH/static mode uses a single Dot frame and never arms ticks (#497).
func styleSpinner(sp *spinner.Model, th theme.Theme) {
	th = th.Resolve()
	if staticWorkingChrome() {
		sp.Spinner = spinner.Spinner{Frames: []string{th.Icons.Dot}, FPS: time.Hour}
	} else {
		sp.Spinner = spinner.Spinner{
			Frames: []string{th.Icons.Dot, th.Icons.Cursor},
			FPS:    localWorkingSpinnerFPS,
		}
	}
	sp.Style = adaptiveStyle(th.AccentAlt)
}

// restyleWidgets reapplies theme tokens to composer and spinner after an
// appearance change (adaptive colors resolve differently under a new bg).
func (m *Model) restyleWidgets() {
	styleComposer(&m.composer, m.th)
	styleSpinner(&m.spin, m.th)
}
