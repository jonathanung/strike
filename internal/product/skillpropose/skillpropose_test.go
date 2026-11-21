package skillpropose

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/pkg/protocol"
)

const secret = "sk-abcdefghijklmnopqrstuvwxyz0123456789"

var base = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

type seed struct {
	id, project, parent, title string
	at                         time.Time
	opening                    string
	tools                      []string
	later                      []string // tools after the first turn
}

func writeSeed(t *testing.T, dir string, s seed) {
	t.Helper()
	var lines []string
	add := func(at time.Time, ev protocol.Event) {
		env, err := protocol.Wrap(ev)
		if err != nil {
			t.Fatal(err)
		}
		env.Time = at
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(raw))
	}
	c := protocol.Correlation{SessionID: s.id, TurnID: "t1"}
	add(s.at, protocol.UserMessage{Correlation: c, Text: s.opening})
	for i, tool := range s.tools {
		add(s.at.Add(time.Duration(i+1)*time.Second), protocol.ToolCallBegin{Correlation: c, CallID: tool + "-call", Name: tool, Args: json.RawMessage(`{}`)})
	}
	add(s.at.Add(time.Minute), protocol.TurnCompleted{Correlation: c})
	for _, tool := range s.later {
		add(s.at.Add(2*time.Minute), protocol.ToolCallBegin{Correlation: protocol.Correlation{SessionID: s.id, TurnID: "t2"}, CallID: "x", Name: tool, Args: json.RawMessage(`{}`)})
	}
	if err := os.WriteFile(filepath.Join(dir, s.id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteMeta(dir, s.id, session.Meta{ProjectKey: s.project, ParentSessionID: s.parent, Title: s.title}); err != nil {
		t.Fatal(err)
	}
}

func day(n int) time.Time { return base.Add(time.Duration(n) * 24 * time.Hour) }

var releaseTools = []string{"bash", "read", "edit", "bash"}

func plantedHistory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, s := range []seed{
		{id: "s1", project: "ws", title: "Release 1", at: day(0), opening: "Bump the version, update the changelog and tag the release", tools: releaseTools},
		{id: "s2", project: "ws", title: "Release 2", at: day(3), opening: "please bump version, update changelog, tag release", tools: []string{"bash", "read", "edit", "edit", "bash"}},
		{id: "s3", project: "ws", title: "Release 3", at: day(7), opening: "Bump version and changelog then tag the release", tools: releaseTools, later: []string{"webfetch"}},
		// Near misses: same words, different tools; same tools, different words.
		{id: "n1", project: "ws", at: day(8), opening: "Bump the version, update the changelog and tag the release", tools: []string{"webfetch", "websearch"}},
		{id: "n2", project: "ws", at: day(9), opening: "Explain the flaky scheduler test failure", tools: releaseTools},
		// Not counted: a child session, another workspace, a one-tool turn.
		{id: "c1", project: "ws", parent: "s1", at: day(4), opening: "Bump version update changelog tag release", tools: releaseTools},
		{id: "o1", project: "other", at: day(5), opening: "Bump version update changelog tag release", tools: releaseTools},
		{id: "t1", project: "ws", at: day(6), opening: "Bump version update changelog tag release", tools: []string{"bash"}},
	} {
		writeSeed(t, dir, s)
	}
	return dir
}

