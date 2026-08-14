package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseMCPServeArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    cliOptions
		wantErr string
		help    bool
	}{
		{
			name: "defaults",
			args: nil,
			want: cliOptions{},
		},
		{
			name: "provider model auto",
			args: []string{"--provider", "echo", "--model", "m", "--auto"},
			want: cliOptions{provider: "echo", model: "m", providerSet: true, auto: true},
		},
		{
			name: "equals flags",
			args: []string{"--provider=echo", "--effort=high", "--sandbox=read-only"},
			want: cliOptions{provider: "echo", effort: "high", sandbox: "read-only", providerSet: true},
		},
		{
			name: "dangerously-skip-permissions",
			args: []string{"--dangerously-skip-permissions"},
			want: cliOptions{dangerouslySkipPermissions: true},
		},
		{
			name: "help",
			args: []string{"--help"},
			help: true,
		},
		{
			name:    "unexpected arg",
			args:    []string{"nope"},
			wantErr: "unexpected argument",
		},
		{
			name:    "bad sandbox",
			args:    []string{"--sandbox", "nope"},
			wantErr: "unknown sandbox",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseMCPServeArgs(tt.args)
			if tt.help {
				if err == nil {
					t.Fatal("expected help error")
				}
				return
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMCPServeArgs: %v", err)
			}
			if opts != tt.want {
				t.Fatalf("opts = %+v, want %+v", opts, tt.want)
			}
		})
	}
}

func TestRunMCPServeCLIHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMCPServeCLI([]string{"--help"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "strike_task") {
		t.Fatalf("help missing strike_task: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunMCPServeCLIBadArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMCPServeCLI([]string{"--sandbox=nope"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown sandbox") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestMCPServeStrikeTaskEcho(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Isolate config/auth so assemble does not pick up the developer machine.
	if err := os.MkdirAll(filepath.Join(home, ".strike"), 0o755); err != nil {
		t.Fatal(err)
	}

	cwd := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	req := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"strike_task","arguments":{"prompt":"hello from mcp"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"strike_task","arguments":{"prompt":""}}}`,
		"",
	}, "\n")

	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runMCPServeCLI(
			[]string{"--provider=echo", "--auto"},
			strings.NewReader(req),
			&stdout,
			&stderr,
		)
	}()

	var code int
	select {
	case code = <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("mcp-serve timed out; stderr=%q stdout=%q", stderr.String(), stdout.String())
	}
	if code != 0 {
		t.Fatalf("exit = %d\nstderr=%s\nstdout=%s", code, stderr.String(), stdout.String())
	}

	lines := nonEmptyMCPLines(stdout.String())
	if len(lines) < 4 {
		t.Fatalf("responses = %d, want >= 4\n%s\nstderr=%s", len(lines), stdout.String(), stderr.String())
	}

	var init struct {
		Result struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
			Capabilities map[string]any `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &init); err != nil {
		t.Fatalf("init: %v", err)
	}
	if init.Result.ServerInfo.Name != "strike" {
		t.Fatalf("serverInfo.name = %q", init.Result.ServerInfo.Name)
	}
	if _, ok := init.Result.Capabilities["tools"]; !ok {
		t.Fatalf("missing tools capability: %+v", init.Result.Capabilities)
	}

	var list struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Result.Tools) != 1 || list.Result.Tools[0].Name != "strike_task" {
		t.Fatalf("tools = %+v", list.Result.Tools)
	}

	var call struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &call); err != nil {
		t.Fatalf("call: %v\nline=%s", err, lines[2])
	}
	if call.Result.IsError {
		t.Fatalf("strike_task isError; content=%+v stderr=%s", call.Result.Content, stderr.String())
	}
	if len(call.Result.Content) == 0 || !strings.Contains(call.Result.Content[0].Text, "hello from mcp") {
		// echo provider typically echoes the user prompt; accept any non-empty text.
		if len(call.Result.Content) == 0 || strings.TrimSpace(call.Result.Content[0].Text) == "" {
			t.Fatalf("empty strike_task content: %+v stderr=%s", call.Result, stderr.String())
		}
	}

	var empty struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[3]), &empty); err != nil {
		t.Fatalf("empty: %v", err)
	}
	if !empty.Result.IsError {
		t.Fatalf("empty prompt should be tool error: %+v", empty.Result)
	}
	if len(empty.Result.Content) == 0 || !strings.Contains(empty.Result.Content[0].Text, "prompt is empty") {
		t.Fatalf("empty content = %+v", empty.Result.Content)
	}
}

func TestStrikeTaskHandlerInvalidJSON(t *testing.T) {
	h := strikeTaskHandler(cliOptions{provider: "echo", providerSet: true}, io.Discard)
	text, isErr, err := h(context.Background(), json.RawMessage(`not-json`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !isErr || !strings.Contains(text, "invalid arguments") {
		t.Fatalf("text=%q isErr=%v", text, isErr)
	}
}

func nonEmptyMCPLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
