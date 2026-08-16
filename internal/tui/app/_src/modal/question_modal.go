package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

type questionPhase int

const (
	questionPhaseAnswer questionPhase = iota
	questionPhaseConfirm
)

// questionModal walks the user through a QuestionAsked batch. Users can hop
// between prompts, pick an option or write a custom answer, then review every
// answer on a confirmation screen before the batch is submitted. Esc always
// replies with empty answers so the question service unblocks as reject.
type questionModal struct {
	req        protocol.QuestionAsked
	ops        chan<- protocol.Op
	index      int
	answers    []string
	filled     []bool
	cursor     int
	input      textinput.Model
	th         theme.Theme
	phase      questionPhase
	customMode bool // freeform override when the current prompt has options
}

func newQuestionModal(req protocol.QuestionAsked, ops chan<- protocol.Op, themes ...theme.Theme) *questionModal {
	th := theme.Default()
	if len(themes) > 0 {
		th = themes[0]
	}
	th = th.Resolve()
	n := len(req.Questions)
	in := newTextInput(th, "type your answer")
	m := &questionModal{
		req:     req,
		ops:     ops,
		answers: make([]string, n),
		filled:  make([]bool, n),
		input:   in,
		th:      th,
	}
	m.prepareStep()
	return m
}

func (m *questionModal) current() (protocol.QuestionPrompt, bool) {
	if m.index < 0 || m.index >= len(m.req.Questions) {
		return protocol.QuestionPrompt{}, false
	}
	return m.req.Questions[m.index], true
}

func (m *questionModal) hasOptions() bool {
	q, ok := m.current()
	return ok && len(q.Options) > 0
}

// isFreeform is true when the user is typing (no options, or custom override).
func (m *questionModal) isFreeform() bool {
	if !m.hasOptions() {
		return true
	}
	return m.customMode
}

// optionCount is the number of real options (excludes the synthetic "custom" row).
func (m *questionModal) optionCount() int {
	q, ok := m.current()
	if !ok {
		return 0
	}
	return len(q.Options)
}

// listLen is options plus one synthetic "write your own" row when options exist.
func (m *questionModal) listLen() int {
	n := m.optionCount()
	if n == 0 {
		return 0
	}
	return n + 1
}

func (m *questionModal) prepareStep() {
	m.customMode = false
	m.cursor = 0
	m.input.SetValue("")
	m.input.Blur()
	q, ok := m.current()
	if !ok {
		return
	}
	if len(q.Options) == 0 {
		if m.filled[m.index] {
			m.input.SetValue(m.answers[m.index])
		}
		m.input.Focus()
		return
	}
	if m.filled[m.index] {
		ans := m.answers[m.index]
		for i, opt := range q.Options {
			if opt.Label == ans {
				m.cursor = i
				return
			}
		}
		// Prior answer was custom text.
		m.customMode = true
		m.input.SetValue(ans)
		m.input.Focus()
		return
	}
}

func (m *questionModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if len(m.req.Questions) == 0 {
		if isEscape(msg) {
			return nil, m.reply(nil)
		}
		return m, nil
	}

	if isEscape(msg) {
		return nil, m.reply(nil)
	}

	if m.phase == questionPhaseConfirm {
		return m.updateConfirm(msg)
	}
	return m.updateAnswer(msg)
}

func (m *questionModal) updateConfirm(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	switch msg.String() {
	case "enter", "y":
		return nil, m.reply(append([]string(nil), m.answers...))
	case "left", "h", "shift+tab", "backspace":
		// Return to the last question for editing.
		m.phase = questionPhaseAnswer
		if n := len(m.req.Questions); n > 0 {
			m.index = n - 1
		}
		m.prepareStep()
		if m.isFreeform() {
			return m, m.input.Focus()
		}
		return m, nil
	}
	return m, nil
}

