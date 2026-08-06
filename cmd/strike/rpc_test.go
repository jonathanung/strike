package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseRPCArgsHelp(t *testing.T) {
	_, err := parseRPCArgs([]string{"--help"})
	if err != errRPCHelp {
		t.Fatalf("err = %v, want errRPCHelp", err)
	}
}

func TestParseRPCArgsProvider(t *testing.T) {
	opts, err := parseRPCArgs([]string{"--provider", "echo", "--auto"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.provider != "echo" || !opts.providerSet || !opts.dangerouslySkipPermissions {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestParseRPCArgsRejectsUpgrade(t *testing.T) {
	_, err := parseRPCArgs([]string{"--upgrade"})
	if err == nil || !strings.Contains(err.Error(), "upgrade") {
		t.Fatalf("err = %v, want upgrade rejection", err)
	}
}

func TestRunRPCCLIHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runRPCCLI([]string{"--help"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "JSON-RPC") {
		t.Fatalf("stdout missing usage: %q", stdout.String())
	}
}

func TestRunCLIRPCHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCLI([]string{"rpc", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout.String(), rpcUsage) {
		t.Fatalf("stdout missing rpc usage: %q", stdout.String())
	}
}

// rpcOut is a concurrency-safe stdout capture for runRPC tests.
type rpcOut struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *rpcOut) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}

func (o *rpcOut) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

func TestRunRPCEchoTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	pr, pw := io.Pipe()
	var stdout rpcOut
	var stderr bytes.Buffer

	// Single writer: initialize → user.input → wait for text → shutdown.
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		defer pw.Close()
		if _, err := io.WriteString(pw, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n"); err != nil {
			return
		}
		if _, err := io.WriteString(pw, `{"jsonrpc":"2.0","id":2,"method":"user.input","params":{"text":"hello rpc"}}`+"\n"); err != nil {
			return
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if rpcStdoutHasTextDelta(stdout.String(), "hello rpc") {
				_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":99,"method":"shutdown"}`+"\n")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		// Best-effort shutdown so runRPC does not hang the test forever.
		_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":99,"method":"shutdown"}`+"\n")
	}()

	done := make(chan error, 1)
	go func() {
		done <- runRPC(cliOptions{provider: "echo", providerSet: true}, pr, &stdout, &stderr)
	}()

	select {
	case err := <-done:
		<-writeDone
		if err != nil {
			t.Fatalf("runRPC: %v\nstderr=%s\nstdout=%s", err, stderr.String(), stdout.String())
		}
	case <-time.After(15 * time.Second):
		_ = pr.Close()
		t.Fatalf("runRPC timed out\nstderr=%s\nstdout=%s", stderr.String(), stdout.String())
	}

	out := stdout.String()
	if !rpcStdoutHasTextDelta(out, "hello rpc") {
		t.Fatalf("missing text.delta echo; stdout=%q stderr=%q", out, stderr.String())
	}
	if !strings.Contains(out, "rpc.ready") {
		t.Fatalf("missing rpc.ready: %s", out)
	}

	// stdout must be pure JSON-RPC lines
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		if !json.Valid([]byte(line)) {
			t.Fatalf("non-json stdout line: %q", line)
		}
	}

	// Session log should exist
	sessionsDir := filepath.Join(home, ".strike", "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		t.Fatalf("sessions dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected a session log file")
	}
}

func rpcStdoutHasTextDelta(out, want string) bool {
	// Echo streams word-by-word; accumulate text.delta payloads.
	var acc strings.Builder
	var sawTurnDone bool
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if m["method"] != "event" {
			continue
		}
		p, _ := m["params"].(map[string]any)
		switch p["type"] {
		case "text.delta":
			data, _ := p["data"].(map[string]any)
			text, _ := data["text"].(string)
			acc.WriteString(text)
		case "turn.completed":
			sawTurnDone = true
		}
	}
	if !strings.Contains(acc.String(), want) {
		return false
	}
	// Prefer waiting until the turn finishes so shutdown is not racy.
	return sawTurnDone
}

func TestRunRPCRequiresProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	err = runRPC(cliOptions{}, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"shutdown"}`+"\n"), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("err = %v, want credentials failure", err)
	}
}
