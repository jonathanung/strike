package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
)

func writeGrepFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func grepper(t *testing.T, work string) host.ContentSearcher {
	t.Helper()
	cs, ok := NewFiles(work).(host.ContentSearcher)
	if !ok {
		t.Fatal("files service does not implement host.ContentSearcher")
	}
	return cs
}

func matchKeys(ms []host.GrepMatch) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, fmt.Sprintf("%s:%d", m.Path, m.Line))
	}
	return out
}

func TestGrepFilesMatchesCaseInsensitivelyAcrossFiles(t *testing.T) {
	work := t.TempDir()
	writeGrepFixture(t, work, map[string]string{
		"a.go":           "package a\n// FlagHandled here\n",
		"sub/b.md":       "nothing\nsee flaghandled too\nand FLAGHANDLED\n",
		"c.txt":          "no match\n",
		".git/config":    "flaghandled in vcs internals\n",
		"node_modules/x": "flaghandled in noise dir\n",
	})
	res, err := grepper(t, work).GrepFiles(context.Background(), "flagHandled", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(matchKeys(res.Matches), ",")
	want := "a.go:2,sub/b.md:2,sub/b.md:3"
	if got != want {
		t.Fatalf("matches = %s, want %s", got, want)
	}
	if res.Capped {
		t.Fatal("small tree must not report capped")
	}
	if res.Matches[0].Snippet != "// FlagHandled here" {
		t.Fatalf("snippet = %q", res.Matches[0].Snippet)
	}
	if res.FilesScanned != 3 {
		t.Fatalf("filesScanned = %d, want 3", res.FilesScanned)
	}
}

func TestGrepFilesNeverReadsOutsideRoot(t *testing.T) {
	base := t.TempDir()
	work := filepath.Join(base, "work")
	outside := filepath.Join(base, "outside")
	writeGrepFixture(t, work, map[string]string{"in.txt": "inside token\n"})
	writeGrepFixture(t, outside, map[string]string{"secret.txt": "outside token\n"})
	mustSymlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(work, "link.txt"))
	mustSymlink(t, outside, filepath.Join(work, "outdir"))
	res, err := grepper(t, work).GrepFiles(context.Background(), "token", 0)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(matchKeys(res.Matches), ",")
	if got != "in.txt:1" {
		t.Fatalf("matches = %s, want only in.txt:1 (symlinked file/dir must be skipped)", got)
	}
}

func TestGrepFilesHonorsAllowedRootCeiling(t *testing.T) {
	work := t.TempDir()
	writeGrepFixture(t, work, map[string]string{
		"allowed/a.txt": "needle\n",
		"denied/b.txt":  "needle\n",
	})
	files := NewFiles(work)
	SetFilesRootCeiling(files, []string{filepath.Join(work, "allowed")}, true)
	res, err := files.(host.ContentSearcher).GrepFiles(context.Background(), "needle", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(matchKeys(res.Matches), ","); got != "allowed/a.txt:1" {
		t.Fatalf("matches = %s, want only allowed/a.txt:1", got)
	}
}

func TestGrepFilesSkipsBinary(t *testing.T) {
	work := t.TempDir()
	writeGrepFixture(t, work, map[string]string{
		"bin.dat":  "needle\x00\x01\x02",
		"text.txt": "needle\n",
	})
	res, err := grepper(t, work).GrepFiles(context.Background(), "needle", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(matchKeys(res.Matches), ","); got != "text.txt:1" {
		t.Fatalf("matches = %s, want only text.txt:1", got)
	}
}

func TestGrepFilesCapsAreHonest(t *testing.T) {
	many := strings.Repeat("hit\n", maxGrepPerFile+5)
	cases := []struct {
		name       string
		files      map[string]string
		limit      int
		wantN      int
		wantCapped bool
	}{
		{"under total limit", map[string]string{"a": "hit\nhit\n", "b": "hit\n"}, 3, 3, false},
		{"exactly at limit is not capped", map[string]string{"a": "hit\nhit\nhit\n"}, 3, 3, false},
		{"over total limit", map[string]string{"a": "hit\nhit\n", "b": "hit\nhit\n"}, 3, 3, true},
		{"per-file cap", map[string]string{"a": many}, 0, maxGrepPerFile, true},
		{"limit above max is clamped", map[string]string{"a": "hit\n"}, maxGrepResults * 10, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			writeGrepFixture(t, work, tc.files)
			res, err := grepper(t, work).GrepFiles(context.Background(), "hit", tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Matches) != tc.wantN || res.Capped != tc.wantCapped {
				t.Fatalf("got %d matches capped=%v, want %d capped=%v", len(res.Matches), res.Capped, tc.wantN, tc.wantCapped)
			}
		})
	}
}

func TestGrepFilesOversizeFileReportsCapped(t *testing.T) {
	work := t.TempDir()
	big := strings.Repeat("x", maxGrepFileBytes) + "\nneedle past the scan window\n"
	writeGrepFixture(t, work, map[string]string{"big.txt": big})
	res, err := grepper(t, work).GrepFiles(context.Background(), "needle", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 0 || !res.Capped {
		t.Fatalf("got %d matches capped=%v, want 0 and capped (file prefix only)", len(res.Matches), res.Capped)
	}
}

func TestGrepFilesCancelledContextReportsCapped(t *testing.T) {
	work := t.TempDir()
	writeGrepFixture(t, work, map[string]string{"a.txt": "needle\n"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := grepper(t, work).GrepFiles(ctx, "needle", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Capped || len(res.Matches) != 0 {
		t.Fatalf("got %d matches capped=%v, want 0 and capped", len(res.Matches), res.Capped)
	}
}

func TestGrepFilesRejectsBadQueries(t *testing.T) {
	work := t.TempDir()
	cases := []struct{ name, q string }{
		{"empty", ""},
		{"blank", "   "},
		{"multi-line", "a\nb"},
		{"too long", strings.Repeat("q", maxGrepQueryBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := grepper(t, work).GrepFiles(context.Background(), tc.q, 0); err == nil {
				t.Fatalf("query %q: want error", tc.q)
			}
		})
	}
}

func TestGrepSnippetClipsAroundMatch(t *testing.T) {
	long := strings.Repeat("a", 500) + "NEEDLE" + strings.Repeat("b", 500)
	cases := []struct {
		name, line, needle string
		check              func(string) bool
	}{
		{"short trimmed", "  \tshort needle line \r", "needle", func(s string) bool { return s == "short needle line" }},
		{"long keeps match", long, "needle", func(s string) bool {
			return strings.Contains(s, "NEEDLE") && strings.HasPrefix(s, "…") && strings.HasSuffix(s, "…") &&
				len([]rune(s)) == maxGrepSnippetRune+2
		}},
		{"long match at start", "NEEDLE" + strings.Repeat("c", 400), "needle", func(s string) bool {
			return strings.HasPrefix(s, "NEEDLE") && strings.HasSuffix(s, "…")
		}},
		{"invalid utf8 sanitized", "ok \xff needle", "needle", func(s string) bool { return s == "ok � needle" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := grepSnippet(tc.line, tc.needle); !tc.check(got) {
				t.Fatalf("grepSnippet = %q", got)
			}
		})
	}
}
