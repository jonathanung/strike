package engine

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike/pkg/protocol"
)

func testQueueEngine() *Engine {
	return &Engine{events: make(chan protocol.Event, 8)}
}

func drainQueueEvent(t *testing.T, e *Engine) protocol.Event {
	t.Helper()
	select {
	case ev := <-e.events:
		return ev
	default:
		t.Fatal("expected a queue event")
		return nil
	}
}

func TestInputQueueEditKeepsImagesAndRejectsEmpty(t *testing.T) {
	e := testQueueEngine()
	e.enqueueUserInput(protocol.UserInput{
		Text:   "before",
		Images: []protocol.ImageAttachment{{MIME: "image/png", Ref: "att:sha256:abc"}},
	})
	queued, ok := drainQueueEvent(t, e).(protocol.InputQueued)
	if !ok || len(queued.Items) != 1 || queued.Items[0].Text != "before" || queued.Items[0].ID == "" {
		t.Fatalf("snapshot = %#v", queued)
	}
	id := queued.Items[0].ID

	// An image-bearing item may keep a blank caption. The image stays.
	e.handleInputQueueEdit(protocol.InputQueueEdit{ID: id, Text: "   "})
	if e.pendingUserInputs[0].text != "   " || e.pendingUserInputs[0].images[0].Ref != "att:sha256:abc" {
		t.Fatalf("item = %#v", e.pendingUserInputs[0])
	}
	if _, ok := drainQueueEvent(t, e).(protocol.InputQueued); !ok {
		t.Fatal("edit did not emit a snapshot")
	}

	e.handleInputQueueEdit(protocol.InputQueueEdit{ID: "missing", Text: "x"})
	errEv, ok := drainQueueEvent(t, e).(protocol.EngineError)
	if !ok || errEv.Code != protocol.ErrorCodeInvalidArgs || !strings.Contains(errEv.Message, "not in the queue") {
		t.Fatalf("missing edit = %#v", errEv)
	}
}

func TestInputQueueEditRejectsEmptyTextWithoutImages(t *testing.T) {
	e := testQueueEngine()
	e.enqueueUserInput(protocol.UserInput{Text: "keep"})
	queued := drainQueueEvent(t, e).(protocol.InputQueued)
	e.handleInputQueueEdit(protocol.InputQueueEdit{ID: queued.Items[0].ID, Text: " "})
	errEv, ok := drainQueueEvent(t, e).(protocol.EngineError)
	if !ok || !strings.Contains(errEv.Message, "needs text") {
		t.Fatalf("empty edit = %#v", errEv)
	}
	if e.pendingUserInputs[0].text != "keep" {
		t.Fatalf("text changed to %q", e.pendingUserInputs[0].text)
	}
}

func TestInputQueueReorderRejectsPartialList(t *testing.T) {
	e := testQueueEngine()
	e.enqueueUserInput(protocol.UserInput{Text: "a"})
	e.enqueueUserInput(protocol.UserInput{Text: "b"})
	_ = drainQueueEvent(t, e)
	second := drainQueueEvent(t, e).(protocol.InputQueued)
	if len(second.Items) != 2 {
		t.Fatalf("items = %#v", second.Items)
	}
	e.handleInputQueueReorder(protocol.InputQueueReorder{IDs: []string{second.Items[0].ID}})
	errEv, ok := drainQueueEvent(t, e).(protocol.EngineError)
	if !ok || !strings.Contains(errEv.Message, "every queued item") {
		t.Fatalf("partial reorder = %#v", errEv)
	}
	if e.pendingUserInputs[0].text != "a" || e.pendingUserInputs[1].text != "b" {
		t.Fatalf("order changed: %#v", e.pendingUserInputs)
	}

	e.handleInputQueueReorder(protocol.InputQueueReorder{IDs: []string{second.Items[1].ID, second.Items[0].ID}})
	snapped, ok := drainQueueEvent(t, e).(protocol.InputQueued)
	if !ok || snapped.Items[0].Text != "b" || snapped.Items[1].Text != "a" {
		t.Fatalf("reordered = %#v", snapped)
	}

	e.handleInputQueueCancel(protocol.InputQueueCancel{ID: snapped.Items[0].ID})
	left, ok := drainQueueEvent(t, e).(protocol.InputQueued)
	if !ok || len(left.Items) != 1 || left.Items[0].Text != "a" {
		t.Fatalf("after cancel = %#v", left)
	}
	e.handleInputQueueCancel(protocol.InputQueueCancel{ID: left.Items[0].ID})
	empty, ok := drainQueueEvent(t, e).(protocol.InputQueued)
	if !ok || len(empty.Items) != 0 {
		t.Fatalf("cleared = %#v", empty)
	}
	if e.pendingUserInputs != nil {
		t.Fatalf("backing slice = %#v, want nil", e.pendingUserInputs)
	}
}
