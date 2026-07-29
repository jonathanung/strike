package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestApplyEventToPaneTurnCompletedMarksAssistantComplete(t *testing.T) {
	p := &rootPane{toolByID: map[string]*toolCell{}}
	applyEventToPane(p, protocol.TurnStarted{})
	applyEventToPane(p, protocol.TextDelta{Text: "# Hello\n\n**bold**"})

	assts := assistantCellsOf(p.cells)
	if len(assts) != 1 {
		t.Fatalf("after TextDelta: %d assistants, want 1", len(assts))
	}
	if assts[0].complete {
		t.Fatal("assistant complete=true while streaming on background pane")
	}

	applyEventToPane(p, protocol.TurnCompleted{StopReason: "end_turn"})
	assts = assistantCellsOf(p.cells)
	if len(assts) != 1 {
		t.Fatalf("after TurnCompleted: %d assistants, want 1", len(assts))
	}
	if !assts[0].complete {
		t.Fatal("background TurnCompleted left assistant incomplete; switch-in would stay plain")
	}
	if assts[0].mdCacheOK {
		t.Fatal("mdCacheOK still true after background complete, want false")
	}
}

func TestApplyEventToPaneUserMessageMarksPriorAssistantComplete(t *testing.T) {
	p := &rootPane{toolByID: map[string]*toolCell{}}
	applyEventToPane(p, protocol.TextDelta{Text: "prior reply with **md**"})
	applyEventToPane(p, protocol.UserMessage{Text: "next turn"})

	assts := assistantCellsOf(p.cells)
	if len(assts) != 1 {
		t.Fatalf("assistants = %d, want 1", len(assts))
	}
	if !assts[0].complete {
		t.Fatal("background UserMessage left prior assistant incomplete")
	}
}

func TestLoadRootPaneAfterBackgroundTurnPrettifiesMarkdown(t *testing.T) {
	// Root A streams+completes while B is focused; loadRootPane (activateRoot)
	// must leave assistants complete so the first View uses glamour.
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-b"

	bg := &rootPane{
		sessionID: "root-a",
		toolByID:  map[string]*toolCell{},
	}
	applyEventToPane(bg, protocol.TurnStarted{})
	applyEventToPane(bg, protocol.TextDelta{Text: "# Title\n\n**bold** answer"})
	applyEventToPane(bg, protocol.TurnCompleted{StopReason: "end_turn"})

	m.loadRootPane(bg)

	assts := assistantCellsOf(m.cells)
	if len(assts) != 1 || !assts[0].complete {
		t.Fatalf("loaded pane assistant complete=%v count=%d",
			len(assts) == 1 && assts[0].complete, len(assts))
	}

	plain := ansi.Strip(assts[0].render(80, theme.Default()))
	if strings.Contains(plain, "**bold**") {
		t.Fatalf("switch-in still shows raw markdown markers:\n%s", plain)
	}
	if !strings.Contains(plain, "bold") {
		t.Fatalf("switch-in missing prettified bold content:\n%s", plain)
	}
}
