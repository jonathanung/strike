package tui

import (
	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func newComposer(th theme.Theme) textarea.Model {
	th = th.Resolve()
	st := th.S()
	ta := textarea.New()
	ta.Placeholder = "Ask strike anything… (/provider to pick a model, enter to send)"
	ta.Prompt = th.Icons.Prompt + themedSpace(th.Spacing.XS)
	ta.MaxHeight = composerMaxHeight
	ta.SetHeight(composerMinHeight)
	ta.ShowLineNumbers = false
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
	ta.Cursor.SetMode(cursor.CursorStatic)
	ta.Focus()
	return ta
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
	th = th.Resolve()
	sp := spinner.New()
	sp.Spinner = spinner.Spinner{Frames: []string{th.Icons.Dot, th.Icons.Cursor}, FPS: spinner.MiniDot.FPS}
	sp.Style = th.S().Spinner
	return sp
}
