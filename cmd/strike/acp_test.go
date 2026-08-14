package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseACPArgsHelp(t *testing.T) {
	_, err := parseACPArgs([]string{"--help"})
	if err != errACPHelp {
		t.Fatalf("err = %v, want errACPHelp", err)
	}
}

func TestParseACPArgsProvider(t *testing.T) {
	opts, err := parseACPArgs([]string{"--provider", "echo", "--auto"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.provider != "echo" || !opts.providerSet || !opts.auto {
		t.Fatalf("opts = %+v", opts)
	}
}

func TestParseACPArgsRejectsUpgrade(t *testing.T) {
	_, err := parseACPArgs([]string{"--upgrade"})
	if err == nil || !strings.Contains(err.Error(), "upgrade") {
		t.Fatalf("err = %v, want upgrade rejection", err)
	}
}

func TestRunACPCLIHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runACPCLI([]string{"--help"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Agent Client Protocol") {
		t.Fatalf("stdout missing usage: %q", stdout.String())
	}
}

func TestRunCLIACPHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runCLI([]string{"acp", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout.String(), acpUsage) {
		t.Fatalf("stdout missing acp usage: %q", stdout.String())
	}
}

type acpOut struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *acpOut) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}

func (o *acpOut) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

func TestRunACPEchoTurn(t *testing.T) {
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
	var stdout acpOut
	var stderr bytes.Buffer

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		defer pw.Close()
		lines := []string{
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
			`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":` + jsonString(work) + `,"mcpServers":[]}}`,
			`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"PLACEHOLDER","prompt":[{"type":"text","text":"hello acp"}]}}`,
		}
		// initialize + session/new first
		if _, err := io.WriteString(pw, lines[0]+"\n"); err != nil {
			return
		}
		if _, err := io.WriteString(pw, lines[1]+"\n"); err != nil {
			return
		}
		// Wait for session/new result to learn sessionId
		var sessionID string
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) && sessionID == "" {
			for _, m := range acpDecode(stdout.String()) {
				if m["id"] == float64(2) {
					if res, ok := m["result"].(map[string]any); ok {
						if sid, ok := res["sessionId"].(string); ok {
							sessionID = sid
						}
					}
				}
			}
			if sessionID == "" {
				time.Sleep(10 * time.Millisecond)
			}
		}
		if sessionID == "" {
			_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":99,"method":"shutdown"}`+"\n")
			return
		}
		prompt := `{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":` + jsonString(sessionID) + `,"prompt":[{"type":"text","text":"hello acp"}]}}`
		if _, err := io.WriteString(pw, prompt+"\n"); err != nil {
			return
		}
		deadline = time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if acpStdoutHasChunk(stdout.String(), "hello acp") {
				_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":99,"method":"shutdown"}`+"\n")
				return
			}
			// Also accept prompt result
			for _, m := range acpDecode(stdout.String()) {
				if m["id"] == float64(3) {
					if res, ok := m["result"].(map[string]any); ok && res["stopReason"] != nil {
						_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":99,"method":"shutdown"}`+"\n")
						return
					}
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":99,"method":"shutdown"}`+"\n")
	}()

	done := make(chan error, 1)
	go func() {
		done <- runACP(cliOptions{provider: "echo", providerSet: true, dangerouslySkipPermissions: true}, pr, &stdout, &stderr)
	}()

	select {
	case err := <-done:
		<-writeDone
		if err != nil {
			t.Fatalf("runACP: %v\nstderr=%s\nstdout=%s", err, stderr.String(), stdout.String())
		}
	case <-time.After(30 * time.Second):
		_ = pw.Close()
		t.Fatalf("timeout\nstderr=%s\nstdout=%s", stderr.String(), stdout.String())
	}

	if !acpStdoutHasChunk(stdout.String(), "hello acp") {
		// echo provider echoes the prompt; require either chunk or stopReason end_turn
		var gotStop bool
		for _, m := range acpDecode(stdout.String()) {
			if m["id"] == float64(3) {
				if res, ok := m["result"].(map[string]any); ok && res["stopReason"] == "end_turn" {
					gotStop = true
				}
			}
		}
		if !gotStop {
			t.Fatalf("missing agent chunk and prompt result\nstdout=%s\nstderr=%s", stdout.String(), stderr.String())
		}
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func acpDecode(s string) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out
}

func acpStdoutHasChunk(s, text string) bool {
	for _, m := range acpDecode(s) {
		if m["method"] != "session/update" {
			continue
		}
		p, _ := m["params"].(map[string]any)
		u, _ := p["update"].(map[string]any)
		if u["sessionUpdate"] != "agent_message_chunk" {
			continue
		}
		c, _ := u["content"].(map[string]any)
		if t, _ := c["text"].(string); strings.Contains(t, text) {
			return true
		}
	}
	return false
}
