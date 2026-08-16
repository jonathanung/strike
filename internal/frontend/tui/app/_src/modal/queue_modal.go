package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

const queueModalVisible = 10

// inputQueueRunNextMsg closes the queue browser and either interrupts the
// running turn (next item drains on TurnCompleted) or drains immediately when idle.
type inputQueueRunNextMsg struct{}

// inputQueueEditComposerMsg removes the selected item from the queue and loads
// its display text into the composer for re-edit.
type inputQueueEditComposerMsg struct {
	remaining []queuedInput
	text      string
}

// queueModal browses and edits prompts buffered while a turn runs (/queue).
// List mode: navigate, reorder, delete, promote, run-next. Edit mode: rewrite
// the selected prompt text (images stay attached).
type queueModal struct {
	items  []queuedInput
	cursor int
	edit   bool
	input  textinput.Model
	th     theme.Theme
}

func newQueueModal(items []queuedInput, themes ...theme.Theme) *queueModal {
	th := theme.Default()
	if len(themes) > 0 {
		th = themes[0]
	}
	th = th.Resolve()
	in := newTextInput(th, "edit queued prompt")
	return &queueModal{
		items: cloneQueuedInputs(items),
		input: in,
		th:    th,
	}
}

// syncFrom replaces the modal list from the live app queue (enqueue/drain while open).
func (m *queueModal) syncFrom(items []queuedInput) {
	if m == nil || m.edit {
		return
	}
	m.items = cloneQueuedInputs(items)
	if m.cursor >= len(m.items) {
		m.cursor = max(0, len(m.items)-1)
	}
}

func (m *queueModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if m.edit {
		return m.updateEdit(msg)
	}
	return m.updateList(msg)
}

func (m *queueModal) updateList(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "q" {
		return nil, nil
	}
	n := len(m.items)
	switch msg.String() {
	case "up", "ctrl+p", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "ctrl+n", "j":
		if m.cursor < n-1 {
			m.cursor++
		}
	case "shift+up", "K":
		m.moveSelected(-1)
	case "shift+down", "J":
		m.moveSelected(1)
	case "p":
		m.promoteSelected()
	case "d", "delete", "backspace":
		m.removeSelected()
	case "c":
		if n == 0 {
			return m, nil
		}
		m.items = nil
		m.cursor = 0
	case "enter":
		if n == 0 || m.cursor < 0 || m.cursor >= n {
			return m, nil
		}
		m.beginEdit()
	case "e":
		if n == 0 || m.cursor < 0 || m.cursor >= n {
			return m, nil
		}
		// Load into composer and close (text only; matches pop-last).
		// handleKeyMsg syncs the live queue from items before close; remaining
		// is computed here so the edit msg is self-contained.
		item := m.items[m.cursor]
		remaining := append([]queuedInput(nil), m.items[:m.cursor]...)
		remaining = append(remaining, m.items[m.cursor+1:]...)
		m.items = remaining
		if m.cursor >= len(m.items) {
			m.cursor = max(0, len(m.items)-1)
		}
		text := item.displayPrompt
		if text == "" {
			text = item.modelText
		}
		return nil, func() tea.Msg {
			return inputQueueEditComposerMsg{remaining: remaining, text: text}
		}
	case "x", "ctrl+x":
		// Interrupt current turn (if any) and run the FIFO head next.
		return nil, func() tea.Msg { return inputQueueRunNextMsg{} }
	default:
		// ignore typing in list mode (no filter — queue is short)
	}
	return m, nil
}

func (m *queueModal) updateEdit(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) {
		m.edit = false
		m.input.Blur()
		return m, nil
	}
	if msg.String() == "enter" {
		text := m.input.Value()
		// Allow empty only if images remain on the item.
		if m.cursor >= 0 && m.cursor < len(m.items) {
			item := m.items[m.cursor]
			if strings.TrimSpace(text) == "" && len(item.images) == 0 {
				// Keep edit open; nothing to save.
				return m, nil
			}
			item.displayPrompt = text
			item.modelText = text
			m.items[m.cursor] = item
		}
		m.edit = false
		m.input.Blur()
		// handleKeyMsg syncs live queue from items when leaving edit.
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *queueModal) beginEdit() {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return
	}
	item := m.items[m.cursor]
	text := item.displayPrompt
	if text == "" {
		text = item.modelText
	}
	m.input.SetValue(text)
	m.input.CursorEnd()
	m.input.Focus()
	m.edit = true
}