func TestReadWorkflowsScopesToWorkspaceRootsAndFirstTurn(t *testing.T) {
	dir := plantedHistory(t)
	wfs, err := ReadWorkflows(dir, "ws", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, wf := range wfs {
		ids = append(ids, wf.SessionID)
	}
	if got := strings.Join(ids, ","); got != "s1,s2,s3,n1,n2" {
		t.Fatalf("sessions = %s", got)
	}
	if got := strings.Join(wfs[1].Tools, ">"); got != "bash>read>edit>bash" {
		t.Fatalf("collapsed tools = %s", got)
	}
	if got := strings.Join(wfs[2].Tools, ">"); got != "bash>read>edit>bash" {
		t.Fatalf("first turn only, got %s", got)
	}
	if wfs, _ := ReadWorkflows(dir, "", time.Time{}); len(wfs) != 0 {
		t.Fatalf("empty project key must read nothing, got %d", len(wfs))
	}
	if wfs, _ := ReadWorkflows(dir, "ws", day(7)); len(wfs) != 3 {
		t.Fatalf("since filter: got %d, want 3", len(wfs))
	}
	if wfs, err := ReadWorkflows(filepath.Join(dir, "missing"), "ws", time.Time{}); err != nil || wfs != nil {
		t.Fatalf("missing dir: %v %v", wfs, err)
	}
}

func TestMineProposesOnlyTheRealRepetition(t *testing.T) {
	wfs, err := ReadWorkflows(plantedHistory(t), "ws", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	now := day(10)
	got := Mine(wfs, 3, now)
	if len(got) != 1 {
		t.Fatalf("proposals = %d, want 1: %+v", len(got), got)
	}
	p := got[0]
	if p.Count != 3 || len(p.Sessions) != 3 || p.Sessions[0].SessionID != "s1" || p.Sessions[2].Title != "Release 3" {
		t.Fatalf("evidence = %+v", p.Sessions)
	}
	if p.Name != "bump-version-update" {
		t.Fatalf("name = %q", p.Name)
	}
	for _, want := range []string{"Drafted from 3 similar sessions", "## Steps", "1. Use the `bash` tool.", "4. Use the `bash` tool.", "$ARGUMENTS"} {
		if !strings.Contains(p.Body, want) {
			t.Fatalf("body missing %q:\n%s", want, p.Body)
		}
	}
	if !strings.HasPrefix(p.Description, "Repeated workflow: Bump the version") {
		t.Fatalf("description = %q", p.Description)
	}
	if p.ID == "" || p.DraftedBy == "" || !p.CreatedAt.Equal(now) {
		t.Fatalf("meta = %+v", p)
	}
	if len(Mine(wfs, 4, now)) != 0 {
		t.Fatal("threshold 4 must not propose a 3-session group")
	}
	if len(Mine(nil, 3, now)) != 0 {
		t.Fatal("no history, no proposal")
	}
	if len(Mine(wfs[:1], 1, now)) != 0 {
		t.Fatal("threshold is clamped to MinThreshold; one session never proposes")
	}
}

func TestSecretBearingRepetitionArrivesRedacted(t *testing.T) {
	dir := t.TempDir()
	for i, id := range []string{"a", "b", "c"} {
		writeSeed(t, dir, seed{id: id, project: "ws", title: "deploy " + secret, at: day(i), opening: "Deploy staging with token " + secret + " and verify health", tools: []string{"bash", "webfetch"}})
	}
	wfs, err := ReadWorkflows(dir, "ws", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	got := Mine(wfs, 3, day(5))
	if len(got) != 1 {
		t.Fatalf("proposals = %d", len(got))
	}
	raw, _ := json.Marshal(got[0])
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("secret leaked into draft: %s", raw)
	}
	if strings.Contains(got[0].Name, "redacted") {
		t.Fatalf("name built from a redaction marker: %q", got[0].Name)
	}
}

func TestSimilarityHelpers(t *testing.T) {
	cases := []struct {
		a, b []string
		j    float64
		s    float64
	}{
		{[]string{"a", "b"}, []string{"a", "b"}, 1, 1},
		{[]string{"a", "b"}, []string{"c"}, 0, 0},
		{nil, []string{"a"}, 0, 0},
		{[]string{"a", "b", "c", "d"}, []string{"a", "c"}, 0.5, 2 * 2.0 / 6},
	}
	for _, tc := range cases {
		if got := jaccard(tc.a, tc.b); got != tc.j {
			t.Errorf("jaccard(%v,%v) = %v, want %v", tc.a, tc.b, got, tc.j)
		}
		if got := seqOverlap(tc.a, tc.b); got != tc.s {
			t.Errorf("seqOverlap(%v,%v) = %v, want %v", tc.a, tc.b, got, tc.s)
		}
	}
	if got := strings.Join(words("Please FIX the build; fix it now v2 abc123 x"), ","); got != "fix,build" {
		t.Fatalf("words = %s", got)
	}
}

func TestStoreOptInScanDismissAccept(t *testing.T) {
	wfs, err := ReadWorkflows(plantedHistory(t), "ws", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	st := NewStore(filepath.Join(t.TempDir(), "state", "skill-proposals.json"))
	ws, err := st.Get("ws")
	if err != nil || ws.Enabled || ws.EffectiveThreshold() != DefaultThreshold {
		t.Fatalf("default state = %+v %v", ws, err)
	}
	if _, err := st.Scan("ws", wfs, nil, day(10)); !errors.Is(err, ErrDisabled) {
		t.Fatalf("scan while off = %v, want ErrDisabled", err)
	}
	on := true
	if _, err := st.Settings("ws", &on, nil); err != nil {
		t.Fatal(err)
	}
	bad := 1
	if _, err := st.Settings("ws", nil, &bad); err == nil {
		t.Fatal("threshold 1 must be refused")
	}
	ws, err = st.Scan("ws", wfs, nil, day(10))
	if err != nil || len(ws.Pending) != 1 || !ws.LastScan.Equal(day(10)) {
		t.Fatalf("scan = %+v %v", ws, err)
	}
	id := ws.Pending[0].ID
	if p, err := st.Find("ws", id); err != nil || p.ID != id {
		t.Fatalf("find = %+v %v", p, err)
	}

	// An installed skill with the drafted name is not proposed again.
	ws, _ = st.Scan("ws", wfs, map[string]bool{"bump-version-update": true}, day(10))
	if len(ws.Pending) != 0 {
		t.Fatalf("installed name re-proposed: %+v", ws.Pending)
	}

	// Accept (take without dismiss) removes it; a rescan may propose it again.
	ws, _ = st.Scan("ws", wfs, nil, day(10))
	if _, err := st.Take("ws", id, false); err != nil {
		t.Fatal(err)
	}
	if ws, _ = st.Get("ws"); len(ws.Pending) != 0 || len(ws.Dismissed) != 0 {
		t.Fatalf("after accept = %+v", ws)
	}

	// Dismiss suppresses the pattern on every later scan.
	ws, _ = st.Scan("ws", wfs, nil, day(10))
	if _, err := st.Take("ws", id, true); err != nil {
		t.Fatal(err)
	}
	ws, _ = st.Scan("ws", wfs, nil, day(11))
	if len(ws.Pending) != 0 {
		t.Fatalf("dismissed pattern reappeared: %+v", ws.Pending)
	}
	if _, err := st.Take("ws", "nope", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id = %v", err)
	}

	// Other workspaces are untouched, and the file is private.
	if other, _ := st.Get("other"); other.Enabled {
		t.Fatal("opt-in leaked across workspaces")
	}
	info, err := os.Stat(st.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %v %v", info.Mode(), err)
	}
}
