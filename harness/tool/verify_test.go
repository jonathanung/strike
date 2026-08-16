package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const strikeLikeAgents = `# strike-cli

## Verification (required before claiming done)

| Tier | When | Local gate |
|---|---|---|
| **A** | Docs, skills, comments, markdown-only | ` + "`gofmt`" + ` if any ` + "`.go`" + ` touched; full suite not required |
| **B** | Normal Go / web / TUI (default) | ` + "`gofmt`" + ` → ` + "`go generate ./internal/frontend/tui/app`" + ` if ` + "`internal/frontend/tui/app/_src`" + ` changed → ` + "`make web-check`" + ` if ` + "`web/`" + ` changed → ` + "`make test && make vet && make build`" + ` |
| **C** | Trust boundary | Tier B + ` + "`go test -race ./... -count=1`" + ` + focused package tests first |

` + "```sh" + `
make test
make vet
make build
` + "```" + `
`

const strikeLikeCI = `name: CI
jobs:
  test:
    steps:
      - name: Format
        run: |
          unformatted=$(gofmt -l .)
          if [ -n "$unformatted" ]; then
            echo "Not gofmt-formatted; run 'gofmt -w .':"
            exit 1
          fi
      - run: make web-check
      - run: go test -race ./...
`

func writeVerifyFixture(t *testing.T, agents, makefile, ci string) string {
	t.Helper()
	dir := t.TempDir()
	if agents != "" {
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(agents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if makefile != "" {
		if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(makefile), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if ci != "" {
		wf := filepath.Join(dir, ".github", "workflows")
		if err := os.MkdirAll(wf, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wf, "ci.yml"), []byte(ci), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func parseVerifyPayload(t *testing.T, res Result) verifyPayload {
	t.Helper()
	var p verifyPayload
	if err := json.Unmarshal([]byte(res.Output), &p); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, res.Output)
	}
	var meta verifyPayload
	if err := json.Unmarshal(res.Metadata, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if p.OK != meta.OK || p.Tier != meta.Tier || len(p.Failures) != len(meta.Failures) {
		t.Fatalf("metadata mismatch output=%+v meta=%+v", p, meta)
	}
	return p
}

func execVerify(t *testing.T, dir string, args map[string]any, run verifyCommandRunner) (Result, error) {
	t.Helper()
	return verifyTool{run: run}.Execute(context.Background(), mustJSON(t, args), allowAll(dir))
}

func TestVerifyNameContractSchema(t *testing.T) {
	tl := NewVerify()
	if tl.Name() != "verify" {
		t.Fatalf("Name() = %q", tl.Name())
	}
	c := LookupContract(tl)
	if c.SideEffect != SideEffectProcess || c.Idempotency != IdempotencyUnsafe {
		t.Fatalf("contract = %+v", c)
	}
	if len(tl.Schema()) == 0 || tl.Description() == "" {
		t.Fatal("missing schema/description")
	}
	if !strings.Contains(tl.Description(), "failures only") {
		t.Fatalf("description should say failures only: %q", tl.Description())
	}
}

func TestParseSelectStrikeLikeTiers(t *testing.T) {
	dir := writeVerifyFixture(t, strikeLikeAgents, "test:\n\tgo test ./...\n", strikeLikeCI)
	docs := inspectVerifyDocs(dir)

	a, err := selectTierCommands(docs, "A")
	if err != nil {
		t.Fatalf("A: %v", err)
	}
	if len(a) != 1 || a[0] != "gofmt -l ." {
		t.Fatalf("A commands = %#v, want [gofmt -l .] (enriched from CI)", a)
	}

	b, err := selectTierCommands(docs, "b")
	if err != nil {
		t.Fatalf("B: %v", err)
	}
	wantB := []string{
		"gofmt -l .",
		"go generate ./internal/frontend/tui/app",
		"make web-check",
		"make test",
		"make vet",
		"make build",
	}
	if strings.Join(b, "|") != strings.Join(wantB, "|") {
		t.Fatalf("B commands = %#v\nwant %#v", b, wantB)
	}

	c, err := selectTierCommands(docs, "C")
	if err != nil {
		t.Fatalf("C: %v", err)
	}
	wantC := append(append([]string{}, wantB...), "go test -race ./... -count=1")
	if strings.Join(c, "|") != strings.Join(wantC, "|") {
		t.Fatalf("C commands = %#v\nwant %#v", c, wantC)
	}
}

func TestParseSelectDoesNotTreatPathsAsCommands(t *testing.T) {
	md := `| **A** | docs | ` + "`gofmt`" + ` if ` + "`.go`" + ` and ` + "`web/`" + ` and ` + "`internal/frontend/tui/_src`" + ` |`
	got := parseVerifyTiers(md)["A"]
	if len(got) != 1 || got[0] != "gofmt" {
		t.Fatalf("A = %#v, want [gofmt]", got)
	}
}

func TestSelectMissingDocs(t *testing.T) {
	dir := t.TempDir()
	docs := inspectVerifyDocs(dir)
	_, err := selectTierCommands(docs, "B")
	if err == nil {
		t.Fatal("expected missing-docs error")
	}
	var ce *CodedError
	if !errors.As(err, &ce) || ce.Code != CodePreconditionFailed {
		t.Fatalf("err = %v, want precondition_failed", err)
	}
	msg := err.Error()
	for _, want := range []string{"AGENTS.md", "Makefile", "ci.yml", "will not guess", "none present"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in %q", want, msg)
		}
	}
	if strings.Contains(msg, "go test ./...") && !strings.Contains(msg, "will not guess") {
		t.Fatalf("looks like a guessed command: %q", msg)
	}
}

func TestSelectAgentsWithoutTiersIsMissingDocs(t *testing.T) {
	dir := writeVerifyFixture(t, "# hello\n\nNo tiers here.\n", "test:\n\tgo test ./...\n", "")
	docs := inspectVerifyDocs(dir)
	_, err := selectTierCommands(docs, "B")
	if err == nil {
		t.Fatal("expected missing-docs error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "found AGENTS.md, Makefile") {
		t.Fatalf("should list found files: %q", msg)
	}
	if strings.Contains(strings.ToLower(msg), "guessed go test") {
		t.Fatalf("must not guess go test: %q", msg)
	}
}

func TestSelectInvalidTier(t *testing.T) {
	_, err := selectTierCommands(verifyDocs{Tiers: map[string][]string{"B": {"make test"}}}, "D")
	if err == nil || CodeOf(err) != string(CodeInvalidArgs) {
		t.Fatalf("err = %v, want invalid_args", err)
	}
}

func TestParseVerifyFailuresStructured(t *testing.T) {
	out := strings.Join([]string{
		"=== RUN   TestFoo",
		"--- FAIL: TestFoo (0.00s)",
		"    foo_test.go:12: boom",
		"    foo_test.go:13: more",
		"--- FAIL: TestBar/sub (0.00s)",
		"    bar_test.go:4: nope",
		"FAIL",
		"FAIL\tgithub.com/acme/mod/internal/tool\t0.123s",
		"ok  \tgithub.com/acme/mod/internal/ok\t0.001s",
	}, "\n")
	got := parseVerifyFailures("make test", out, 1)
	if len(got) != 2 {
		t.Fatalf("failures = %#v", got)
	}
	if got[0].Package != "github.com/acme/mod/internal/tool" || got[0].Test != "TestFoo" {
		t.Fatalf("first = %+v", got[0])
	}
	if !strings.Contains(got[0].Snippet, "foo_test.go:12: boom") {
		t.Fatalf("snippet = %q", got[0].Snippet)
	}
	if got[0].Command != "make test" {
		t.Fatalf("command = %q", got[0].Command)
	}
	if got[1].Test != "TestBar/sub" || got[1].Package != "github.com/acme/mod/internal/tool" {
		t.Fatalf("second = %+v", got[1])
	}
}

func TestParseVerifyFailuresPassIsEmpty(t *testing.T) {
	out := "ok  \tgithub.com/acme/mod/internal/tool\t0.01s\nPASS\n"
	if got := parseVerifyFailures("make test", out, 0); got != nil {
		t.Fatalf("pass should be empty, got %#v", got)
	}
}

func TestParseVerifyFailuresFallbackExcerpt(t *testing.T) {
	out := "gofmt: not a directory\nmake: *** [fmt] Error 2\n"
	got := parseVerifyFailures("gofmt -l .", out, 2)
	if len(got) != 1 {
		t.Fatalf("got %#v", got)
	}
	if got[0].Test != "" || got[0].Command != "gofmt -l ." {
		t.Fatalf("got %+v", got[0])
	}
	if !strings.Contains(got[0].Snippet, "not a directory") {
		t.Fatalf("snippet = %q", got[0].Snippet)
	}
}

func TestVerifyPassCompact(t *testing.T) {
	dir := writeVerifyFixture(t, strikeLikeAgents, "", strikeLikeCI)
	var ran []string
	res, err := execVerify(t, dir, map[string]any{"tier": "A"}, func(ctx context.Context, tc *Context, command string, timeout time.Duration) (ProcessResult, error) {
		ran = append(ran, command)
		return ProcessResult{
			Output:   strings.Repeat("ok package\n", 200),
			ExitCode: 0,
			Status:   ProcessStatusExited,
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != "gofmt -l ." {
		t.Fatalf("ran = %#v", ran)
	}
	p := parseVerifyPayload(t, res)
	if !p.OK || p.Tier != "A" || len(p.Failures) != 0 {
		t.Fatalf("payload = %+v", p)
	}
	if strings.Contains(res.Output, "ok package") {
		t.Fatalf("passing run dumped suite output:\n%s", res.Output)
	}
	if !strings.Contains(res.Title, "passed") {
		t.Fatalf("title = %q", res.Title)
	}
}

func TestVerifyFailStructured(t *testing.T) {
	dir := writeVerifyFixture(t, strikeLikeAgents, "", strikeLikeCI)
	failOut := "--- FAIL: TestParse (0.00s)\n    verify_test.go:9: bad\nFAIL\tgithub.com/acme/mod/internal/tool\t0.01s\n"
	res, err := execVerify(t, dir, map[string]any{"tier": "A"}, func(ctx context.Context, tc *Context, command string, timeout time.Duration) (ProcessResult, error) {
		return ProcessResult{Output: failOut, ExitCode: 1, Status: ProcessStatusExited}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p := parseVerifyPayload(t, res)
	if p.OK || len(p.Failures) != 1 {
		t.Fatalf("payload = %+v", p)
	}
	f := p.Failures[0]
	if f.Package != "github.com/acme/mod/internal/tool" || f.Test != "TestParse" {
		t.Fatalf("failure = %+v", f)
	}
	if f.Command != "gofmt -l ." {
		t.Fatalf("command = %q", f.Command)
	}
	if !strings.Contains(f.Snippet, "verify_test.go:9: bad") {
		t.Fatalf("snippet = %q", f.Snippet)
	}
	if strings.Count(res.Output, "--- FAIL") > 1 {
		t.Fatalf("should not dump raw suite twice:\n%s", res.Output)
	}
	if !strings.Contains(res.Title, "1 failure") {
		t.Fatalf("title = %q", res.Title)
	}
}

func TestVerifyMissingDocsExecute(t *testing.T) {
	_, err := execVerify(t, t.TempDir(), map[string]any{"tier": "B"}, func(context.Context, *Context, string, time.Duration) (ProcessResult, error) {
		t.Fatal("runner must not run when docs are missing")
		return ProcessResult{}, nil
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if CodeOf(err) != string(CodePreconditionFailed) {
		t.Fatalf("code = %q err=%v", CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "will not guess") {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyInvalidArgs(t *testing.T) {
	dir := writeVerifyFixture(t, strikeLikeAgents, "", "")
	_, err := NewVerify().Execute(context.Background(), json.RawMessage(`{`), allowAll(dir))
	if err == nil || CodeOf(err) != string(CodeInvalidArgs) {
		t.Fatalf("bad json: %v", err)
	}
	_, err = execVerify(t, dir, map[string]any{}, nil)
	if err == nil || CodeOf(err) != string(CodeInvalidArgs) {
		t.Fatalf("missing tier: %v", err)
	}
	_, err = execVerify(t, dir, map[string]any{"tier": "Z"}, nil)
	if err == nil || CodeOf(err) != string(CodeInvalidArgs) {
		t.Fatalf("bad tier: %v", err)
	}
}

func TestVerifyPermissionDenied(t *testing.T) {
	dir := writeVerifyFixture(t, strikeLikeAgents, "", "")
	tc := allowAll(dir)
	tc.Ask = func(context.Context, AskRequest) error {
		return ErrPermissionDenied("verify denied")
	}
	_, err := verifyTool{}.Execute(context.Background(), mustJSON(t, map[string]any{"tier": "A"}), tc)
	if err == nil || CodeOf(err) != string(CodePermissionDenied) {
		t.Fatalf("err = %v", err)
	}
}

func TestInspectRealAgentsMd(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err != nil {
		t.Skip("AGENTS.md not at module root")
	}
	docs := inspectVerifyDocs(root)
	b, err := selectTierCommands(docs, "B")
	if err != nil {
		t.Fatalf("real AGENTS.md B: %v", err)
	}
	joined := strings.Join(b, "\n")
	for _, want := range []string{"make test", "make vet", "make build"} {
		if !strings.Contains(joined, want) {
			t.Errorf("real B missing %q: %#v", want, b)
		}
	}
	if strings.Contains(joined, "go test ./...") && !containsCommand(b, "go test ./...") {
		// Makefile may mention go test ./...; B should use documented make targets.
	}
	c, err := selectTierCommands(docs, "C")
	if err != nil {
		t.Fatalf("real AGENTS.md C: %v", err)
	}
	if !containsCommand(c, "go test -race ./... -count=1") {
		t.Fatalf("real C missing race command: %#v", c)
	}
}

func containsCommand(cmds []string, want string) bool {
	for _, c := range cmds {
		if c == want {
			return true
		}
	}
	return false
}
