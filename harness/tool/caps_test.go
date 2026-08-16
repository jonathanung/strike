package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// floodCmd prints exactly n bytes of 'x' on stdout (no trailing newline).
func floodCmd(n int) string {
	return fmt.Sprintf("dd if=/dev/zero bs=%d count=1 status=none | tr '\\0' 'x'", n)
}

func TestBashOutputCap(t *testing.T) {
	dir := t.TempDir()
	n := bashMaxOutput + 4000
	res, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": floodCmd(n),
	}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "output truncated") {
		t.Fatalf("expected truncation marker, output len=%d head=%q", len(res.Output), trimForErr(res.Output, 80))
	}
	// Retained payload is everything before the "\n… (output truncated" suffix.
	const suffix = "\n… (output truncated"
	marker := strings.Index(res.Output, suffix)
	if marker < 0 {
		t.Fatal("missing truncation ellipsis")
	}
	if marker != bashMaxOutput {
		t.Fatalf("retained %d bytes, want %d", marker, bashMaxOutput)
	}
	if !strings.Contains(res.Output[marker:], "bytes total") {
		t.Fatalf("expected bytes-total note, got %q", res.Output[marker:])
	}
}

func TestProcessDefaultMaxOutput(t *testing.T) {
	n := processDefaultMaxOutput + 2500
	res, err := RunProcess(context.Background(), ProcessSpec{
		Argv:    []string{"bash", "-c", floodCmd(n)},
		Combine: true,
		// MaxOutput zero → processDefaultMaxOutput
	}, ProcessObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("expected Truncated")
	}
	if len(res.Output) != processDefaultMaxOutput {
		t.Fatalf("len(output)=%d, want %d", len(res.Output), processDefaultMaxOutput)
	}
	if res.BytesSeen < n {
		t.Fatalf("BytesSeen=%d, want >= %d", res.BytesSeen, n)
	}
}

func TestReadDefaultLimitTruncation(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	total := readDefaultLimit + 40
	for i := 1; i <= total; i++ {
		fmt.Fprintf(&b, "line-%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := NewRead().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "big.txt",
	}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, fmt.Sprintf("1\tline-1")) {
		t.Fatalf("missing first line: %q", trimForErr(res.Output, 60))
	}
	if !strings.Contains(res.Output, fmt.Sprintf("%d\tline-%d", readDefaultLimit, readDefaultLimit)) {
		t.Fatalf("missing last default line %d", readDefaultLimit)
	}
	if strings.Contains(res.Output, fmt.Sprintf("line-%d", readDefaultLimit+1)) {
		t.Fatal("default limit should not include line past cap")
	}
	if !strings.Contains(res.Output, "more lines not shown") {
		t.Fatalf("expected continuation note, got %q", res.Output[len(res.Output)-120:])
	}
}

func TestReadMaxLineLen(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("z", readMaxLineLen+200)
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(long+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := NewRead().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "long.txt",
	}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "…") {
		t.Fatalf("expected line truncation ellipsis, got %q", res.Output)
	}
	// Line content after "1\t" and before "…" should be readMaxLineLen runes/bytes of z.
	idx := strings.Index(res.Output, "\t")
	if idx < 0 {
		t.Fatalf("bad output %q", res.Output)
	}
	rest := res.Output[idx+1:]
	cut := strings.Index(rest, "…")
	if cut != readMaxLineLen {
		t.Fatalf("truncated line body len=%d, want %d", cut, readMaxLineLen)
	}
}

func TestGrepMaxMatchesTruncation(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < grepMaxMatches+25; i++ {
		fmt.Fprintf(&b, "match-line-%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "hits.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := NewGrep().Execute(context.Background(), mustJSON(t, map[string]any{
		"pattern": "match-line",
		"path":    dir,
	}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, fmt.Sprintf("results truncated to %d", grepMaxMatches)) {
		t.Fatalf("expected truncation note, got %q", res.Output)
	}
	// Count match lines (path:line: content style).
	count := 0
	for _, line := range strings.Split(res.Output, "\n") {
		if strings.Contains(line, "match-line-") {
			count++
		}
	}
	if count != grepMaxMatches {
		t.Fatalf("match lines=%d, want %d", count, grepMaxMatches)
	}
}

func TestGlobMaxResultsTruncation(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < globMaxResults+30; i++ {
		name := filepath.Join(dir, fmt.Sprintf("f%04d.txt", i))
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := NewGlob().Execute(context.Background(), mustJSON(t, map[string]any{
		"pattern": "*.txt",
		"path":    dir,
	}), allowAll(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, fmt.Sprintf("results truncated to %d", globMaxResults)) {
		t.Fatalf("expected truncation note, got %q", res.Output)
	}
	paths := 0
	for _, line := range strings.Split(strings.TrimSpace(res.Output), "\n") {
		if strings.HasSuffix(line, ".txt") {
			paths++
		}
	}
	if paths != globMaxResults {
		t.Fatalf("paths=%d, want %d", paths, globMaxResults)
	}
}

func TestWebFetchOutputCapBound(t *testing.T) {
	// Sanity: constant is the tighter produce-time budget (not the old 100k).
	if webfetchMaxOutputRunes > 50_000 {
		t.Fatalf("webfetchMaxOutputRunes=%d looks too large for token-efficiency defaults", webfetchMaxOutputRunes)
	}
	if webfetchMaxBody > 3<<20 {
		t.Fatalf("webfetchMaxBody=%d looks too large", webfetchMaxBody)
	}
	// Ensure truncateRunes honors the cap (unit of the produce path).
	s := strings.Repeat("字", webfetchMaxOutputRunes+100)
	out := truncateRunes(s, webfetchMaxOutputRunes)
	if utf8.RuneCountInString(out) <= webfetchMaxOutputRunes {
		// marker adds runes; body before marker must be <= cap
	}
	if !strings.Contains(out, "output truncated") {
		t.Fatalf("expected marker, got runeCount=%d", utf8.RuneCountInString(out))
	}
	body := strings.Split(out, "\n\n…")[0]
	if utf8.RuneCountInString(body) != webfetchMaxOutputRunes {
		t.Fatalf("body runes=%d, want %d", utf8.RuneCountInString(body), webfetchMaxOutputRunes)
	}
}

func TestProduceTimeCapDefaultsDocumented(t *testing.T) {
	// Guard against accidental drift back to pre-#439 ceilings.
	cases := []struct {
		name string
		got  int
		max  int
	}{
		{"bashMaxOutput", bashMaxOutput, 20_000},
		{"processDefaultMaxOutput", processDefaultMaxOutput, 20_000},
		{"readDefaultLimit", readDefaultLimit, 1000},
		{"readMaxLineLen", readMaxLineLen, 1500},
		{"grepMaxMatches", grepMaxMatches, 150},
		{"globMaxResults", globMaxResults, 150},
		{"webfetchMaxOutputRunes", webfetchMaxOutputRunes, 50_000},
	}
	for _, tc := range cases {
		if tc.got <= 0 || tc.got > tc.max {
			t.Errorf("%s=%d, want in (0, %d]", tc.name, tc.got, tc.max)
		}
	}
	if bashMaxOutput != processDefaultMaxOutput {
		t.Errorf("bashMaxOutput (%d) should match processDefaultMaxOutput (%d)", bashMaxOutput, processDefaultMaxOutput)
	}
}

func trimForErr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
