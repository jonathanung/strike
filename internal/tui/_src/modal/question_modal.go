package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// questionModal walks the user through a QuestionAsked batch one prompt at a
// time. Options use a list picker; freeform prompts use a text input. Esc
// always replies with empty answers so the question service unblocks as reject.
type questionModal struct {
	req     protocol.QuestionAsked
	ops     chan<- protocol.Op
	index   int
	answers []string
	cursor  int
	input   textinput.Model
	th      theme.Theme
}

func newQuestionModal(req protocol.QuestionAsked, ops chan<- protocol.Op, themes ...theme.Theme) *questionModal {
	th := theme.Default()
	if len(themes) > 0 {
		th = themes[0]
	}
	th = th.Resolve()
	in := newTextInput(th, "type your answer")
	m := &questionModal{req: req, ops: ops, answers: make([]string, 0, len(req.Questions)), input: in, th: th}
	if m.isFreeform() {
		m.input.Focus()
	}
	return m
}

func (m *questionModal) current() (protocol.QuestionPrompt, bool) {
	if m.index < 0 || m.index >= len(m.req.Questions) {
		return protocol.QuestionPrompt{}, false
	}
	return m.req.Questions[m.index], true
}

func (m *questionModal) isFreeform() bool {
	q, ok := m.current()
	return !ok || len(q.Options) == 0
}

func (m *questionModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	if len(m.req.Questions) == 0 {
		if isEscape(msg) {
			return nil, m.reply(nil)
		}
		return m, nil
	}

	if m.isFreeform() {
		if isEscape(msg) {
			return nil, m.reply(nil)
		}
		switch msg.String() {
		case "enter":
			return m.accept(strings.TrimSpace(m.input.Value()))
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	q, _ := m.current()
	n := len(q.Options)
	if isEscape(msg) {
		return nil, m.reply(nil)
	}
	switch msg.String() {
	case "up", "k":
		if n > 0 {
			m.cursor = (m.cursor + n - 1) % n
		}
	case "down", "j":
		if n > 0 {
			m.cursor = (m.cursor + 1) % n
		}
	case "enter":
		if n == 0 {
			return m, nil
		}
		return m.accept(q.Options[m.cursor].Label)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		i := int(msg.String()[0] - '1')
		if i >= 0 && i < n && i < 9 {
			return m.accept(q.Options[i].Label)
		}
	}
	return m, nil
}

// accept records one answer and either advances to the next prompt or submits
// the full batch as a single QuestionReply.
func (m *questionModal) accept(answer string) (modal, tea.Cmd) {
	m.answers = append(m.answers, answer)
	if m.index+1 >= len(m.req.Questions) {
		return nil, m.reply(append([]string(nil), m.answers...))
	}
	m.index++
	m.cursor = 0
	m.input.SetValue("")
	m.input.Blur()
	if m.isFreeform() {
		return m, m.input.Focus()
	}
	return m, nil
}

func (m *questionModal) reply(answers []string) tea.Cmd {
	reqID := m.req.RequestID
	ops := m.ops
	return func() tea.Msg {
		ops <- protocol.QuestionReply{RequestID: reqID, Answers: answers}
		return nil
	}
}

func (m *questionModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))

	title := "question"
	if n := len(m.req.Questions); n > 1 {
		title = "question " + itoa(m.index+1) + "/" + itoa(n)
	}

	q, ok := m.current()
	if !ok {
		return ui.Dialog(th, ui.DialogOpts{
			Title: title,
			Hint:  dotJoin(th, "esc dismiss"),
			Width: width,
			Tone:  ui.ToneAccent,
		}, st.Muted.Render("no questions"))
	}

	var parts []string
	if h := strings.TrimSpace(q.Header); h != "" {
		parts = append(parts, wrapToWidth(st.Accent.Render(h), inner))
	}
	parts = append(parts, wrapToWidth(st.Text.Render(q.Question), inner))

	var hint string
	if m.isFreeform() {
		cursorWidth := max(1, ansi.StringWidth(m.input.Cursor.View()))
		m.input.Width = max(1, inner-ansi.StringWidth(m.input.Prompt)-cursorWidth)
		m.input.SetValue(m.input.Value())
		parts = append(parts, m.input.View())
		hint = dotJoin(th, "enter submit", "esc dismiss")
	} else {
		items := make([]ui.ListItem, len(q.Options))
		for i, opt := range q.Options {
			label := opt.Label
			if i < 9 {
				label = itoa(i+1) + ")" + themedSpace(th.Spacing.Label) + opt.Label
			}
			items[i] = ui.ListItem{Label: label, Detail: opt.Description}
		}
		parts = append(parts, ui.List(th, ui.ListOpts{
			Items:   items,
			Cursor:  m.cursor,
			Width:   inner,
			Visible: len(items),
			Empty:   "no options",
		}))
		hint = dotJoin(th, "up/down/j/k move", "enter or 1-9 select", "esc dismiss")
	}

	gap := strings.Repeat("\n", max(1, th.Spacing.SM))
	body := strings.Join(parts, gap)
	return ui.Dialog(th, ui.DialogOpts{
		Title: title,
		Hint:  hint,
		Width: width,
		Tone:  ui.ToneAccent,
	}, body)
}
