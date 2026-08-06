package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/sandbox"
)

func TestExtractSessionPR(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		output    string
		wantURL   string
		wantNum   int
		wantState string
		wantOK    bool
	}{
		{
			name:      "gh pr create url",
			command:   "gh pr create --title t --body b",
			output:    "https://github.com/acme/repo/pull/42\n",
			wantURL:   "https://github.com/acme/repo/pull/42",
			wantNum:   42,
			wantState: "open",
			wantOK:    true,
		},
		{
			name:      "gh pr view with noise",
			command:   "gh pr view --json url -q .url",
			output:    "Opening...\nhttps://github.com/foo/bar/pull/7\n",
			wantURL:   "https://github.com/foo/bar/pull/7",
			wantNum:   7,
			wantState: "open",
			wantOK:    true,
		},
		{
			name:      "json state merged",
			command:   "gh pr view --json url,number,state",
			output:    `{"url":"https://github.com/a/b/pull/3","number":3,"state":"MERGED"}` + "\nhttps://github.com/a/b/pull/3\n",
			wantURL:   "https://github.com/a/b/pull/3",
			wantNum:   3,
			wantState: "merged",
			wantOK:    true,
		},
		{
			name:      "env prefix still matches",
			command:   "GH_TOKEN=x gh pr create",
			output:    "https://github.com/a/b/pull/1",
			wantURL:   "https://github.com/a/b/pull/1",
			wantNum:   1,
			wantState: "open",
			wantOK:    true,
		},
		{
			name:    "non-gh command ignored",
			command: "echo https://github.com/a/b/pull/9",
			output:  "https://github.com/a/b/pull/9",
			wantOK:  false,
		},
		{
			name:    "gh without pr subcommand",
			command: "gh issue list",
			output:  "https://github.com/a/b/pull/9",
			wantOK:  false,
		},
		{
			name:    "no url in output",
			command: "gh pr create",
			output:  "error: not on a branch",
			wantOK:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pr, ok := extractSessionPR(tt.command, tt.output)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (pr=%+v)", ok, tt.wantOK, pr)
			}
			if !tt.wantOK {
				return
			}
			if pr.URL != tt.wantURL || pr.Number != tt.wantNum || pr.State != tt.wantState {
				t.Fatalf("pr = %+v, want url=%q num=%d state=%q", pr, tt.wantURL, tt.wantNum, tt.wantState)
			}
		})
	}
}

func TestBashMarksCheckpointUncovered(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	store := NewCheckpointStore()
	store.BeginTurn("t")
	tc := allowAll(dir)
	tc.CheckpointUncovered = store.MarkUncovered
	if _, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "true",
	}), tc); err != nil {
		t.Fatal(err)
	}
	store.CommitTurn()
	peek := store.Peek()
	if len(peek.Uncovered) != 1 || peek.Uncovered[0] != "bash" {
		t.Fatalf("after bash Peek.Uncovered = %#v", peek.Uncovered)
	}
}

