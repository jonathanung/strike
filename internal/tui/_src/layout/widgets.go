package tui

import (
	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func newComposer(th theme.Theme) textarea.Model {
	ta := textarea.New()
	ta.Placeholder = "Ask anything…  (/ for commands)"
	ta.MaxHeight = composerMaxHeight
	ta.SetHeight(composerMinHeight)
	ta.ShowLineNumbers = false
	ta.Cursor.SetMode(cursor.CursorStatic)
	styleComposer(&ta, th)
	ta.Focus()
	return ta
}

// styleComposer applies resolved theme tokens to an existing textarea so
// appearance toggles can restyle without dropping value or cursor state.
func styleComposer(ta *textarea.Model, th theme.Theme) {
	th = th.Resolve()
	st := th.S()
	ta.Prompt = th.Icons.Prompt + themedSpace(th.Spacing.XS)
	ta.FocusedStyle = textarea.Style{
		Base:        st.Input,
		CursorLine:  st.Input,
		Placeholder: st.InputPlaceholder,
		Prompt:      st.InputPrompt,
		Text:        st.Input,
	}
	ta.BlurredStyle = ta.FocusedStyle
	ta.Cursor.Style = st.InputCursor
	ta.Cursor.TextStyle = st.Input
}

func newTextInput(th theme.Theme, placeholder string) textinput.Model {
	th = th.Resolve()
	st := th.S()
	in := textinput.New()
	in.Placeholder = placeholder
	in.Prompt = th.Icons.InputCursor + themedSpace(th.Spacing.XS)
	in.PromptStyle = st.InputPrompt
	in.TextStyle = st.Input
	in.PlaceholderStyle = st.InputPlaceholder
	in.Cursor.Style = st.InputCursor
	in.Cursor.TextStyle = st.Input
	in.Cursor.SetMode(cursor.CursorStatic)
	return in
}

func newSpinner(th theme.Theme) spinner.Model {
	sp := spinner.New()
	styleSpinner(&sp, th)
	return sp
}

// styleSpinner reapplies theme spinner tokens after an appearance change.
func styleSpinner(sp *spinner.Model, th theme.Theme) {
	th = th.Resolve()
	sp.Spinner = spinner.Spinner{Frames: []string{th.Icons.Dot, th.Icons.Cursor}, FPS: spinner.MiniDot.FPS}
	sp.Style = th.S().Spinner
}

// restyleWidgets reapplies theme tokens to composer and spinner after an
// appearance change (adaptive colors resolve differently under a new bg).
func (m *Model) restyleWidgets() {
	styleComposer(&m.composer, m.th)
	styleSpinner(&m.spin, m.th)
}
