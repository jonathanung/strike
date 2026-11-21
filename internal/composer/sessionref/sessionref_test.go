package sessionref

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/composer/triggers"
	"github.com/jonathanung/strike/internal/persist/session"
)

func writeLog(t *testing.T, dir, id string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(session.LogPath(dir, id), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ev(typ, data string) string {
	return `{"type":"` + typ + `","time":"2026-11-17T09:30:00Z","data":` + data + `}`
}

func TestReadSummaryTitleAndLastTurns(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "s1",
		ev("session.titled", `{"title":"Auth investigation"}`),
		ev("user.message", `{"text":"first question"}`),
		ev("turn.started", `{}`),
		ev("text.delta", `{"text":"first "}`),
		ev("text.delta", `{"text":"answer"}`),
		ev("turn.completed", `{}`),
		ev("compaction.completed", `{"reason":"auto","summary":"Refresh races logout."}`),
		ev("user.message", `{"text":"second question"}`),
		ev("text.delta", `{"text":"second answer"}`),
		ev("tool.begin", `{"name":"bash"}`),
		ev("text.delta", `{"text":"after tool"}`),
		`not json`,
		ev("user.message", `{"text":"third question"}`),
	)
	cases := []struct {
		turns     int
		wantTurns []string
		omitted   int
	}{
		{turns: 10, wantTurns: []string{"User: first question", "Assistant: first answer", "User: second question", "Assistant: second answer", "Assistant: after tool", "User: third question"}},
		{turns: 2, wantTurns: []string{"Assistant: after tool", "User: third question"}, omitted: 4},
	}
	for _, tc := range cases {
		ref, err := Read(dir, "s1", tc.turns)
		if err != nil {
			t.Fatal(err)
		}
		if ref.Title != "Auth investigation" || ref.Summary != "Refresh races logout." || ref.ID != "s1" {
			t.Fatalf("ref = %#v", ref)
		}
		var got []string
		for _, turn := range ref.Turns {
			got = append(got, turn.Role+": "+turn.Text)
		}
		if strings.Join(got, "|") != strings.Join(tc.wantTurns, "|") || ref.Omitted != tc.omitted {
			t.Fatalf("turns(%d) = %v omitted %d, want %v omitted %d", tc.turns, got, ref.Omitted, tc.wantTurns, tc.omitted)
		}
		if ref.Updated.IsZero() {
			t.Fatal("updated not set")
		}
	}
}

func TestReadMissingAndInvalid(t *testing.T) {
	dir := t.TempDir()
	if _, err := Read(dir, "pruned", 10); !errors.Is(err, triggers.ErrSessionNotFound) {
		t.Fatalf("missing err = %v", err)
	}
	for _, id := range []string{"", "../etc", "a/b", `a\b`} {
		if _, err := Read(dir, id, 10); err == nil {
			t.Fatalf("Read(%q) accepted", id)
		}
	}
	// A file outside the directory is never reached through a crafted id.
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "outside.jsonl"), []byte("{}\n"), 0o644); err == nil {
		if _, err := Read(dir, "../outside", 10); err == nil {
			t.Fatal("escaped the session dir")
		}
	}
}

func TestReaderDefaultsTurns(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for i := 0; i < 15; i++ {
		lines = append(lines, ev("user.message", `{"text":"q"}`))
	}
	writeLog(t, dir, "many", lines...)
	ref, err := Reader(dir, 0)("many")
	if err != nil {
		t.Fatal(err)
	}
	if len(ref.Turns) != DefaultTurns || ref.Omitted != 5 {
		t.Fatalf("turns = %d omitted = %d", len(ref.Turns), ref.Omitted)
	}
}
