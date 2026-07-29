package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"testing"
	"time"

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

func TestAppendReplayPreservesCorrelation(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "correlated-session")
	if err != nil {
		t.Fatal(err)
	}
	events := []protocol.Event{
		protocol.UserMessage{Correlation: protocol.Correlation{SessionID: "session-1", TurnID: "turn-1"}, Text: "hello"},
		protocol.TextDelta{Correlation: protocol.Correlation{SessionID: "session-1", TurnID: "turn-1", ProviderRequestID: "provider-1"}, Text: "world"},
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
	if !reflect.DeepEqual(got, events) {
		t.Errorf("Replay() = %#v, want %#v", got, events)
	}
}

func TestReplayLiteralLegacyJSONLHasEmptyCorrelation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.jsonl")
	contents := "" +
		`{"type":"user.message","time":"2020-01-01T00:00:00Z","data":{"text":"hello"}}` + "\n" +
		`{"type":"turn.completed","time":"2020-01-01T00:00:01Z","data":{"stopReason":"end_turn"}}` + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	events, err := Replay(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("Replay() returned %d events, want 2", len(events))
	}
	for i, ev := range events {
		var corr protocol.Correlation
		switch ev := ev.(type) {
		case protocol.UserMessage:
			corr = ev.Correlation
		case protocol.TurnCompleted:
			corr = ev.Correlation
		default:
			t.Fatalf("legacy event %d = %T", i, ev)
		}
		if corr != (protocol.Correlation{}) {
			t.Errorf("legacy event %d correlation = %#v, want empty", i, corr)
		}
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(b, &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"sessionId", "turnId", "providerRequestId"} {
			if _, ok := fields[key]; ok {
				t.Errorf("legacy event %d unexpectedly has %s: %s", i, key, b)
			}
		}
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
	const calls = 256
	ids := make([]string, 0, calls)
	seen := make(map[string]bool, calls)
	pattern := regexp.MustCompile(`^(\d{8}T\d{6}(?:\.\d{1,9})?Z)-([A-Za-z0-9_-]{8,})$`)
	for i := 0; i < calls; i++ {
		id := NewID()
		match := pattern.FindStringSubmatch(id)
		if match == nil {
			t.Fatalf("NewID() = %q, want UTC timestamp-first and filename-safe random suffix", id)
		}
		stamp, err := time.Parse("20060102T150405.999999999Z", match[1])
		if err != nil {
			t.Fatalf("NewID() timestamp %q: %v", match[1], err)
		}
		if stamp.Location() != time.UTC {
			t.Errorf("NewID() timestamp location = %v, want UTC", stamp.Location())
		}
		if seen[id] {
			t.Fatalf("NewID() repeated %q after %d calls", id, i+1)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(ids, sorted) {
		t.Errorf("NewID() values are not lexically sortable in creation order")
	}
}

func TestAppendReplaySessionTitled(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "titled-session")
	if err != nil {
		t.Fatal(err)
	}
	events := []protocol.Event{
		protocol.UserMessage{Text: "  hello   world  "},
		protocol.SessionTitled{Title: "hello world"},
		protocol.TurnStarted{},
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
	if TitleFromEvents(got) != "hello world" {
		t.Errorf("TitleFromEvents = %q", TitleFromEvents(got))
	}
}

func TestReplaySliceAndLastBounded(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "slice-session")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := st.Append(protocol.UserMessage{Text: "m" + string(rune('0'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	path := st.Path()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	events, total, err := ReplaySlice(path, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	if total != 10 {
		t.Fatalf("total = %d, want 10", total)
	}
	if len(events) != 4 {
		t.Fatalf("len = %d, want 4", len(events))
	}
	if um, ok := events[0].(protocol.UserMessage); !ok || um.Text != "m3" {
		t.Fatalf("first = %#v", events[0])
	}

	tail, total, err := ReplayLast(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if total != 10 || len(tail) != 3 {
		t.Fatalf("tail total=%d len=%d", total, len(tail))
	}
	if um, ok := tail[0].(protocol.UserMessage); !ok || um.Text != "m7" {
		t.Fatalf("tail first = %#v", tail[0])
	}
	if um, ok := tail[2].(protocol.UserMessage); !ok || um.Text != "m9" {
		t.Fatalf("tail last = %#v", tail[2])
	}

	if _, _, err := ReplaySlice(path, -1, 2); err == nil {
		t.Fatal("expected negative offset error")
	}
	if _, _, err := ReplayLast(path, 0); err == nil {
		t.Fatal("expected n<=0 error")
	}
}

func TestDefaultDirResolvesStrikeDirSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".strike")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := DefaultDir()
	want := filepath.Join(target, "sessions")
	if got != want {
		t.Errorf("DefaultDir() = %q, want %q", got, want)
	}
}
