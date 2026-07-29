package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestChildViewTitleBrief(t *testing.T) {
	tests := []struct {
		name              string
		agent, prompt, id string
		title             string
		wantContains      []string
		wantNotContains   []string
	}{
		{
			name:            "agent and id",
			agent:           "explore",
			prompt:          "a very long prompt that should not appear in the label",
			id:              "child-abcdef12",
			wantContains:    []string{"explore", "abcdef12"},
			wantNotContains: []string{"very long prompt"},
		},
		{
			name:            "durable title wins",
			agent:           "explore",
			id:              "child-x",
			title:           "ship auth",
			wantContains:    []string{"ship auth"},
			wantNotContains: []string{"explore"},
		},
		{
			name:         "agent only",
			agent:        "build",
			wantContains: []string{"build"},
		},
		{
			name:         "empty falls back",
			wantContains: []string{"subagent"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := childViewTitle(tt.agent, tt.prompt, tt.id, tt.title)
			for _, w := range tt.wantContains {
				if !strings.Contains(got, w) {
					t.Errorf("got %q, want contain %q", got, w)
				}
			}
			for _, w := range tt.wantNotContains {
				if strings.Contains(got, w) {
					t.Errorf("got %q, must not contain %q", got, w)
				}
			}
		})
	}
}

func TestRenameModalPersistsAndEmits(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "s1", Title: "old"}, nil)
	m := newRenameModal(fs, "s1", "old")
	// Clear and type.
	for range m.input.Value() {
		next, _ := m.update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = next.(*renameModal)
	}
	next, _ := m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("brief name")})
	m = next.(*renameModal)
	next, cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next != nil {
		t.Fatalf("modal after save = %T, want nil", next)
	}
	if cmd == nil {
		t.Fatal("expected sessionRenamedMsg cmd")
	}
	msg := cmd()
	rm, ok := msg.(sessionRenamedMsg)
	if !ok || rm.id != "s1" || rm.title != "brief name" {
		t.Fatalf("msg = %#v", msg)
	}
	got, ok, err := fs.Get("s1")
	if err != nil || !ok || got.Title != "brief name" {
		t.Fatalf("host title = %+v ok=%v err=%v", got, ok, err)
	}
}

func TestRenameModalAcceptsKeySpace(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "s1", Title: "a"}, nil)
	m := newRenameModal(fs, "s1", "a")
	// Bubble Tea delivers space as KeySpace (with Runes still set), not KeyRunes.
	next, _ := m.update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m = next.(*renameModal)
	next, _ = m.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	m = next.(*renameModal)
	if got := m.input.Value(); got != "a b" {
		t.Fatalf("value = %q, want %q", got, "a b")
	}
	next, cmd := m.update(tea.KeyMsg{Type: tea.KeyEnter})
	if next != nil {
		t.Fatalf("modal after save = %T, want nil", next)
	}
	msg := cmd()
	rm, ok := msg.(sessionRenamedMsg)
	if !ok || rm.title != "a b" {
		t.Fatalf("msg = %#v", msg)
	}
}

func TestRenameModalCaretIsPromptPrefix(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "s1", Title: "ship it"}, nil)
	m := newRenameModal(fs, "s1", "ship it")
	plain := ansi.Strip(m.view(72, theme.Default()))
	// InputCursor (">") is the left-side prompt, not a trailing caret.
	// Match newTextInput: prompt then value; value must not end with ">".
	idx := strings.Index(plain, "ship it")
	if idx < 0 {
		t.Fatalf("view missing title:\n%s", plain)
	}
	// Prompt glyph appears before the title text on the input line.
	before := plain[:idx]
	if !strings.Contains(before, ">") {
		t.Fatalf("expected InputCursor prompt before title, view:\n%s", plain)
	}
	after := plain[idx+len("ship it"):]
	// Strip common trailing chrome (newline / dialog padding) and reject a
	// glued trailing caret like "ship it>".
	if strings.HasPrefix(strings.TrimLeft(after, " \t"), ">") {
		t.Fatalf("caret still trailing the title:\n%s", plain)
	}
}

