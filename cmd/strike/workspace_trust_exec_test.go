package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/trust/workspacetrust"
)

// readSessionLogs concatenates every session JSONL under home.
func readSessionLogs(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, ".strike", "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("sessions dir: %v", err)
	}
	var sb strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(data)
	}
	return sb.String()
}

// TestRunExecUntrustedWorkspaceKeepsRepoConfigInert is the headless case
// from #1352: a fresh repo shipping an MCP server and steering runs with that
// config inert, the skip is on the transcript, and the server process never
// starts (a marker file the server would create stays absent). After
// `strike trust` the same repo starts the server.
func TestRunExecUntrustedWorkspaceKeepsRepoConfigInert(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	config.ResetPluginCache()
	repo := t.TempDir()
	marker := filepath.Join(t.TempDir(), "mcp-started")
	// The "server" proves it ran by touching marker, then exits (handshake
	// fails fast and is recorded as a per-server failure, not an abort).
	mcp := `{"probe": {"command": "/bin/sh", "args": ["-c", "touch '` + marker + `'"]}}`
	for rel, body := range map[string]string{
		".strike/mcp.jsonc": mcp,
		"AGENTS.md":         "repo steering",
	} {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(repo)

	var stdout, stderr bytes.Buffer
	if err := runExec(cliOptions{provider: "echo", providerSet: true}, "hello", execFormatText, &stdout, &stderr); err != nil {
		t.Fatalf("runExec: %v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "You said: hello") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "untrusted workspace: repo config inert") {
		t.Errorf("stderr missing untrusted notice: %q", stderr.String())
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untrusted repo MCP server was spawned (marker stat err = %v)", err)
	}
	log := readSessionLogs(t, home)
	if !strings.Contains(log, `"surface":"workspace"`) || !strings.Contains(log, "repo config inert") {
		t.Errorf("transcript missing workspace skip record:\n%s", log)
	}
	if strings.Contains(log, "repo steering") {
		t.Error("untrusted steering reached the session")
	}

	// Trust the root the same way the CLI does, then rerun.
	store, err := workspacetrust.Open(workspacetrust.DefaultPath(filepath.Join(home, ".strike")))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Trust(repo); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if err := runExec(cliOptions{provider: "echo", providerSet: true}, "again", execFormatText, &stdout, &stderr); err != nil {
		t.Fatalf("runExec (trusted): %v\nstderr=%s", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "untrusted workspace") {
		t.Errorf("trusted run still printed the untrusted notice: %q", stderr.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("trusted repo MCP server did not start: %v\nstderr=%s", err, stderr.String())
	}
}
