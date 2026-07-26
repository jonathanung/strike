package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestSessionTitledSetsPanelAndWindowTitle(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.sessionID = "sess-abcdef123456"
	m.cells = append(m.cells, &userCell{text: "placeholder"})

	cmd := m.applyEvent(protocol.SessionTitled{Title: "  fix   the auth flow  "})
	if m.titleTopic != "fix the auth flow" {
		t.Fatalf("titleTopic = %q, want %q", m.titleTopic, "fix the auth flow")
	}
	if got := m.sessionPanelTitle(); got != "fix the auth flow" {
		t.Errorf("sessionPanelTitle = %q", got)
	}
	if got := windowTitle(m); !strings.Contains(got, "fix the auth flow") {
		t.Errorf("windowTitle = %q", got)
	}
	if cmd == nil {
		t.Fatal("expected title update cmd")
	}
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "fix the auth flow") {
		t.Errorf("view missing session title:\n%s", plain)
	}
}

func TestUserMessageFallbackTitlesWhenNoSessionTitled(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.UserMessage{Text: "hello world from user"})
	if m.titleTopic != "hello world from user" {
		t.Fatalf("titleTopic = %q", m.titleTopic)
	}
	m.applyEvent(protocol.UserMessage{Text: "second"})
	if m.titleTopic != "hello world from user" {
		t.Fatalf("retitled to %q", m.titleTopic)
	}
}

func TestSessionTitledOverridesUserMessageFallback(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.UserMessage{Text: "raw prompt text"})
	m.applyEvent(protocol.SessionTitled{Title: "concise title"})
	if m.titleTopic != "concise title" {
		t.Fatalf("titleTopic = %q, want concise title", m.titleTopic)
	}
}

func TestContextSessionValuePrefersTitle(t *testing.T) {
	const ellipsis = "…"
	got := contextSessionValue("fix auth", "long-session-id-abcdef", 40, ellipsis, "—")
	if got != "fix auth" {
		t.Errorf("got %q, want title", got)
	}
	got = contextSessionValue("", "sess-abcdef123456", 40, ellipsis, "—")
	if !strings.Contains(got, "abcdef") && !strings.Contains(got, "sess") {
		t.Errorf("id fallback = %q", got)
	}
	if got := contextSessionValue("", "", 40, ellipsis, "—"); got != "—" {
		t.Errorf("empty = %q", got)
	}
}

func TestContextWindowShowsSessionTitle(t *testing.T) {
	th := theme.Default()
	w := newContextWindow().resize(40, 16)
	updated, _ := w.update(contextStateMsg{
		WorkDir: "/tmp", SessionID: "sess-xyz", SessionTitle: "ship the feature",
		Provider: "echo", Model: "echo",
	})
	plain := ansi.Strip(updated.view(th))
	if !strings.Contains(plain, "ship the feature") {
		t.Errorf("context window missing session title:\n%s", plain)
	}
	if strings.Contains(plain, "sess-xyz") {
		t.Errorf("context window should prefer title over id:\n%s", plain)
	}
}
