package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestAppendReplayRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "test-session")
	if err != nil {
		t.Fatal(err)
	}
	events := []protocol.Event{
		protocol.UserMessage{Text: "hello"},
		protocol.TurnStarted{},
		protocol.TextDelta{Text: "world"},
		protocol.TurnCompleted{StopReason: "end_turn"},
	}
	for _, ev := range events {
		if err := st.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	path := st.Path()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := Replay(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(events) {
		t.Fatalf("replayed %d events, want %d", len(got), len(events))
	}
	if um, ok := got[0].(protocol.UserMessage); !ok || um.Text != "hello" {
		t.Errorf("first event = %#v", got[0])
	}
	if td, ok := got[2].(protocol.TextDelta); !ok || td.Text != "world" {
		t.Errorf("third event = %#v", got[2])
	}
}

func TestOpenCreatesDirAndFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "sessions")
	st, err := Open(dir, "abc")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	want := filepath.Join(dir, "abc.jsonl")
	if st.Path() != want {
		t.Errorf("path = %q, want %q", st.Path(), want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}

func TestReplayMalformedLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := os.WriteFile(path, []byte("not-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Replay(path); err == nil {
		t.Fatal("expected error for malformed line")
	}
}

func TestReplayUnknownEventType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown.jsonl")
	line := `{"type":"nope","time":"2020-01-01T00:00:00Z","data":{}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Replay(path); err == nil {
		t.Fatal("expected error for unknown event type")
	}
}

func TestReplayMissingFile(t *testing.T) {
	if _, err := Replay(filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestNewIDFormat(t *testing.T) {
	id := NewID()
	if len(id) != len("20060102-150405") {
		t.Errorf("NewID() = %q, unexpected length", id)
	}
}
