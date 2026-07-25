package tool

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractSessionPR(t *testing.T) {
	tests := []struct {
		name    string
		command string
		output  string
		wantURL string
		wantNum int
		wantOK  bool
	}{
		{
			name:    "gh pr create url",
			command: "gh pr create --title t --body b",
			output:  "https://github.com/acme/repo/pull/42\n",
			wantURL: "https://github.com/acme/repo/pull/42",
			wantNum: 42,
			wantOK:  true,
		},
		{
			name:    "gh pr view with noise",
			command: "gh pr view --json url -q .url",
			output:  "Opening...\nhttps://github.com/foo/bar/pull/7\n",
			wantURL: "https://github.com/foo/bar/pull/7",
			wantNum: 7,
			wantOK:  true,
		},
		{
			name:    "env prefix still matches",
			command: "GH_TOKEN=x gh pr create",
			output:  "https://github.com/a/b/pull/1",
			wantURL: "https://github.com/a/b/pull/1",
			wantNum: 1,
			wantOK:  true,
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
			if pr.URL != tt.wantURL || pr.Number != tt.wantNum {
				t.Fatalf("pr = %+v, want url=%q num=%d", pr, tt.wantURL, tt.wantNum)
			}
		})
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
	if got.URL != "https://github.com/acme/repo/pull/99" || got.Number != 99 {
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
