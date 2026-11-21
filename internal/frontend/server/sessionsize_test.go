package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/pkg/protocol"
)

func sizeLog(t *testing.T, evs ...protocol.Event) string {
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

func usage(sid string, used int) protocol.Event {
	return protocol.UsageReported{
		Correlation: protocol.Correlation{SessionID: sid},
		Input:       protocol.KnownTokens(used),
		Used:        protocol.KnownTokens(used),
		Source:      protocol.UsageSourceActual,
	}
}

func TestLastUsageTokens(t *testing.T) {
	own := protocol.Correlation{SessionID: "s1"}
	tests := []struct {
		name  string
		log   string
		extra string
		want  int64
	}{
		{name: "no usage recorded", log: sizeLog(t, protocol.UserMessage{Correlation: own, Text: "hi"}), want: 0},
		{name: "last usage wins", log: sizeLog(t, usage("s1", 1000), usage("s1", 150000)), want: 150000},
		{name: "child usage in a parent log is ignored", log: sizeLog(t, usage("s1", 90000), usage("child", 400000)), want: 90000},
		{name: "correlation without session id counts as own", log: sizeLog(t, usage("", 4200)), want: 4200},
		{
			name: "unknown used count is unknown",
			log: sizeLog(t, protocol.UsageReported{
				Correlation: own, Used: protocol.UnknownTokens(),
			}),
			want: 0,
		},
		{
			name: "compaction after the last usage makes it unknown",
			log:  sizeLog(t, usage("s1", 180000), protocol.CompactionCompleted{Correlation: own, Reason: "auto", Removed: 40, Kept: 4}),
			want: 0,
		},
		{
			name: "usage after a compaction counts",
			log:  sizeLog(t, usage("s1", 180000), protocol.CompactionCompleted{Correlation: own, Reason: "auto"}, usage("s1", 30000)),
			want: 30000,
		},
		{
			name:  "malformed trailing lines are skipped",
			log:   sizeLog(t, usage("s1", 77000)),
			extra: "{\"type\":\"usage.reported\",broken\nnot json at all\n",
			want:  77000,
		},
		{name: "no trailing newline", log: strings.TrimSuffix(sizeLog(t, usage("s1", 5000)), "\n"), want: 5000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastUsageTokens([]byte(tt.log+tt.extra), "s1"); got != tt.want {
				t.Fatalf("lastUsageTokens = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSessionHistoryTokensTailAndCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")
	// A usage line followed by more than historyTailBytes of other events:
	// the usage falls outside the tail and must report unknown, not a guess.
	big := sizeLog(t, usage("s1", 120000))
	filler := strings.Repeat(`{"type":"text.delta","data":{"text":"`+strings.Repeat("x", 1000)+`"}}`+"\n", (historyTailBytes/1000)+50)
	if err := os.WriteFile(path, []byte(big+filler), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := sessionHistoryTokens(path, "s1"); got != 0 {
		t.Fatalf("usage outside the tail = %d, want 0", got)
	}
	// Appending a new usage changes size: the cache must re-read.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimPrefix(sizeLog(t, usage("s1", 64000)), `{"type":"session.header","data":{"schemaVersion":1}}`+"\n")
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := sessionHistoryTokens(path, "s1"); got != 64000 {
		t.Fatalf("after append = %d, want 64000", got)
	}
	if got := sessionHistoryTokens(filepath.Join(dir, "missing.jsonl"), "missing"); got != 0 {
		t.Fatalf("missing log = %d, want 0", got)
	}
	if got := sessionHistoryTokens("", "x"); got != 0 {
		t.Fatalf("empty path = %d, want 0", got)
	}
}

func TestSessionsListCarriesHistoryTokens(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "big.jsonl"), []byte(sizeLog(t,
		protocol.UserMessage{Correlation: protocol.Correlation{SessionID: "big"}, Text: "hi"},
		usage("big", 160000),
	)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "small.jsonl"), []byte(sizeLog(t,
		protocol.UserMessage{Correlation: protocol.Correlation{SessionID: "small"}, Text: "hi"},
	)), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := testServer(t, dir, "")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for _, s := range body.Sessions {
		got[s["id"].(string)] = s["historyTokens"]
	}
	if v, ok := got["big"].(float64); !ok || v != 160000 {
		t.Fatalf("big historyTokens = %v, want 160000 (%s)", got["big"], rec.Body.String())
	}
	if v, present := got["small"]; present && v != nil {
		t.Fatalf("small historyTokens = %v, want omitted", v)
	}
	if _, listed := got["small"]; !listed {
		t.Fatalf("small session missing from list: %s", rec.Body.String())
	}
}
