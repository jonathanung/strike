package tool

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/pkg/redact"
)

// fakeSpillSecret is credential-shaped test data (not a real key).
const fakeSpillSecret = "sk-ant-FAKEFAKEFAKE0000"

func spillCtx(t *testing.T) *Context {
	t.Helper()
	tc := allowAll(t.TempDir())
	tc.SessionTempDir = t.TempDir()
	return tc
}

func readSpill(t *testing.T, tc *Context, output string) string {
	t.Helper()
	path := SpillPathFromOutput(output)
	if path == "" {
		t.Fatalf("no spill hint in output tail: %q", tail(output, 400))
	}
	if !IsSpillPath(tc.SessionTempDir, path) {
		t.Fatalf("spill path %q not under %q", path, tc.SessionTempDir)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("spill perm = %o, want 600", perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func needBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
}

func TestBashSpillsOverflowReadableWithOffset(t *testing.T) {
	needBash(t)
	tc := spillCtx(t)
	// 3000 numbered lines (~40KB) overflow the 16KB produce cap; a secret sits
	// in the tail that the preview never retains.
	cmd := `for i in $(seq 1 3000); do echo "line-$i padding-padding"; done; echo "` + fakeSpillSecret + `"`
	res, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{"command": cmd}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "output truncated") {
		t.Fatalf("expected truncation note, got tail %q", tail(res.Output, 300))
	}
	if len(res.Output) > bashMaxOutput+1024 {
		t.Fatalf("history body %d bytes exceeds cap + hint", len(res.Output))
	}
	if strings.Contains(res.Output, "blob:sha256:") {
		t.Fatal("timeline blob ref leaked into tool output")
	}
	body := readSpill(t, tc, res.Output)
	if !strings.Contains(body, "line-1 padding") || !strings.Contains(body, "line-3000 padding") {
		t.Fatalf("spill missing head/tail lines (%d bytes)", len(body))
	}
	if strings.Contains(body, fakeSpillSecret) || !strings.Contains(body, "[REDACTED") {
		t.Fatal("spill body not secret-redacted")
	}

	// The model reopens the remainder with read + offset.
	path := SpillPathFromOutput(res.Output)
	rr, err := NewRead().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": path, "offset": 2999, "limit": 2,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rr.Output, "line-2999") || !strings.Contains(rr.Output, "line-3000") {
		t.Fatalf("read offset output = %q", rr.Output)
	}
}

func TestBashNoSpillWithoutScratchOrWhenSmall(t *testing.T) {
	needBash(t)
	big := `head -c 40000 /dev/zero | tr '\0' 'a'`
	cases := []struct {
		name    string
		tempDir func(t *testing.T) string
		cmd     string
	}{
		{"no session temp", func(*testing.T) string { return "" }, big},
		{"temp alloc failure (path is a file)", func(t *testing.T) string {
			f := filepath.Join(t.TempDir(), "not-a-dir")
			if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return f
		}, big},
		{"under cap", func(t *testing.T) string { return t.TempDir() }, "echo small"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tc := allowAll(t.TempDir())
			tc.SessionTempDir = tt.tempDir(t)
			res, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{"command": tt.cmd}), tc)
			if err != nil {
				t.Fatalf("spill failure must be non-fatal: %v", err)
			}
			if strings.Contains(res.Output, SpillHintPrefix) {
				t.Fatalf("unexpected spill hint: %q", tail(res.Output, 300))
			}
		})
	}
}

func TestSpillBudgetExhaustedDegrades(t *testing.T) {
	tc := spillCtx(t)
	dir := filepath.Join(tc.SessionTempDir, spillSubdir)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "filler.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// Sparse file: logical size counts against the budget without disk use.
	if err := f.Truncate(spillDirMaxBytes); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if hint := spillOverflow(tc, "bash", "more output"); hint != "" {
		t.Fatalf("expected degrade past budget, got %q", hint)
	}
}

func TestWriteSpillCapsSizeAndReusesContent(t *testing.T) {
	dir := t.TempDir()
	huge := strings.Repeat("y", spillMaxBytes+100)
	p1, err := writeSpill(dir, "bash", huge)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p1)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > spillMaxBytes+128 {
		t.Fatalf("spill size %d exceeds cap", info.Size())
	}
	p2, err := writeSpill(dir, "bash", huge)
	if err != nil || p2 != p1 {
		t.Fatalf("content-addressed reuse: p2=%q err=%v want %q", p2, err, p1)
	}
	// A planted symlink at a different content's leaf is refused.
	other := "other content"
	p3, err := writeSpill(dir, "grep", other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p3); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), p3); err != nil {
		t.Skip("symlinks unsupported")
	}
	if _, err := writeSpill(dir, "grep", other); err == nil {
		t.Fatal("expected refusal to write through planted symlink")
	}
}

