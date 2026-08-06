package sdk_test

import (
	"path/filepath"
	"testing"

	"github.com/jonathanung/strike-cli/pkg/protocol"
	"github.com/jonathanung/strike-cli/pkg/sdk"
)

func TestReadWriteSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")

	want := []protocol.Event{
		protocol.UserMessage{Text: "hi"},
		protocol.TextDelta{Text: "yo"},
		protocol.TurnCompleted{StopReason: "end_turn"},
	}
	if err := sdk.WriteSession(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := sdk.ReadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d", len(got))
	}
	um, ok := got[0].(protocol.UserMessage)
	if !ok || um.Text != "hi" {
		t.Fatalf("got[0] = %#v", got[0])
	}
	td, ok := got[1].(protocol.TextDelta)
	if !ok || td.Text != "yo" {
		t.Fatalf("got[1] = %#v", got[1])
	}
}

func TestReadSessionMissing(t *testing.T) {
	_, err := sdk.ReadSession(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err == nil {
		t.Fatal("expected error")
	}
}