func (m *questionModal) updateAnswer(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	key := msg.String()

	// Navigation between questions (shift+tab always; left/right only when not typing).
	switch key {
	case "shift+tab":
		return m.goPrev()
	case "left", "h":
		if !m.isFreeform() {
			return m.goPrev()
		}
	case "right", "l":
		if !m.isFreeform() {
			return m.goNext()
		}
	case "tab":
		if m.hasOptions() {
			return m.toggleCustom()
		}
		// No options: tab advances when the current step is already filled.
		if m.filled[m.index] {
			return m.goNext()
		}
	}

	if m.isFreeform() {
		switch key {
		case "enter":
			return m.accept(strings.TrimSpace(m.input.Value()))
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	// Options list mode.
	q, _ := m.current()
	n := m.listLen()
	switch key {
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
		if m.cursor >= m.optionCount() {
			return m.toggleCustom()
		}
		return m.accept(q.Options[m.cursor].Label)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		i := int(key[0] - '1')
		if i >= 0 && i < m.optionCount() && i < 9 {
			return m.accept(q.Options[i].Label)
		}
	}
	return m, nil
}

func (m *questionModal) toggleCustom() (modal, tea.Cmd) {
	if !m.hasOptions() {
		return m, nil
	}
	m.customMode = !m.customMode
	if m.customMode {
		if m.filled[m.index] {
			// Prefer existing custom text; if it matched an option, start empty.
			ans := m.answers[m.index]
			match := false
			if q, ok := m.current(); ok {
				for _, opt := range q.Options {
					if opt.Label == ans {
						match = true
						break
					}
				}
			}
			if !match {
				m.input.SetValue(ans)
			} else {
				m.input.SetValue("")
			}
		} else {
			m.input.SetValue("")
		}
		return m, m.input.Focus()
	}
	m.input.SetValue("")
	m.input.Blur()
	m.cursor = m.optionCount() // land on "write your own" row when leaving custom
	return m, nil
}

func (m *questionModal) goPrev() (modal, tea.Cmd) {
	if m.index <= 0 {
		return m, nil
	}
	m.index--
	m.prepareStep()
	if m.isFreeform() {
		return m, m.input.Focus()
	}
	return m, nil
}

func (m *questionModal) goNext() (modal, tea.Cmd) {
	if m.index < 0 || m.index >= len(m.filled) || !m.filled[m.index] {
		return m, nil
	}
	// Prefer the next prompt so hop stays linear even when the batch is already
	// fully answered (e.g. editing q1 after confirm should reach q2, not skip).
	if m.index+1 < len(m.req.Questions) {
		m.index++
		m.prepareStep()
		if m.isFreeform() {
			return m, m.input.Focus()
		}
		return m, nil
	}
	if m.allFilled() {
		m.phase = questionPhaseConfirm
		m.input.Blur()
	}
	return m, nil
}

func (m *questionModal) allFilled() bool {
	for _, f := range m.filled {
		if !f {
			return false
		}
	}
	return len(m.filled) > 0
}

// accept records one answer and advances to the next unfilled prompt or the
// confirmation screen when the batch is complete.
func (m *questionModal) accept(answer string) (modal, tea.Cmd) {
	if m.index < 0 || m.index >= len(m.answers) {
		return m, nil
	}
	m.answers[m.index] = answer
	m.filled[m.index] = true
	if m.allFilled() {
		m.phase = questionPhaseConfirm
		m.customMode = false
		m.input.Blur()
		return m, nil
	}
	// Prefer the next index; otherwise the first unfilled.
	next := m.index + 1
	if next >= len(m.req.Questions) || m.filled[next] {
		next = -1
		for i, f := range m.filled {
			if !f {
				next = i
				break
			}
		}
	}
	if next < 0 {
		m.phase = questionPhaseConfirm
		m.customMode = false
		m.input.Blur()
		return m, nil
	}
	m.index = next
	m.prepareStep()
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

	if m.phase == questionPhaseConfirm {
		return m.viewConfirm(width, inner, th, st)
	}

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
		cursorWidth := max(1, 1)
		m.input.SetWidth(max(1, inner-ansi.StringWidth(m.input.Prompt)-cursorWidth))
		m.input.SetValue(m.input.Value())
		parts = append(parts, m.input.View())
		hints := []string{"enter next"}
		if m.hasOptions() {
			hints = append(hints, "tab options")
		}
		if m.index > 0 {
			hints = append(hints, "shift+tab back")
		}
		hints = append(hints, "esc dismiss")
		hint = dotJoin(th, hints...)
	} else {
		optN := m.optionCount()
		items := make([]ui.ListItem, m.listLen())
		for i, opt := range q.Options {
			label := opt.Label
			if i < 9 {
				label = itoa(i+1) + ")" + themedSpace(th.Spacing.Label) + opt.Label
			}
			items[i] = ui.ListItem{Label: label, Detail: opt.Description}
		}
		items[optN] = ui.ListItem{Label: "Write your own answer" + th.Icons.Ellipsis}
		parts = append(parts, ui.List(th, ui.ListOpts{
			Items:   items,
			Cursor:  m.cursor,
			Width:   inner,
			Visible: len(items),
			Wrap:    true,
			Empty:   "no options",
		}))
		hints := []string{"up/down/j/k move", "enter or 1-9 select", "tab custom"}
		if m.index > 0 {
			hints = append(hints, "shift+tab back")
		}
		if m.filled[m.index] && (m.index+1 < len(m.req.Questions) || m.allFilled()) {
			hints = append(hints, "right next")
		}
		hints = append(hints, "esc dismiss")
		hint = dotJoin(th, hints...)
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

func (m *questionModal) viewConfirm(width, inner int, th theme.Theme, st theme.Styles) string {
	n := len(m.req.Questions)
	title := "confirm answers"
	if n > 1 {
		title = "confirm " + itoa(n) + " answers"
	}

	var parts []string
	parts = append(parts, wrapToWidth(st.Muted.Render("Review your answers before submitting."), inner))
	gap := strings.Repeat("\n", max(1, th.Spacing.SM))

	for i, q := range m.req.Questions {
		label := strings.TrimSpace(q.Header)
		if label == "" {
			label = q.Question
		}
		ans := ""
		if i < len(m.answers) {
			ans = m.answers[i]
		}
		if ans == "" {
			ans = "(empty)"
		}
		head := st.Accent.Render(itoa(i+1)+".") + themedSpace(th.Spacing.Label) + st.Text.Render(label)
		parts = append(parts, wrapToWidth(head, inner))
		parts = append(parts, wrapToWidth(st.Muted.Render(th.Icons.DetailSeparator+themedSpace(th.Spacing.Label)+ans), inner))
	}

	body := strings.Join(parts, gap)
	return ui.Dialog(th, ui.DialogOpts{
		Title: title,
		Hint:  dotJoin(th, "enter submit", "left edit", "esc dismiss"),
		Width: width,
		Tone:  ui.ToneAccent,
	}, body)
}