func TestSpillPathParsing(t *testing.T) {
	temp := "/tmp/strike/sess"
	good := filepath.Join(temp, spillSubdir, "bash-0011.txt")
	cases := []struct {
		name, out, want string
		under           bool
	}{
		{"none", "plain output", "", false},
		{"hint", "x\n" + SpillHintPrefix + good + spillHintSuffix, good, true},
		{"last wins", SpillHintPrefix + "/etc/passwd" + spillHintSuffix + "\n" + SpillHintPrefix + good + spillHintSuffix, good, true},
		{"forged outside", SpillHintPrefix + "/etc/passwd" + spillHintSuffix, "/etc/passwd", false},
		{"unterminated", SpillHintPrefix + good, "", false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := SpillPathFromOutput(tt.out)
			if got != tt.want {
				t.Fatalf("path = %q, want %q", got, tt.want)
			}
			if IsSpillPath(temp, got) != tt.under {
				t.Fatalf("IsSpillPath(%q) = %v", got, !tt.under)
			}
		})
	}
	if IsSpillPath("", good) {
		t.Fatal("empty temp dir must not match")
	}
}

func TestGrepSpillsOverflowMatches(t *testing.T) {
	tc := spillCtx(t)
	var b strings.Builder
	for i := 1; i <= grepMaxMatches+50; i++ {
		fmt.Fprintf(&b, "needle %d\n", i)
	}
	fmt.Fprintf(&b, "needle %s\n", fakeSpillSecret)
	if err := os.WriteFile(filepath.Join(tc.WorkDir, "hay.txt"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := NewGrep().Execute(context.Background(), mustJSON(t, map[string]any{"pattern": "needle"}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Output, fmt.Sprintf("%d match(es)", grepMaxMatches)) {
		t.Fatalf("preview count changed: %q", res.Output[:40])
	}
	if !strings.Contains(res.Output, "51 more match(es)") {
		t.Fatalf("missing overflow count: %q", tail(res.Output, 300))
	}
	body := readSpill(t, tc, res.Output)
	if !strings.Contains(body, "hay.txt:1: needle 1\n") || !strings.Contains(body, fmt.Sprintf("needle %d\n", grepMaxMatches+50)) {
		t.Fatalf("spill missing matches: %q", tail(body, 200))
	}
	if strings.Contains(body, fakeSpillSecret) {
		t.Fatal("grep spill not redacted")
	}

	// Without scratch: today's truncate-and-discard.
	tc.SessionTempDir = ""
	res, err = NewGrep().Execute(context.Background(), mustJSON(t, map[string]any{"pattern": "needle"}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, SpillHintPrefix) || !strings.Contains(res.Output, "results truncated") {
		t.Fatalf("fallback output = %q", tail(res.Output, 300))
	}
}

func TestWebFetchSpillsOverflow(t *testing.T) {
	page := strings.Repeat("w", webfetchMaxOutputRunes) + "\nTAIL-MARKER " + fakeSpillSecret + "\n"
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(page))
	})
	tc := spillCtx(t)
	res, err := NewWebFetch().Execute(context.Background(), mustJSON(t, map[string]any{"url": srv.URL}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "TAIL-MARKER") {
		t.Fatal("tail leaked into capped preview")
	}
	body := readSpill(t, tc, res.Output)
	if !strings.Contains(body, "TAIL-MARKER") {
		t.Fatal("spill missing tail")
	}
	if strings.Contains(body, fakeSpillSecret) {
		t.Fatal("webfetch spill not redacted")
	}
}

func TestRunProcessSpillMaxRetainsFull(t *testing.T) {
	needBash(t)
	for _, combine := range []bool{true, false} {
		t.Run(fmt.Sprintf("combine=%v", combine), func(t *testing.T) {
			res, err := RunProcess(context.Background(), ProcessSpec{
				Argv:      []string{"bash", "-c", "head -c 5000 /dev/zero | tr '\\0' 'z'"},
				MaxOutput: 100,
				SpillMax:  20_000,
				Combine:   combine,
			}, ProcessObserver{})
			if err != nil {
				t.Fatal(err)
			}
			if !res.Truncated || len(res.Full) != 5000 {
				t.Fatalf("truncated=%v full=%d, want 5000", res.Truncated, len(res.Full))
			}
		})
	}
	res, err := RunProcess(context.Background(), ProcessSpec{
		Argv:      []string{"bash", "-c", "head -c 5000 /dev/zero | tr '\\0' 'z'"},
		MaxOutput: 100,
		Combine:   true,
	}, ProcessObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Full != "" {
		t.Fatalf("Full retained without SpillMax: %d bytes", len(res.Full))
	}
}

// The engine scrubs Output again in settleToolFeedback; the hint for a
// realistic session scratch path must survive that pass intact.
func TestSpillHintScrub(t *testing.T) {
	temp := filepath.Join(t.TempDir(), "strike", "20261113T120000.123456789Z-ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err := os.MkdirAll(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(t.TempDir())
	tc.SessionTempDir = temp
	hint := spillOverflow(tc, "bash", strings.Repeat("q", 100))
	if hint == "" {
		t.Fatal("no hint")
	}
	want := SpillPathFromOutput(hint)
	if got := SpillPathFromOutput(redact.ScrubToolOutput("preview\n" + hint)); got != want {
		t.Fatalf("scrubbed path = %q, want %q", got, want)
	}
}
