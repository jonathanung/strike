package session

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestTitleFromText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "whitespace only", in: "  \n\t  ", want: ""},
		{name: "simple", in: "fix the auth flow", want: "fix the auth flow"},
		{name: "trim and collapse", in: "  hello   \n world  ", want: "hello world"},
		{name: "strip controls", in: "hi\x1b[2J\x00there", want: "hi[2Jthere"},
		{name: "nbsp to space", in: "a\u00a0b", want: "a b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TitleFromText(tt.in); got != tt.want {
				t.Errorf("TitleFromText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTitleFromTextTruncates(t *testing.T) {
	in := strings.Repeat("字", titleMaxRunes+10)
	got := TitleFromText(in)
	if n := utf8.RuneCountInString(got); n != titleMaxRunes {
		t.Fatalf("rune count = %d, want %d (%q)", n, titleMaxRunes, got)
	}
}

func TestTitleFromEvents(t *testing.T) {
	events := []protocol.Event{
		protocol.UserMessage{Text: "  first   prompt  "},
		protocol.TurnStarted{},
		protocol.UserMessage{Text: "second should not win"},
	}
	if got := TitleFromEvents(events); got != "first prompt" {
		t.Errorf("from user message = %q, want first prompt", got)
	}

	events = append(events, protocol.SessionTitled{Title: "renamed title"})
	if got := TitleFromEvents(events); got != "renamed title" {
		t.Errorf("SessionTitled should win = %q", got)
	}

	events = append(events, protocol.SessionTitled{Title: "final name"})
	if got := TitleFromEvents(events); got != "final name" {
		t.Errorf("last SessionTitled should win = %q", got)
	}

	if got := TitleFromEvents(nil); got != "" {
		t.Errorf("empty events = %q", got)
	}
	if got := TitleFromEvents([]protocol.Event{protocol.TurnStarted{}}); got != "" {
		t.Errorf("no user content = %q", got)
	}
}
