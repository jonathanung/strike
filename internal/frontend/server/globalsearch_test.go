package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/pkg/protocol"
)

const searchSecret = "sk-ant-api03-SEARCHSECRETVALUE9876"

func searchLog(t *testing.T, evs ...protocol.Event) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"type":"session.header","data":{"schemaVersion":1}}` + "\n")
	for _, ev := range evs {
		env, err := protocol.Wrap(ev)
		if err != nil {
			t.Fatal(err)
		}
		line, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestSessionSearchMessages(t *testing.T) {
	own := protocol.Correlation{SessionID: "s1"}
	child := protocol.Correlation{SessionID: "kid", ParentSessionID: "s1", Depth: 1}
	log := searchLog(t,
		protocol.UserMessage{Correlation: own, Text: "find the coral bookmark"},
		protocol.TurnStarted{Correlation: own},
		protocol.TextDelta{Correlation: own, Text: "The coral "},
		protocol.ToolCallBegin{Correlation: own, CallID: "1", Name: "bash", Args: json.RawMessage(`{"command":"echo toolnoise"}`)},
		protocol.ToolCallEnd{Correlation: own, CallID: "1", Output: "toolnoise output"},
		protocol.TextDelta{Correlation: own, Text: "bookmark is in docs."},
		protocol.TextDelta{Correlation: child, Text: "child chatter"},
		protocol.TurnCompleted{Correlation: own, StopReason: "end_turn"},
		protocol.UserMessage{Correlation: own, Text: "my key is " + searchSecret},
	) + "{broken json\n" + `{"type":"user.message","data":{"text":12}}` + "\n"
	msgs, skipped, err := sessionSearchMessages(strings.NewReader(log), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 1 {
		// "{broken json" has no interesting marker and is never decoded; the
		// mistyped user.message is.
		t.Fatalf("skipped = %d, want 1", skipped)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs = %+v", msgs)
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" || msgs[2].Role != "user" {
		t.Fatalf("roles = %+v", msgs)
	}
	if msgs[1].Text != "The coral bookmark is in docs." {
		t.Fatalf("assistant text = %q", msgs[1].Text)
	}
	for _, m := range msgs {
		if strings.Contains(m.Text, "toolnoise") || strings.Contains(m.Text, "child chatter") {
			t.Fatalf("tool output or child text leaked into corpus: %q", m.Text)
		}
		if strings.Contains(m.Text, "SEARCHSECRET") {
			t.Fatalf("secret not redacted: %q", m.Text)
		}
	}
}

func TestSnippetAround(t *testing.T) {
	tests := []struct {
		name, text, match string
		wantPrefix        bool
		wantSuffix        bool
	}{
		{name: "short text, no ellipses", text: "the coral bookmark", match: "coral"},
		{name: "long text both sides", text: strings.Repeat("a ", 100) + "coral" + strings.Repeat(" b", 100), match: "coral", wantPrefix: true, wantSuffix: true},
		{name: "multibyte runes are not split", text: strings.Repeat("界", 80) + "coral" + strings.Repeat("界", 150), match: "coral", wantPrefix: true, wantSuffix: true},
		{name: "newlines flattened", text: "line one\nthe coral\tbookmark", match: "coral"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := strings.Index(tt.text, tt.match)
			snip, ms, me := snippetAround(tt.text, start, start+len(tt.match))
			if got := snip[ms:me]; got != tt.match {
				t.Fatalf("snippet[%d:%d] = %q, want %q (snippet %q)", ms, me, got, tt.match, snip)
			}
			if !utf8Valid(snip) {
				t.Fatalf("snippet is not valid UTF-8: %q", snip)
			}
			if strings.HasPrefix(snip, "…") != tt.wantPrefix || strings.HasSuffix(snip, "…") != tt.wantSuffix {
				t.Fatalf("ellipses wrong: %q", snip)
			}
			if strings.ContainsAny(snip, "\n\t\r") {
				t.Fatalf("snippet not flattened: %q", snip)
			}
		})
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

// memLogs serves session logs from memory for runGlobalSearch.
type memLogs map[string]string

func (m memLogs) open(info session.Info) (io.ReadCloser, error) {
	s, ok := m[info.ID]
	if !ok {
		return nil, errors.New("missing")
	}
	return io.NopCloser(strings.NewReader(s)), nil
}

func TestRunGlobalSearch(t *testing.T) {
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	c := func(id string) protocol.Correlation { return protocol.Correlation{SessionID: id} }
	logs := memLogs{
		"old":   searchLog(t, protocol.UserMessage{Correlation: c("old"), Text: "where is the Coral bookmark"}),
		"new":   searchLog(t, protocol.UserMessage{Correlation: c("new"), Text: "hello"}, protocol.TextDelta{Correlation: c("new"), Text: "the coral bookmark lives in docs; coral again"}, protocol.TurnCompleted{Correlation: c("new")}, protocol.UserMessage{Correlation: c("new"), Text: "coral once more"}),
		"none":  searchLog(t, protocol.UserMessage{Correlation: c("none"), Text: "nothing here"}),
		"child": searchLog(t, protocol.UserMessage{Correlation: c("child"), Text: "coral in a child"}),
	}
	infos := []session.Info{
		{ID: "old", Title: "Old one", UpdatedAt: base, WorkspaceID: "ws1"},
		{ID: "new", UpdatedAt: base.Add(time.Hour)},
		{ID: "none", UpdatedAt: base.Add(2 * time.Hour)},
		{ID: "child", ParentSessionID: "new", UpdatedAt: base.Add(3 * time.Hour)},
		{ID: "gone", UpdatedAt: base.Add(4 * time.Hour)},
	}
	resp := runGlobalSearch("coral", infos, logs.open, time.Now)
	if resp.Sessions != 4 || resp.Scanned != 4 {
		t.Fatalf("sessions/scanned = %d/%d, want 4/4 (roots only)", resp.Sessions, resp.Scanned)
	}
	if resp.Capped || resp.Partial || resp.Limit != searchMaxHits {
		t.Fatalf("flags = %+v", resp)
	}
	if resp.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (unreadable log)", resp.Skipped)
	}
	if len(resp.Hits) != 2 || resp.Hits[0].ID != "new" || resp.Hits[1].ID != "old" {
		t.Fatalf("hits = %+v (want newest first, roots only)", resp.Hits)
	}
	newHit := resp.Hits[0]
	if newHit.Role != "assistant" || newHit.Matches != 2 || newHit.Title != "hello" {
		t.Fatalf("new hit = %+v", newHit)
	}
	if got := newHit.Snippet[newHit.MatchStart:newHit.MatchEnd]; got != "coral" {
		t.Fatalf("highlight = %q", got)
	}
	oldHit := resp.Hits[1]
	if oldHit.Title != "Old one" || oldHit.WorkspaceID != "ws1" || oldHit.UpdatedAt != base.UnixMilli() {
		t.Fatalf("old hit = %+v", oldHit)
	}
	if got := oldHit.Snippet[oldHit.MatchStart:oldHit.MatchEnd]; got != "Coral" {
		t.Fatalf("case-insensitive highlight = %q", got)
	}
}

func TestRunGlobalSearchCapsAndBudget(t *testing.T) {
	logs := memLogs{}
	var infos []session.Info
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < searchMaxHits+5; i++ {
		id := "s" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + string(rune('a'+i/26))
		logs[id] = searchLog(t, protocol.UserMessage{Correlation: protocol.Correlation{SessionID: id}, Text: "needle"})
		infos = append(infos, session.Info{ID: id, UpdatedAt: base.Add(time.Duration(i) * time.Minute)})
	}
	resp := runGlobalSearch("needle", infos, logs.open, time.Now)
	if len(resp.Hits) != searchMaxHits || !resp.Capped {
		t.Fatalf("hits=%d capped=%v, want %d capped", len(resp.Hits), resp.Capped, searchMaxHits)
	}

	// A clock past the budget after the first session stops the scan and
	// says so instead of spinning.
	calls := 0
	clock := func() time.Time {
		calls++
		if calls > 2 {
			return base.Add(time.Hour)
		}
		return base
	}
	resp = runGlobalSearch("needle", infos, logs.open, clock)
	if !resp.Partial || resp.Scanned >= len(infos) {
		t.Fatalf("partial=%v scanned=%d, want partial with fewer than %d scanned", resp.Partial, resp.Scanned, len(infos))
	}
}

func TestGlobalSearchRoute(t *testing.T) {
	dir := t.TempDir()
	write := func(id, body string) {
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c := func(id string) protocol.Correlation { return protocol.Correlation{SessionID: id} }
	write("alpha", searchLog(t, protocol.UserMessage{Correlation: c("alpha"), Text: "remember the coral bookmark"}))
	write("beta", searchLog(t, protocol.UserMessage{Correlation: c("beta"), Text: "secret " + searchSecret}))
	srv := testServer(t, dir, "")

	get := func(q string) (int, globalSearchResponse) {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/search?q="+q, nil))
		var resp globalSearchResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}
	for _, q := range []string{"", "a", strings.Repeat("x", searchMaxRunes+1)} {
		if code, _ := get(q); code != http.StatusBadRequest {
			t.Fatalf("q=%q status = %d, want 400", q, code)
		}
	}
	code, resp := get("coral")
	if code != http.StatusOK || len(resp.Hits) != 1 || resp.Hits[0].ID != "alpha" {
		t.Fatalf("coral: %d %+v", code, resp)
	}
	// The secret is redacted before matching: neither a hit nor a snippet.
	_, resp = get("SEARCHSECRET")
	if len(resp.Hits) != 0 {
		t.Fatalf("redacted secret matched: %+v", resp.Hits)
	}
	_, resp = get("secret")
	for _, h := range resp.Hits {
		if strings.Contains(h.Snippet, "SEARCHSECRET") {
			t.Fatalf("snippet leaked secret: %q", h.Snippet)
		}
	}
	// Freshness: a message appended after the first search is found by the
	// next one (no index to rebuild).
	f, err := os.OpenFile(filepath.Join(dir, "alpha.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	more := strings.TrimPrefix(searchLog(t, protocol.UserMessage{Correlation: c("alpha"), Text: "now the turquoise flag"}), `{"type":"session.header","data":{"schemaVersion":1}}`+"\n")
	if _, err := f.WriteString(more + "{malformed line\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	_, resp = get("turquoise")
	if len(resp.Hits) != 1 || resp.Hits[0].ID != "alpha" {
		t.Fatalf("fresh message not found: %+v", resp)
	}
}