func (m *queueModal) moveSelected(delta int) bool {
	n := len(m.items)
	if n < 2 || m.cursor < 0 || m.cursor >= n {
		return false
	}
	j := m.cursor + delta
	if j < 0 || j >= n {
		return false
	}
	m.items[m.cursor], m.items[j] = m.items[j], m.items[m.cursor]
	m.cursor = j
	return true
}

func (m *queueModal) promoteSelected() bool {
	if m.cursor <= 0 || m.cursor >= len(m.items) {
		return false
	}
	item := m.items[m.cursor]
	copy(m.items[1:m.cursor+1], m.items[0:m.cursor])
	m.items[0] = item
	m.cursor = 0
	return true
}

func (m *queueModal) removeSelected() bool {
	n := len(m.items)
	if n == 0 || m.cursor < 0 || m.cursor >= n {
		return false
	}
	m.items = append(m.items[:m.cursor], m.items[m.cursor+1:]...)
	if len(m.items) == 0 {
		m.items = nil
		m.cursor = 0
		return true
	}
	if m.cursor >= len(m.items) {
		m.cursor = len(m.items) - 1
	}
	return true
}

func (m *queueModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))
	title := "Input queue"
	if n := len(m.items); n > 0 {
		title = detailJoin(th, "Input queue", fmt.Sprintf("%d", n))
	}

	if m.edit {
		sizeInput(&m.input, inner)
		lines := []string{
			st.Muted.Render(fmt.Sprintf("Edit #%d", m.cursor+1)),
			m.input.View(),
		}
		if m.cursor >= 0 && m.cursor < len(m.items) && len(m.items[m.cursor].images) > 0 {
			lines = append(lines, st.Muted.Render(fmt.Sprintf("%d image(s) kept", len(m.items[m.cursor].images))))
		}
		body := wrapToWidth(strings.Join(lines, "\n"), inner)
		return ui.Dialog(th, ui.DialogOpts{
			Title: title,
			Hint:  dotJoin(th, "type", "enter save", "esc cancel"),
			Width: width,
		}, body)
	}

	if m.cursor >= len(m.items) {
		m.cursor = max(0, len(m.items)-1)
	}
	items := make([]ui.ListItem, len(m.items))
	for i, q := range m.items {
		label := fmt.Sprintf("%d", i+1)
		if i == 0 {
			label = "1" + th.Icons.Dot + "next"
		}
		detail := queueItemLabel(th, q)
		if len(q.images) > 0 {
			detail = detailJoin(th, detail, fmt.Sprintf("%d img", len(q.images)))
		}
		items[i] = ui.ListItem{
			Label:   label,
			Detail:  detail,
			Current: i == 0,
		}
	}
	empty := "queue empty " + th.Icons.DetailSeparator + " prompts typed during a turn land here"
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   inner,
		Visible: queueModalVisible,
		Empty:   empty,
	})
	hint := dotJoin(th,
		"↑/↓ move",
		"shift+↑/↓ reorder",
		"enter edit",
		"e to composer",
		"p promote",
		"d delete",
		"c clear",
		"x run next",
		"esc close",
	)
	return ui.Dialog(th, ui.DialogOpts{
		Title: title,
		Hint:  hint,
		Width: width,
	}, body)
}

func queueItemLabel(th theme.Theme, q queuedInput) string {
	th = th.Resolve()
	text := strings.Join(strings.Fields(q.displayPrompt), " ")
	if text == "" {
		text = strings.Join(strings.Fields(q.modelText), " ")
	}
	if text == "" && len(q.images) > 0 {
		return "(image only)"
	}
	if text == "" {
		return "(empty)"
	}
	if cut := truncateRunes(text, 64); cut != text {
		return cut + th.Icons.Ellipsis
	}
	return text
}

func cloneQueuedInputs(in []queuedInput) []queuedInput {
	if len(in) == 0 {
		return nil
	}
	out := make([]queuedInput, len(in))
	for i, q := range in {
		out[i] = queuedInput{
			modelText:     q.modelText,
			displayPrompt: q.displayPrompt,
			images:        append([]protocol.ImageAttachment(nil), q.images...),
		}
	}
	return out
}