func TestBashCWDResetsBetweenCalls(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	sub := filepath.Join(root, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Distinct absolute path outside root so a sticky cwd would be obvious.
	outside := t.TempDir()
	tc := allowAll(root)

	first, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "cd " + outside + " && pwd",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(first.Output); got != outside {
		t.Fatalf("first pwd = %q, want %q", got, outside)
	}

	second, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "pwd",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(second.Output); got != root {
		t.Fatalf("second pwd = %q, want session root %q (cd must not stick)", got, root)
	}

	// cd within one call is fine and still starts from root.
	third, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "cd nested && pwd",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(third.Output); got != sub {
		t.Fatalf("third pwd = %q, want %q", got, sub)
	}

	fourth, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "pwd",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(fourth.Output); got != root {
		t.Fatalf("fourth pwd = %q, want session root %q", got, root)
	}
}

func TestBashRecordsSessionPRFromGH(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	// Fake gh on PATH that prints a PR URL.
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho 'https://github.com/acme/repo/pull/99'\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var got SessionPR
	tc := allowAll(dir)
	tc.RecordSessionPR = func(pr SessionPR) error {
		got = pr
		return nil
	}
	res, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "gh pr create --title x --body y",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://github.com/acme/repo/pull/99" || got.Number != 99 || got.State != "open" {
		t.Fatalf("RecordSessionPR got %+v", got)
	}
	var meta map[string]any
	if err := json.Unmarshal(res.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["prUrl"] != "https://github.com/acme/repo/pull/99" {
		t.Fatalf("metadata = %s", res.Metadata)
	}
	if !strings.Contains(res.Output, "pull/99") {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestBashWorkspaceWriteInsideSandbox(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	tc := allowAll(root)
	marker := filepath.Join(root, "sandboxed.txt")
	res, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "printf x > sandboxed.txt && cat sandboxed.txt",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "x") {
		t.Fatalf("output = %q", res.Output)
	}
	body, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "x" {
		t.Fatalf("file = %q", body)
	}
}

func TestBashSandboxModeOff(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	tc := allowAll(root)
	tc.SandboxMode = "off"
	res, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "echo off-mode-ok",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "off-mode-ok") {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestBashSandboxMode(t *testing.T) {
	if got := bashSandboxMode(nil); got != sandbox.DefaultMode {
		t.Fatalf("nil tc = %v", got)
	}
	if got := bashSandboxMode(&Context{}); got != sandbox.ModeWorkspaceWrite {
		t.Fatalf("empty = %v", got)
	}
	if got := bashSandboxMode(&Context{SandboxMode: "off"}); got != sandbox.ModeOff {
		t.Fatalf("off = %v", got)
	}
	if got := bashSandboxMode(&Context{SandboxMode: "read-only"}); got != sandbox.ModeReadOnly {
		t.Fatalf("read-only = %v", got)
	}
}

func TestBashSandboxPolicyCompiled(t *testing.T) {
	wd := t.TempDir()
	p := bashSandboxPolicy(&Context{
		WorkDir:     wd,
		SandboxMode: "workspace-write",
		Sandbox: sandbox.Policy{
			Mode:             sandbox.ModeWorkspaceWrite,
			WorkDir:          wd,
			NoWorkspaceWrite: true,
			DenyWriteGlobs:   []string{"**/*.env"},
		},
	})
	if !p.NoWorkspaceWrite || !p.NetworkEnabled() || len(p.DenyWriteGlobs) != 1 {
		t.Fatalf("compiled policy = %+v", p)
	}
	// Fallback when only SandboxMode is set — network on (product default).
	p2 := bashSandboxPolicy(&Context{WorkDir: wd, SandboxMode: "read-only"})
	if p2.Mode != sandbox.ModeReadOnly || p2.WorkDir != wd || !p2.NetworkEnabled() {
		t.Fatalf("fallback = %+v", p2)
	}
	if pNil := bashSandboxPolicy(nil); !pNil.NetworkEnabled() || pNil.Mode != sandbox.DefaultMode {
		t.Fatalf("nil context = %+v", pNil)
	}
	// Explicit air-gap is preserved when compiled.
	pOff := bashSandboxPolicy(&Context{
		WorkDir: wd,
		Sandbox: sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite, WorkDir: wd, NoNetwork: true},
	})
	if pOff.NetworkEnabled() || !pOff.NoNetwork {
		t.Fatalf("NoNetwork compiled = %+v", pOff)
	}
}

func TestBashDoesNotRecordPROnFailure(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho 'https://github.com/acme/repo/pull/1'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	called := false
	tc := allowAll(dir)
	tc.RecordSessionPR = func(SessionPR) error {
		called = true
		return nil
	}
	_, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "gh pr create",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("RecordSessionPR must not run on non-zero exit")
	}
}

func TestBashSandboxDenialSetsErrorCode(t *testing.T) {
	// Integration: OS sandbox applied + deny-write path inside the workspace
	// (static bash guard allows in-workspace redirects; bwrap remounts RO).
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if !sandbox.Available() {
		t.Skip("OS sandbox backend unavailable")
	}
	root := t.TempDir()
	denyPath := filepath.Join(root, "secret.env")
	if err := os.WriteFile(denyPath, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tc := allowAll(root)
	tc.SandboxMode = "workspace-write"
	tc.Sandbox = sandbox.Policy{
		Mode:           sandbox.ModeWorkspaceWrite,
		WorkDir:        root,
		DenyWritePaths: []string{denyPath},
	}
	res, err := NewBash().Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "echo overwritten > secret.env",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := os.ReadFile(denyPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(body) != "keep\n" {
		t.Fatalf("deny-write path was mutated: %q", body)
	}
	if res.ErrorCode != ErrorCodeSandboxDenied {
		low := strings.ToLower(res.Output)
		if res.ErrorCode == "" && (strings.Contains(low, "permission denied") ||
			strings.Contains(low, "read-only")) {
			t.Fatalf("denial text present but ErrorCode=%q out=%q", res.ErrorCode, res.Output)
		}
		// Backend applied but message shape unexpected — still require block.
		t.Logf("ErrorCode=%q out=%q (file intact)", res.ErrorCode, res.Output)
		return
	}
	if !strings.Contains(res.Output, string(CodeSandboxDenied)) {
		t.Fatalf("output missing code prefix: %q", res.Output)
	}
	if !strings.Contains(string(res.Metadata), `"errorCode":"sandbox_denied"`) {
		t.Fatalf("metadata = %s", res.Metadata)
	}
	if !strings.Contains(string(res.Metadata), `"sandboxApplied":true`) {
		t.Fatalf("metadata missing sandboxApplied: %s", res.Metadata)
	}
}

func TestBashSandboxDenialClassificationUnit(t *testing.T) {
	// Pure classification path without requiring bwrap: feed a synthetic
	// ProcessResult through the same helpers bash uses.
	proc := ProcessResult{
		SandboxApplied: true,
		ExitCode:       1,
		Output:         "cannot create /etc/x: Read-only file system",
		Status:         ProcessStatusExited,
	}
	reason, ok := detectSandboxDenial(proc)
	if !ok {
		t.Fatal("expected denial")
	}
	out := formatSandboxDenial(reason, proc.Output+"\n(exit code 1)")
	if !strings.HasPrefix(out, string(CodeSandboxDenied)+":") {
		t.Fatalf("out = %q", out)
	}
}

func TestBashTimeoutSetsErrorCode(t *testing.T) {
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, AskRequest) error { return nil },
	}
	args, _ := json.Marshal(map[string]any{
		"command":   "sleep 5",
		"timeoutMs": 50,
	})
	res, err := NewBash().Execute(context.Background(), args, tc)
	if err != nil {
		t.Fatal(err)
	}
	if res.ErrorCode != ErrorCodeTimeout {
		t.Fatalf("ErrorCode = %q, want %q", res.ErrorCode, ErrorCodeTimeout)
	}
	if !strings.Contains(res.Output, "timed out") {
		t.Fatalf("output = %q", res.Output)
	}
	if !strings.Contains(string(res.Metadata), `"incomplete":true`) {
		t.Fatalf("metadata = %s", res.Metadata)
	}
}

func TestBashCancelPreservesPartialOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, AskRequest) error { return nil },
	}
	// Print then sleep so cancel captures stdout.
	args, _ := json.Marshal(map[string]any{
		"command": "printf 'hello-partial\\n'; sleep 30",
	})
	done := make(chan Result, 1)
	errc := make(chan error, 1)
	go func() {
		res, err := NewBash().Execute(ctx, args, tc)
		done <- res
		errc <- err
	}()
	// Wait until some output is likely, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		if err := <-errc; err != nil {
			t.Fatalf("err = %v", err)
		}
		if res.ErrorCode != ErrorCodeCanceled {
			t.Fatalf("ErrorCode = %q, want %q", res.ErrorCode, ErrorCodeCanceled)
		}
		if !strings.Contains(res.Output, "hello-partial") {
			t.Fatalf("output missing partial: %q", res.Output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bash did not return after cancel")
	}
}
