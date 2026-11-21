package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/protocol"
)

const reviewTwoHunkDiff = `diff --git a/alpha.txt b/alpha.txt
index 1111111..2222222 100644
--- a/alpha.txt
+++ b/alpha.txt
@@ -1,3 +1,3 @@
-one
+ONE
 two
 three
@@ -10,3 +10,4 @@ ctx
 ten
 eleven
+eleven-and-a-half
 twelve
diff --git a/img.png b/img.png
index 3333333..4444444 100644
Binary files a/img.png and b/img.png differ
diff --git a/run.sh b/run.sh
old mode 100644
new mode 100755
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
index 5555555..0000000
--- a/gone.txt
+++ /dev/null
@@ -1 +0,0 @@
-bye
\ No newline at end of file
`

func TestParseReviewDiff(t *testing.T) {
	files := parseReviewDiff(reviewTwoHunkDiff)
	if len(files) != 4 {
		t.Fatalf("files = %d, want 4: %+v", len(files), files)
	}
	cases := []struct {
		path  string
		hunks int
		note  string
	}{
		{"alpha.txt", 2, ""},
		{"img.png", 0, "binary"},
		{"run.sh", 0, "mode or rename only"},
		{"gone.txt", 1, ""},
	}
	for i, tc := range cases {
		f := files[i]
		if f.path != tc.path || len(f.hunks) != tc.hunks || !strings.Contains(f.note, tc.note) {
			t.Errorf("file %d = path %q hunks %d note %q; want %q %d %q", i, f.path, len(f.hunks), f.note, tc.path, tc.hunks, tc.note)
		}
	}
	alpha := files[0].hunks
	if alpha[0].OldStart != 1 || alpha[0].NewLines != 3 || alpha[1].NewStart != 10 || alpha[1].NewLines != 4 {
		t.Fatalf("hunk ranges = %+v", alpha)
	}
	if len(alpha[1].Lines) != 4 || alpha[1].Lines[2] != "+eleven-and-a-half" {
		t.Fatalf("hunk 2 lines = %q", alpha[1].Lines)
	}
	if alpha[0].ID == alpha[1].ID || !strings.HasPrefix(alpha[0].ID, "h") {
		t.Fatalf("hunk ids = %q %q", alpha[0].ID, alpha[1].ID)
	}
	gone := files[3].hunks[0]
	if gone.OldLines != 1 || gone.NewLines != 0 || gone.Lines[len(gone.Lines)-1] != `\ No newline at end of file` {
		t.Fatalf("deleted hunk = %+v", gone)
	}
	if got := strings.Join(files[3].header, "\n"); !strings.Contains(got, "+++ /dev/null") || !strings.Contains(got, "deleted file mode") {
		t.Fatalf("deleted header = %q", got)
	}
}

func TestParseReviewDiffIDsSurviveLineShifts(t *testing.T) {
	shifted := strings.Replace(reviewTwoHunkDiff, "@@ -10,3 +10,4 @@ ctx", "@@ -40,3 +40,4 @@ ctx", 1)
	a := parseReviewDiff(reviewTwoHunkDiff)[0].hunks[1].ID
	b := parseReviewDiff(shifted)[0].hunks[1].ID
	if a != b {
		t.Fatalf("id changed with line numbers: %q vs %q", a, b)
	}
	dup := "diff --git a/d.txt b/d.txt\n--- a/d.txt\n+++ b/d.txt\n@@ -1 +1 @@\n-x\n+y\n@@ -9 +9 @@\n-x\n+y\n"
	h := parseReviewDiff(dup)[0].hunks
	if len(h) != 2 || h[0].ID == h[1].ID || h[1].ID != h[0].ID+"-2" {
		t.Fatalf("duplicate bodies ids = %+v", h)
	}
}

