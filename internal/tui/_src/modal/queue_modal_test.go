package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func sampleQueue() []queuedInput {
	return []queuedInput{
		{modelText: "first", displayPrompt: "first"},
		{modelText: "second", displayPrompt: "second"},
		{modelText: "third", displayPrompt: "third"},
	}
}

func TestQueueModalViewListsItems(t *testing.T) {
	m := newQueueModal(sampleQueue(), theme.Default())
	plain := ansi.Strip(m.view(72, theme.Default()))
	dot := theme.Default().Resolve().Icons.Dot
	for _, want := range []string{"Input queue", "first", "second", "third", "1" + dot + "next"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("view missing %q:\n%s", want, plain)
		}
	}
}

func TestQueueModalReorderAndPromote(t *testing.T) {
	m := newQueueModal(sampleQueue())
	m.cursor = 2
	next, cmd := m.update(tea.KeyPressMsg{Text: "K"}) // shift+K style via "K"
	if next == nil {
		t.Fatal("modal closed on reorder")
	}
	qm := next.(*queueModal)
	if cmd == nil {
		t.Fatal("expected replace cmd after reorder")
	}
	msg := cmd()
	rep, ok := msg.(inputQueueReplaceMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	// K moves up: third swaps with second → first, third, second; cursor on third at idx 1
	if got := labels(rep.items); got != "first|third|second" {
		t.Fatalf("after K = %s", got)
	}
	if qm.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", qm.cursor)
	}

	next, cmd = qm.update(tea.KeyPressMsg{Text: "p"})
	qm = next.(*queueModal)
	rep = cmd().(inputQueueReplaceMsg)
	if got := labels(rep.items); got != "third|first|second" {
		t.Fatalf("after promote = %s", got)
	}
	if qm.cursor != 0 {
		t.Fatalf("cursor after promote = %d", qm.cursor)
	}
}

func TestQueueModalDeleteAndClear(t *testing.T) {
	m := newQueueModal(sampleQueue())
	m.cursor = 1
	next, cmd := m.update(tea.KeyPressMsg{Text: "d"})
	qm := next.(*queueModal)
	rep := cmd().(inputQueueReplaceMsg)
	if got := labels(rep.items); got != "first|third" {
		t.Fatalf("after delete = %s", got)
	}
	if qm.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 (clamped to last)", qm.cursor)
	}

	next, cmd = qm.update(tea.KeyPressMsg{Text: "c"})
	qm = next.(*queueModal)
	rep = cmd().(inputQueueReplaceMsg)
	if len(rep.items) != 0 {
		t.Fatalf("clear left %d items", len(rep.items))
	}
	if len(qm.items) != 0 {
		t.Fatalf("modal items = %#v", qm.items)
	}
}

func TestQueueModalEditSavesText(t *testing.T) {
	m := newQueueModal(sampleQueue())
	next, _ := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	qm := next.(*queueModal)
	if !qm.edit {
		t.Fatal("enter did not enter edit mode")
	}
	qm.input.SetValue("rewritten first")
	next, cmd := qm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	qm = next.(*queueModal)
	if qm.edit {
		t.Fatal("enter did not leave edit mode")
	}
	rep := cmd().(inputQueueReplaceMsg)
	if rep.items[0].modelText != "rewritten first" || rep.items[0].displayPrompt != "rewritten first" {
		t.Fatalf("saved = %#v", rep.items[0])
	}
}

func TestQueueModalEditCancelKeepsOriginal(t *testing.T) {
	m := newQueueModal(sampleQueue())
	next, _ := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	qm := next.(*queueModal)
	qm.input.SetValue("discard me")
	next, cmd := qm.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	qm = next.(*queueModal)
	if qm.edit {
		t.Fatal("esc did not leave edit")
	}
	if cmd != nil {
		t.Fatal("cancel should not emit replace")
	}
	if qm.items[0].modelText != "first" {
		t.Fatalf("items mutated on cancel: %#v", qm.items[0])
	}
}

func TestQueueModalEditToComposerMsg(t *testing.T) {
	m := newQueueModal(sampleQueue())
	m.cursor = 1
	next, cmd := m.update(tea.KeyPressMsg{Text: "e"})
	if next != nil {
		t.Fatal("e should close modal")
	}
	msg := cmd().(inputQueueEditComposerMsg)
	if msg.text != "second" {
		t.Fatalf("text = %q", msg.text)
	}
	if got := labels(msg.remaining); got != "first|third" {
		t.Fatalf("remaining = %s", got)
	}
}

func TestQueueModalRunNextMsg(t *testing.T) {
	m := newQueueModal(sampleQueue())
	next, cmd := m.update(tea.KeyPressMsg{Text: "x"})
	if next != nil {
		t.Fatal("x should close modal")
	}
	if _, ok := cmd().(inputQueueRunNextMsg); !ok {
		t.Fatalf("want inputQueueRunNextMsg")
	}
}

func TestQueueModalEscCloses(t *testing.T) {
	m := newQueueModal(sampleQueue())
	next, cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if next != nil || cmd != nil {
		t.Fatalf("esc close = %v %v", next, cmd)
	}
}

func TestQueueModalSyncFromSkipsWhileEditing(t *testing.T) {
	m := newQueueModal(sampleQueue())
	m.beginEdit()
	m.syncFrom([]queuedInput{{modelText: "only", displayPrompt: "only"}})
	if len(m.items) != 3 {
		t.Fatalf("sync while edit changed items: %#v", m.items)
	}
	m.edit = false
	m.syncFrom([]queuedInput{{modelText: "only", displayPrompt: "only"}})
	if len(m.items) != 1 || m.items[0].modelText != "only" {
		t.Fatalf("sync = %#v", m.items)
	}
}

func TestQueueModalPreservesImagesOnTextEdit(t *testing.T) {
	img := protocol.ImageAttachment{MIME: "image/png", Data: "abc"}
	m := newQueueModal([]queuedInput{{
		modelText: "cap", displayPrompt: "cap",
		images: []protocol.ImageAttachment{img},
	}})
	m.beginEdit()
	m.input.SetValue("new cap")
	_, cmd := m.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	rep := cmd().(inputQueueReplaceMsg)
	if rep.items[0].modelText != "new cap" || len(rep.items[0].images) != 1 {
		t.Fatalf("item = %#v", rep.items[0])
	}
}

func labels(items []queuedInput) string {
	parts := make([]string, len(items))
	for i, q := range items {
		parts[i] = q.modelText
	}
	return strings.Join(parts, "|")
}
