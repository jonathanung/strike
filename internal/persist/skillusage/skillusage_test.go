package skillusage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/pkg/protocol"
)

type timed struct {
	at time.Time
	ev protocol.Event
}

func at(stamp string, ev protocol.Event) timed {
	when, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		panic(err)
	}
	return timed{at: when, ev: ev}
}

func writeLog(t *testing.T, dir, id string, events []timed, extra ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"type":"session.header","schemaVersion":1,"time":"2026-01-01T00:00:00Z"}` + "\n")
	for _, item := range events {
		env, err := protocol.Wrap(item.ev)
		if err != nil {
			t.Fatal(err)
		}
		env.Time = item.at.UTC()
		line, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	for _, line := range extra {
		b.WriteString(line + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func corr(session, turn string) protocol.Correlation {
	return protocol.Correlation{SessionID: session, TurnID: turn}
}

func skillCall(session, callID, args string) protocol.ToolCallBegin {
	return protocol.ToolCallBegin{Correlation: corr(session, "t"), CallID: callID, Name: "skill", Args: json.RawMessage(args)}
}

func TestAggregateCountsUsedAndNeverUsedSkills(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "a", []timed{
		at("2026-11-01T10:00:00Z", protocol.UserMessage{Correlation: corr("a", "t1"), Text: "/review the diff"}),
		at("2026-11-02T10:00:00Z", protocol.UserMessage{Correlation: corr("a", "t2"), Text: "$review"}),
		at("2026-11-02T11:00:00Z", protocol.UserMessage{Correlation: corr("a", "t3"), Text: "plain text mentioning /review later"}),
		at("2026-11-02T12:00:00Z", protocol.UserMessage{Correlation: corr("a", "t4"), Text: "/unknown-thing"}),
	})
	writeLog(t, dir, "b", []timed{
		at("2026-11-05T09:00:00Z", skillCall("b", "c1", `{"name":"review"}`)),
		at("2026-11-05T09:01:00Z", skillCall("b", "c2", `{"name":"review","resource":"ref.md"}`)),
		at("2026-11-05T09:02:00Z", protocol.ToolCallBegin{Correlation: corr("b", "t"), CallID: "c3", Name: "read", Args: json.RawMessage(`{"name":"review"}`)}),
	})

	got, err := Aggregate(dir, []string{"review", "ship", " "})
	if err != nil {
		t.Fatal(err)
	}
	if got.Sessions != 2 {
		t.Fatalf("sessions = %d, want 2", got.Sessions)
	}
	review := got.Skills["review"]
	if review.Count != 3 {
		t.Fatalf("review count = %d, want 3 (two composer runs + one tool call)", review.Count)
	}
	if want := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC); !review.LastUsed.Equal(want) {
		t.Fatalf("review last = %v, want %v", review.LastUsed, want)
	}
	ship, ok := got.Skills["ship"]
	if !ok || ship.Count != 0 || !ship.LastUsed.IsZero() {
		t.Fatalf("ship = %+v ok=%v, want listed with zero usage", ship, ok)
	}
	if _, ok := got.Skills[""]; ok {
		t.Fatal("blank name must not be listed")
	}
	if _, ok := got.Skills["unknown-thing"]; ok {
		t.Fatal("unknown names must not be counted")
	}
}

func TestAggregateCountsChildCopiesOnce(t *testing.T) {
	dir := t.TempDir()
	child := skillCall("child", "c9", `{"name":"review"}`)
	msg := protocol.UserMessage{Correlation: corr("child", "t9"), Text: "/review"}
	// The child's own log and the parent's copy of the same events.
	writeLog(t, dir, "child", []timed{at("2026-11-06T08:00:00Z", child), at("2026-11-06T07:00:00Z", msg)})
	writeLog(t, dir, "root", []timed{at("2026-11-06T08:00:00Z", child), at("2026-11-06T07:00:00Z", msg)})
	// A child run recorded only on the parent still counts.
	writeLog(t, dir, "root2", []timed{at("2026-11-07T08:00:00Z", skillCall("orphan-child", "c1", `{"name":"review"}`))})

	got, err := Aggregate(dir, []string{"review"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Skills["review"].Count != 3 {
		t.Fatalf("count = %d, want 3", got.Skills["review"].Count)
	}
}

func TestAggregateUsesFileIDWhenEventHasNoSession(t *testing.T) {
	dir := t.TempDir()
	same := protocol.UserMessage{Correlation: corr("", "t1"), Text: "/review"}
	writeLog(t, dir, "one", []timed{at("2026-11-01T00:00:00Z", same)})
	writeLog(t, dir, "two", []timed{at("2026-11-01T00:00:00Z", same)})
	got, err := Aggregate(dir, []string{"review"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Skills["review"].Count != 2 {
		t.Fatalf("count = %d, want 2 (different sessions)", got.Skills["review"].Count)
	}
}

func TestAggregateSkipsBadLinesAndOtherFiles(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, dir, "a", []timed{
		at("2026-11-01T00:00:00Z", protocol.UserMessage{Correlation: corr("a", "t1"), Text: "/review"}),
	}, "{not json", `{"type":"tool.begin","data":{"name":"skill","args":"not-an-object","callId":"x"}}`)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(`{"type":"user.message","data":{"text":"/review"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Aggregate(dir, []string{"review"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Skills["review"].Count != 1 || got.Sessions != 1 {
		t.Fatalf("got %+v", got)
	}
	if got.SkippedLines != 1 {
		t.Fatalf("skipped = %d, want 1", got.SkippedLines)
	}
}

func TestAggregateSkipsOverlongLines(t *testing.T) {
	old := lineLimit
	lineLimit = 256
	t.Cleanup(func() { lineLimit = old })
	dir := t.TempDir()
	big := protocol.UserMessage{Correlation: corr("a", "t0"), Text: "/review " + strings.Repeat("x", 400)}
	writeLog(t, dir, "a", []timed{
		at("2026-11-01T00:00:00Z", big),
		at("2026-11-02T00:00:00Z", protocol.UserMessage{Correlation: corr("a", "t1"), Text: "/review"}),
	})
	got, err := Aggregate(dir, []string{"review"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Skills["review"].Count != 1 || got.SkippedLines != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestAggregateMissingDirIsEmpty(t *testing.T) {
	got, err := Aggregate(filepath.Join(t.TempDir(), "nope"), []string{"review"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Sessions != 0 || got.Skills["review"].Count != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestInvokedSkill(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/review", "review"},
		{"  /review the diff", "review"},
		{"$ship now", "ship"},
		{"/ review", ""},
		{"/", ""},
		{"$", ""},
		{"review", ""},
		{"", ""},
		{"see /review", ""},
	}
	for _, tc := range cases {
		if got := InvokedSkill(tc.in); got != tc.want {
			t.Errorf("InvokedSkill(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
