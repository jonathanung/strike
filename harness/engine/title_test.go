package engine

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBriefAgentSessionTitle(t *testing.T) {
	tests := []struct {
		name, agent, id, want string
	}{
		{name: "both", agent: "explore", id: "child-abcdef12xyz", want: "explore abcdef12"},
		{name: "agent only", agent: "build", id: "", want: "build"},
		{name: "id only", agent: "", id: "sess-12345678", want: "12345678"},
		{name: "empty", agent: "", id: "", want: "task"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := briefAgentSessionTitle(tt.agent, tt.id); got != tt.want {
				t.Errorf("briefAgentSessionTitle(%q,%q) = %q, want %q", tt.agent, tt.id, got, tt.want)
			}
		})
	}
}

func TestSessionTitleFromTextBriefCap(t *testing.T) {
	long := strings.Repeat("字", 40)
	got := sessionTitleFromText(long)
	if n := utf8.RuneCountInString(got); n != 32 {
		t.Fatalf("rune count = %d, want 32 (%q)", n, got)
	}
	if got := sessionTitleFromText("  fix   the\nauth flow  "); got != "fix the auth flow" {
		t.Errorf("simple = %q", got)
	}
}
