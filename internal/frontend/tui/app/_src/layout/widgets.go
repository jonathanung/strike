package tui

import (
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
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

func newComposer(th theme.Theme) textarea.Model {
	ta := textarea.New()
	ta.Placeholder = "Ask anything…  (/ commands, ! shell)"
	ta.MaxHeight = composerMaxHeight
	ta.SetHeight(composerMinHeight)
	ta.ShowLineNumbers = false
	ta.SetVirtualCursor(true)
	// Bubbles v2 DefaultKeyMap binds enter/ctrl+m to InsertNewline. Strike
	// owns Enter as send and Newline as shift/alt+enter / ctrl+j — disable
	// the widget binding so a fall-through Update never inserts a newline.
	km := textarea.DefaultKeyMap()
	km.InsertNewline.SetEnabled(false)
	ta.KeyMap = km
	styleComposer(&ta, th)
	ta.Focus()
	return ta
}

// styleComposer applies resolved theme tokens to an existing textarea so
// appearance toggles can restyle without dropping value or cursor state.
// Styles come from theme.S() (the visual chokepoint) — no lipgloss.Foreground
// outside internal/frontend/tui/theme.
func styleComposer(ta *textarea.Model, th theme.Theme) {
	th = th.Resolve()
	st := th.S()
	styles := textarea.DefaultDarkStyles()
	styles.Focused = textarea.StyleState{
		Base:             st.Input,
		Text:             st.Input,
		CursorLine:       st.Input,
		Placeholder:      st.InputPlaceholder,
		Prompt:           st.InputPrompt,
		LineNumber:       st.InputPlaceholder,
		CursorLineNumber: st.InputPrompt,
		EndOfBuffer:      st.InputPlaceholder,
	}
	styles.Blurred = styles.Focused
	styles.Cursor.Color = th.Accent.Compat()
	styles.Cursor.Blink = false
	ta.SetStyles(styles)
	ta.Prompt = th.Icons.Prompt + themedSpace(th.Spacing.XS)
}

func newTextInput(th theme.Theme, placeholder string) textinput.Model {
	th = th.Resolve()
	st := th.S()
	in := textinput.New()
	in.Placeholder = placeholder
	in.Prompt = th.Icons.InputCursor + themedSpace(th.Spacing.XS)
	in.SetVirtualCursor(true)
	styles := textinput.DefaultDarkStyles()
	styles.Focused = textinput.StyleState{
		Prompt:      st.InputPrompt,
		Text:        st.Input,
		Placeholder: st.InputPlaceholder,
	}
	styles.Blurred = styles.Focused
	styles.Cursor.Color = th.Accent.Compat()
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
	sp.Style = th.S().Spinner
}

// restyleWidgets reapplies theme tokens to composer and spinner after an
// appearance change (adaptive colors resolve differently under a new bg).
func (m *Model) restyleWidgets() {
	styleComposer(&m.composer, m.th)
	styleSpinner(&m.spin, m.th)
}