func TestRenameCommandAppliesToCurrentSession(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "root-1", Title: "before"}, nil)
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-1"
	m.titleTopic = "before"
	m.services.Sessions = fs

	next, _ := m.handleCommand("/rename after rename")
	m = next.(Model)
	if m.titleTopic != "after rename" {
		t.Fatalf("titleTopic = %q, want after rename", m.titleTopic)
	}
	got, ok, err := fs.Get("root-1")
	if err != nil || !ok || got.Title != "after rename" {
		t.Fatalf("host = %+v ok=%v err=%v", got, ok, err)
	}
}

func TestRenameCommandOpensModalWithoutArgs(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "root-1", Title: "live"}, nil)
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-1"
	m.titleTopic = "live"
	m.services.Sessions = fs

	next, _ := m.handleCommand("/rename")
	m = next.(Model)
	rm, ok := m.modal.(*renameModal)
	if !ok {
		t.Fatalf("modal = %T, want *renameModal", m.modal)
	}
	if rm.id != "root-1" || rm.input.Value() != "live" {
		t.Fatalf("rename modal id=%q value=%q", rm.id, rm.input.Value())
	}
	plain := ansi.Strip(rm.view(72, m.th))
	if !strings.Contains(plain, "Rename") {
		t.Errorf("view missing rename chrome: %q", plain)
	}
}

func TestAgentsPaneRenameKeyOpensModal(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "root-a", Title: "alpha"}, nil)
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.titleTopic = "alpha"
	m.services.Sessions = fs
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	w := newAgentsWindow().resize(40, 8).(agentsWindow)
	next, cmd := w.update(agentsStateMsg{
		activeID: "root-a",
		roots:    []agentsRootSnap{{ID: "root-a", Title: "alpha"}},
	})
	w = next.(agentsWindow)
	next, cmd = w.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if cmd == nil {
		t.Fatal("expected agentsRenameMsg")
	}
	msg := cmd()
	am, ok := msg.(agentsRenameMsg)
	if !ok || am.sessionID != "root-a" {
		t.Fatalf("msg = %#v", msg)
	}
	m = updateApp(t, m, am)
	if _, ok := m.modal.(*renameModal); !ok {
		t.Fatalf("modal = %T, want *renameModal", m.modal)
	}
}

func TestApplySessionRenameUpdatesChildAndRoot(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "root-a", Title: "old root"}, nil)
	fs.put(host.Session{ID: "child-1", ParentID: "root-a", Title: "old child"}, nil)
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.titleTopic = "old root"
	m.services.Sessions = fs
	m.children = []childActivity{{
		sessionID: "child-1",
		agent:     "explore",
		title:     "old child",
		status:    "completed",
	}}

	m = updateApp(t, m, sessionRenamedMsg{id: "root-a", title: "new root"})
	if m.titleTopic != "new root" {
		t.Fatalf("root titleTopic = %q", m.titleTopic)
	}
	m = updateApp(t, m, sessionRenamedMsg{id: "child-1", title: "new child"})
	if m.children[0].title != "new child" {
		t.Fatalf("child title = %q", m.children[0].title)
	}
	label := childViewTitle(m.children[0].agent, m.children[0].prompt, m.children[0].sessionID, m.children[0].title)
	if label != "new child" {
		t.Fatalf("label = %q", label)
	}
}

func TestSessionModalRenameEmitsLiveUpdate(t *testing.T) {
	fs := newFakeSessions()
	fs.put(host.Session{ID: "s1", Title: "old name"}, nil)
	sm := newSessionModal(fs, "s1")
	next, _ := sm.update(tea.KeyMsg{Type: tea.KeyCtrlR})
	sm = next.(*sessionModal)
	for range sm.renameBuf {
		next, _ = sm.update(tea.KeyMsg{Type: tea.KeyBackspace})
		sm = next.(*sessionModal)
	}
	next, _ = sm.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("fresh")})
	sm = next.(*sessionModal)
	next, cmd := sm.update(tea.KeyMsg{Type: tea.KeyEnter})
	sm = next.(*sessionModal)
	if cmd == nil {
		t.Fatal("expected sessionRenamedMsg from session modal")
	}
	msg := cmd()
	rm, ok := msg.(sessionRenamedMsg)
	if !ok || rm.title != "fresh" {
		t.Fatalf("msg = %#v", msg)
	}
}