func TestReviewTurnText(t *testing.T) {
	doc := reviewDoc{
		Rejected: []reviewRejectedJSON{{Path: "b.go", Header: "@@ -1 +1 @@", Lines: []string{"-old", "+new"}}},
		Comments: []reviewCommentJSON{
			{Path: "a.go", StartLine: 3, EndLine: 3, Body: "rename this"},
			{Path: "b.go", StartLine: 5, EndLine: 9, Body: "two\nlines"},
		},
	}
	got := reviewTurnText(doc)
	for _, want := range []string{
		ReviewTurnHeader,
		"### b.go\n- Rejected and reverted `@@ -1 +1 @@`:\n```diff\n-old\n+new\n```",
		"### a.go\n- Comment on line 3:\n  > rename this",
		"- Comment on lines 5-9:\n  > two\n  > lines",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("turn text missing %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, ReviewTurnHeader) {
		t.Fatalf("turn does not open with the header: %q", got)
	}
	if strings.Index(got, "### b.go") > strings.Index(got, "### a.go") {
		t.Fatalf("files not in first-mention order (rejections first):\n%s", got)
	}
	long := reviewDoc{Rejected: []reviewRejectedJSON{{Path: "x", Header: "@@", Lines: make([]string, reviewTurnLinesMax+5)}}}
	if !strings.Contains(reviewTurnText(long), "(5 more lines)") {
		t.Fatal("long rejected hunk not capped")
	}
}

func TestReviewStoreRejectsUnsafeSessionIDs(t *testing.T) {
	st := newReviewStore(t.TempDir())
	for _, id := range []string{"", "..", "../x", "a/b", strings.Repeat("x", 200)} {
		if _, err := st.get(id); err == nil {
			t.Errorf("session id %q accepted", id)
		}
	}
}

func TestReviewRoutesOnThrowawayRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	sessions := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "line " + string(rune('a'+i))
	}
	original := strings.Join(lines, "\n") + "\n"
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, "alpha.txt"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-b", "main")
	write(original)
	git("add", "alpha.txt")
	git("commit", "-m", "initial")
	edited := append([]string(nil), lines...)
	edited[0] = "FIRST CHANGE"
	edited[18] = "SECOND CHANGE"
	write(strings.Join(edited, "\n") + "\n")

	ops := make(chan protocol.Op, 4)
	live := NewLive("sess1", repo, nil, ops)
	srv, err := New(Options{SessionDir: sessions, Live: live})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, req)
		return res
	}
	queue := func() reviewQueueJSON {
		t.Helper()
		res := call(http.MethodGet, "/v1/review", "")
		if res.Code != http.StatusOK {
			t.Fatalf("review = %d %s", res.Code, res.Body.String())
		}
		var q reviewQueueJSON
		if err := json.Unmarshal(res.Body.Bytes(), &q); err != nil {
			t.Fatal(err)
		}
		return q
	}

	q := queue()
	if q.ReadOnly || q.SessionID != "sess1" || len(q.Files) != 1 || len(q.Files[0].Hunks) != 2 {
		t.Fatalf("queue = %+v", q)
	}
	first, second := q.Files[0].Hunks[0], q.Files[0].Hunks[1]

	if res := call(http.MethodPost, "/v1/review/state", `{"hunk":"`+second.ID+`","state":"accepted"}`); res.Code != http.StatusOK {
		t.Fatalf("accept = %d %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodPost, "/v1/review/state", `{"hunk":"x","state":"rejected"}`); res.Code != http.StatusBadRequest {
		t.Fatalf("bad state = %d", res.Code)
	}
	if got := queue().Files[0].Hunks[1].State; got != reviewStateAccepted {
		t.Fatalf("accepted state = %q", got)
	}

	// Reject is refused mid-turn, then reverts exactly one hunk when idle.
	live.Publish(protocol.TurnStarted{})
	if res := call(http.MethodPost, "/v1/review/reject", `{"path":"alpha.txt","hunk":"`+first.ID+`"}`); res.Code != http.StatusConflict {
		t.Fatalf("busy reject = %d %s", res.Code, res.Body.String())
	}
	live.Publish(protocol.TurnCompleted{})
	if res := call(http.MethodPost, "/v1/review/reject", `{"path":"../x","hunk":"`+first.ID+`"}`); res.Code != http.StatusBadRequest {
		t.Fatalf("escape reject = %d", res.Code)
	}
	if res := call(http.MethodPost, "/v1/review/reject", `{"path":"alpha.txt","hunk":"hnope"}`); res.Code != http.StatusConflict {
		t.Fatalf("stale hunk reject = %d %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodPost, "/v1/review/reject", `{"path":"alpha.txt","hunk":"`+first.ID+`"}`); res.Code != http.StatusOK {
		t.Fatalf("reject = %d %s", res.Code, res.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(repo, "alpha.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); strings.Contains(got, "FIRST CHANGE") || !strings.Contains(got, "SECOND CHANGE") || !strings.HasPrefix(got, "line a\n") {
		t.Fatalf("after reject = %q", got)
	}
	q = queue()
	if len(q.Files) != 1 || len(q.Files[0].Hunks) != 1 || q.Files[0].Hunks[0].ID != second.ID || len(q.Rejected) != 1 {
		t.Fatalf("queue after reject = %+v", q)
	}

	// Comments: validation, add, delete.
	for _, bad := range []string{
		`{"path":"alpha.txt","startLine":0,"endLine":1,"body":"x"}`,
		`{"path":"alpha.txt","startLine":5,"endLine":4,"body":"x"}`,
		`{"path":"alpha.txt","startLine":1,"endLine":1,"body":"   "}`,
		`{"path":"/etc/passwd","startLine":1,"endLine":1,"body":"x"}`,
	} {
		if res := call(http.MethodPost, "/v1/review/comments", bad); res.Code != http.StatusBadRequest {
			t.Errorf("bad comment %s = %d", bad, res.Code)
		}
	}
	add := call(http.MethodPost, "/v1/review/comments", `{"path":"alpha.txt","hunk":"`+second.ID+`","startLine":17,"endLine":19,"body":"use a constant"}`)
	if add.Code != http.StatusOK {
		t.Fatalf("comment = %d %s", add.Code, add.Body.String())
	}
	var kept reviewCommentJSON
	if err := json.Unmarshal(add.Body.Bytes(), &kept); err != nil || kept.ID == "" {
		t.Fatalf("comment body = %s %v", add.Body.String(), err)
	}
	drop := call(http.MethodPost, "/v1/review/comments", `{"path":"alpha.txt","startLine":2,"endLine":2,"body":"drop me"}`)
	var dropped reviewCommentJSON
	_ = json.Unmarshal(drop.Body.Bytes(), &dropped)
	if res := call(http.MethodDelete, "/v1/review/comments/"+dropped.ID, ""); res.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", res.Code, res.Body.String())
	}
	if res := call(http.MethodDelete, "/v1/review/comments/"+dropped.ID, ""); res.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d", res.Code)
	}

	// The store survives a daemon restart.
	reopened := newReviewStore(sessions)
	doc, err := reopened.get("sess1")
	if err != nil || len(doc.Comments) != 1 || len(doc.Rejected) != 1 || !doc.Accepted[second.ID] {
		t.Fatalf("reloaded doc = %+v %v", doc, err)
	}

	// Send: refused mid-turn, then exactly one user turn, then cleared.
	live.Publish(protocol.TurnStarted{})
	if res := call(http.MethodPost, "/v1/review/send", `{}`); res.Code != http.StatusConflict {
		t.Fatalf("busy send = %d %s", res.Code, res.Body.String())
	}
	if len(ops) != 0 {
		t.Fatal("busy send submitted an op")
	}
	live.Publish(protocol.TurnCompleted{})
	if res := call(http.MethodPost, "/v1/review/send", `{}`); res.Code != http.StatusOK {
		t.Fatalf("send = %d %s", res.Code, res.Body.String())
	}
	if len(ops) != 1 {
		t.Fatalf("send submitted %d ops, want 1", len(ops))
	}
	in, ok := (<-ops).(protocol.UserInput)
	if !ok {
		t.Fatal("send did not submit a user input")
	}
	for _, want := range []string{ReviewTurnHeader, "### alpha.txt", "Rejected and reverted", "+FIRST CHANGE", "Comment on lines 17-19:\n  > use a constant"} {
		if !strings.Contains(in.Text, want) {
			t.Errorf("turn missing %q:\n%s", want, in.Text)
		}
	}
	if strings.Contains(in.Text, "drop me") {
		t.Fatal("deleted comment was sent")
	}
	q = queue()
	if len(q.Comments) != 0 || len(q.Rejected) != 0 || q.Files[0].Hunks[0].State != "" {
		t.Fatalf("queue not cleared after send: %+v", q)
	}
	if _, err := os.Stat(filepath.Join(sessions, "review", "sess1.json")); !os.IsNotExist(err) {
		t.Fatalf("store file still present after send: %v", err)
	}
	if res := call(http.MethodPost, "/v1/review/send", `{}`); res.Code != http.StatusBadRequest {
		t.Fatalf("empty send = %d", res.Code)
	}
}

func TestReviewReadOnlyAttach(t *testing.T) {
	bare, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, target, body string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		bare.Handler().ServeHTTP(res, httptest.NewRequest(method, target, strings.NewReader(body)))
		return res
	}
	res := call(http.MethodGet, "/v1/review", "")
	if res.Code != http.StatusOK {
		t.Fatalf("read-only review = %d %s", res.Code, res.Body.String())
	}
	var q reviewQueueJSON
	if err := json.Unmarshal(res.Body.Bytes(), &q); err != nil || !q.ReadOnly || q.Reason == "" {
		t.Fatalf("read-only queue = %+v %v", q, err)
	}
	for _, tc := range []struct{ method, target, body string }{
		{http.MethodPost, "/v1/review/state", `{"hunk":"h1","state":"accepted"}`},
		{http.MethodPost, "/v1/review/reject", `{"path":"a","hunk":"h1"}`},
		{http.MethodPost, "/v1/review/comments", `{"path":"a","startLine":1,"endLine":1,"body":"x"}`},
		{http.MethodDelete, "/v1/review/comments/c1", ""},
		{http.MethodPost, "/v1/review/send", `{}`},
	} {
		if res := call(tc.method, tc.target, tc.body); res.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", tc.method, tc.target, res.Code)
		}
	}
}
