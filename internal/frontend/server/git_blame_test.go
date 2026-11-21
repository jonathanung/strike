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

func TestParseBlamePorcelain(t *testing.T) {
	a := strings.Repeat("a", 40)
	z := strings.Repeat("0", 40)
	out := a + " 1 1 2\n" +
		"author Ada Lovelace\n" +
		"author-mail <ada@example.com>\n" +
		"author-time 1700000000\n" +
		"author-tz +0000\n" +
		"summary First draft\n" +
		"filename f.txt\n" +
		"\tline one\n" +
		a + " 2 2\n" +
		"\tline two\n" +
		z + " 3 3 1\n" +
		"author Not Committed Yet\n" +
		"author-time 1700000100\n" +
		"summary Version of f.txt from f.txt\n" +
		"filename f.txt\n" +
		"\tauthor looks like a header but is content\n"
	got := parseBlamePorcelain([]byte(out))
	if len(got) != 3 {
		t.Fatalf("lines = %d, want 3: %+v", len(got), got)
	}
	want := []gitBlameLineJSON{
		{Line: 1, Hash: a, Author: "Ada Lovelace", Date: "2023-11-14T22:13:20Z", Time: 1700000000, Subject: "First draft"},
		// Repeat hashes carry no headers; info comes from the first sighting.
		{Line: 2, Hash: a, Author: "Ada Lovelace", Date: "2023-11-14T22:13:20Z", Time: 1700000000, Subject: "First draft"},
		{Line: 3, Hash: z, Author: "Not Committed Yet", Date: "2023-11-14T22:15:00Z", Time: 1700000100, Subject: "Version of f.txt from f.txt", Uncommitted: true},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %+v, want %+v", i+1, got[i], want[i])
		}
	}
	if len(parseBlamePorcelain(nil)) != 0 {
		t.Error("empty output should parse to no lines")
	}
}

func TestIsBlameHashAndPositiveQueryInt(t *testing.T) {
	cases := map[string]bool{
		strings.Repeat("a", 40): true,
		strings.Repeat("f", 64): true,
		strings.Repeat("a", 39): false,
		strings.Repeat("A", 40): false,
		"author":                false,
	}
	for in, want := range cases {
		if got := isBlameHash(in); got != want {
			t.Errorf("isBlameHash(%q) = %v, want %v", in, got, want)
		}
	}
	for _, tc := range []struct {
		raw      string
		def, max int
		want     int
		err      bool
	}{
		{"", 7, 10, 7, false},
		{"3", 7, 10, 3, false},
		{"99", 7, 10, 10, false},
		{"99", 7, 0, 99, false},
		{"0", 7, 10, 0, true},
		{"-1", 7, 10, 0, true},
		{"x", 7, 10, 0, true},
	} {
		got, err := positiveQueryInt(tc.raw, tc.def, tc.max)
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("positiveQueryInt(%q,%d,%d) = %d,%v want %d err=%v", tc.raw, tc.def, tc.max, got, err, tc.want, tc.err)
		}
	}
}

func TestGitBlameRouteOnMultiAuthorRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	git := func(author string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME="+author, "GIT_AUTHOR_EMAIL=a@example.com",
			"GIT_COMMITTER_NAME="+author, "GIT_COMMITTER_EMAIL=a@example.com",
			"GIT_AUTHOR_DATE=2024-01-02T03:04:05Z", "GIT_COMMITTER_DATE=2024-01-02T03:04:05Z",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("Ada", "init", "-b", "main")
	write("notes.txt", "one\ntwo\n")
	git("Ada", "add", "notes.txt")
	git("Ada", "commit", "-m", "ada writes")
	write("notes.txt", "one\ntwo\nthree\n")
	git("Grace", "commit", "-am", "grace adds three")
	write("notes.txt", "one\ntwo\nthree\nfour\n")
	write("loose.txt", "x\n")

	live := NewLive("s1", repo, nil, make(chan protocol.Op, 1))
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live})
	if err != nil {
		t.Fatal(err)
	}
	get := func(target string) (int, gitBlameJSON, string) {
		t.Helper()
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, target, nil))
		var body gitBlameJSON
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		return res.Code, body, res.Body.String()
	}

	code, body, raw := get("/v1/git/blame?path=notes.txt")
	if code != http.StatusOK || !body.Available {
		t.Fatalf("blame = %d %s", code, raw)
	}
	if body.Total != 4 || len(body.Lines) != 4 || body.Truncated || body.Head == "" {
		t.Fatalf("blame window = %+v", body)
	}
	wantAuthors := []string{"Ada", "Ada", "Grace", "Not Committed Yet"}
	for i, ln := range body.Lines {
		if ln.Line != i+1 || ln.Author != wantAuthors[i] {
			t.Errorf("line %d = %+v, want author %s", i+1, ln, wantAuthors[i])
		}
	}
	if body.Lines[0].Subject != "ada writes" || body.Lines[2].Subject != "grace adds three" {
		t.Errorf("subjects = %q / %q", body.Lines[0].Subject, body.Lines[2].Subject)
	}
	if body.Lines[0].Date != "2024-01-02T03:04:05Z" || body.Lines[0].Time != 1704164645 || len(body.Lines[0].Hash) < 40 {
		t.Errorf("line 1 date/hash = %+v", body.Lines[0])
	}
	if !body.Lines[3].Uncommitted || body.Lines[2].Uncommitted {
		t.Errorf("uncommitted flags = %+v", body.Lines)
	}

	code, body, raw = get("/v1/git/blame?path=notes.txt&start=2&limit=2")
	if code != http.StatusOK || body.Start != 2 || len(body.Lines) != 2 || body.Lines[0].Line != 2 || !body.Truncated {
		t.Fatalf("windowed blame = %d %s", code, raw)
	}
	code, body, raw = get("/v1/git/blame?path=notes.txt&start=50")
	if code != http.StatusOK || len(body.Lines) != 0 || body.Truncated {
		t.Fatalf("past-the-end window = %d %s", code, raw)
	}

	code, body, raw = get("/v1/git/blame?path=loose.txt")
	if code != http.StatusOK || body.Available || !strings.Contains(body.Reason, "not tracked") {
		t.Fatalf("untracked blame = %d %s", code, raw)
	}

	for _, target := range []string{
		"/v1/git/blame?path=../../etc/passwd",
		"/v1/git/blame",
		"/v1/git/blame?path=notes.txt&limit=0",
		"/v1/git/blame?path=notes.txt&start=x",
	} {
		if code, _, raw := get(target); code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", target, code, raw)
		}
	}
}

func TestGitBlameOutsideRepoIsUnavailable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, err := gitBlame(t.Context(), dir, "a.txt", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if body.Available || body.Reason == "" || len(body.Lines) != 0 {
		t.Fatalf("outside repo = %+v", body)
	}
}
