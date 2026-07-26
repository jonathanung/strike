package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/tool"
)

// TestHelperProcess is the fake stdio MCP server used by tests.
// Invoked as: go test -c && ./mcp.test -test.run=TestHelperProcess --
// with GO_WANT_HELPER_PROCESS=1.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	mode := os.Getenv("MCP_FAKE_MODE")
	runFakeMCP(mode)
	os.Exit(0)
}

func runFakeMCP(mode string) {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *int64          `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		if req.ID == nil {
			// notification
			continue
		}
		switch req.Method {
		case "initialize":
			writeRPC(*req.ID, map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "fake", "version": "0.0.1"},
			}, nil)
		case "tools/list":
			writeRPC(*req.ID, map[string]any{
				"tools": []map[string]any{
					{
						"name":        "echo",
						"description": "echoes the message",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"message": map[string]any{"type": "string"},
							},
							"required": []string{"message"},
						},
					},
					{
						"name":        "boom",
						"description": "returns an error result",
						"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
					},
				},
			}, nil)
		case "tools/call":
			var p callToolParams
			_ = json.Unmarshal(req.Params, &p)
			switch p.Name {
			case "echo":
				var args struct {
					Message string `json:"message"`
				}
				_ = json.Unmarshal(p.Arguments, &args)
				if mode == "crash-on-call" {
					os.Exit(2)
				}
				writeRPC(*req.ID, map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": "echo:" + args.Message},
					},
				}, nil)
			case "boom":
				writeRPC(*req.ID, map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": "boom failed"},
					},
					"isError": true,
				}, nil)
			default:
				writeRPC(*req.ID, nil, &rpcError{Code: -32601, Message: "unknown tool"})
			}
		default:
			writeRPC(*req.ID, nil, &rpcError{Code: -32601, Message: "method not found"})
		}
		if mode == "exit-after-list" && req.Method == "tools/list" {
			os.Exit(0)
		}
	}
}

func writeRPC(id int64, result any, rpcErr *rpcError) {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
	}
	if rpcErr != nil {
		msg["error"] = rpcErr
	} else {
		msg["result"] = result
	}
	data, _ := json.Marshal(msg)
	fmt.Printf("%s\n", data)
}

func helperCommand(t *testing.T, mode string) (command string, args []string, env map[string]string) {
	t.Helper()
	return os.Args[0], []string{"-test.run=TestHelperProcess", "--"}, map[string]string{
		"GO_WANT_HELPER_PROCESS": "1",
		"MCP_FAKE_MODE":          mode,
	}
}

func TestStartListCall(t *testing.T) {
	cmd, args, env := helperCommand(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Start(ctx, ServerConfig{
		Name:    "fake",
		Command: cmd,
		Args:    args,
		Env:     env,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer client.Close()

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(tools))
	}

	res, err := client.CallTool(ctx, "echo", json.RawMessage(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatal("unexpected isError")
	}
	if got := formatContent(res.Content); got != "echo:hi" {
		t.Fatalf("content = %q", got)
	}
}

func TestBridgeExecutePermissionAndCall(t *testing.T) {
	cmd, args, env := helperCommand(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Start(ctx, ServerConfig{Name: "demo", Command: cmd, Args: args, Env: env})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer client.Close()

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var echo toolInfo
	for _, ti := range tools {
		if ti.Name == "echo" {
			echo = ti
			break
		}
	}
	bridge := newBridge(client, echo)
	if bridge.Name() != "mcp_demo_echo" {
		t.Fatalf("name = %q", bridge.Name())
	}

	var asked []tool.AskRequest
	tc := &tool.Context{
		WorkDir: t.TempDir(),
		Ask: func(_ context.Context, req tool.AskRequest) error {
			asked = append(asked, req)
			return nil
		},
	}
	res, err := bridge.Execute(ctx, json.RawMessage(`{"message":"ok"}`), tc)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Output != "echo:ok" {
		t.Fatalf("output = %q", res.Output)
	}
	if len(asked) != 1 || asked[0].Permission != Permission || asked[0].Patterns[0] != "demo/echo" {
		t.Fatalf("ask = %+v", asked)
	}
}

func TestBridgePermissionDeny(t *testing.T) {
	cmd, args, env := helperCommand(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Start(ctx, ServerConfig{Name: "demo", Command: cmd, Args: args, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	tools, _ := client.ListTools(ctx)
	bridge := newBridge(client, tools[0])
	tc := &tool.Context{
		Ask: func(context.Context, tool.AskRequest) error {
			return fmt.Errorf("permission denied by user")
		},
	}
	_, err = bridge.Execute(ctx, json.RawMessage(`{}`), tc)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v", err)
	}
}

func TestManagerRegistersAndStatus(t *testing.T) {
	cmd, args, env := helperCommand(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	reg := tool.NewRegistry()
	m := NewManager()
	defer m.Close()

	m.StartAll(ctx, []ServerConfig{{
		Name:    "fake",
		Command: cmd,
		Args:    args,
		Env:     env,
	}}, reg)

	st := m.Statuses()
	if len(st) != 1 || st[0].State != "up" || st[0].ToolCount != 2 {
		t.Fatalf("status = %+v", st)
	}
	if _, ok := reg.Get("mcp_fake_echo"); !ok {
		t.Fatal("echo tool not registered")
	}

	summary := FormatStatuses(st)
	if !strings.Contains(summary, "fake") || !strings.Contains(summary, "up") {
		t.Fatalf("summary = %q", summary)
	}
}

func TestCallAfterCrashErrorsCleanly(t *testing.T) {
	cmd, args, env := helperCommand(t, "crash-on-call")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Start(ctx, ServerConfig{Name: "crashy", Command: cmd, Args: args, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	_, err = client.CallTool(ctx, "echo", json.RawMessage(`{"message":"x"}`))
	if err == nil {
		t.Fatal("expected error after crash")
	}

	// Subsequent calls fail cleanly without panicking.
	deadline := time.Now().Add(3 * time.Second)
	for !client.Closed() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	_, err = client.CallTool(ctx, "echo", json.RawMessage(`{"message":"y"}`))
	if err == nil {
		t.Fatal("expected unavailable error")
	}
	if !strings.Contains(err.Error(), "unavailable") && !strings.Contains(err.Error(), "crashy") {
		t.Fatalf("err = %v", err)
	}
}

func TestNamespaceTool(t *testing.T) {
	cases := []struct {
		server, tool, want string
	}{
		{"github", "create_issue", "mcp_github_create_issue"},
		{"My-Server", "Do Thing", "mcp_my_server_do_thing"},
		{"a", "", "mcp_a_unnamed"},
	}
	for _, tc := range cases {
		if got := NamespaceTool(tc.server, tc.tool); got != tc.want {
			t.Errorf("NamespaceTool(%q,%q)=%q want %q", tc.server, tc.tool, got, tc.want)
		}
	}
}

func TestConfigsFromMap(t *testing.T) {
	got := ConfigsFromMap(map[string]ServerConfigFields{
		"good":   {Command: "npx", Args: []string{"-y", "x"}, Env: map[string]string{"A": "1"}},
		"bad":    {Command: ""},
		"1no":    {Command: "echo"},
		"remote": {Type: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer secret"}},
		"infer":  {URL: "http://127.0.0.1:9/mcp"},
	}, "/tmp/work")
	if len(got) != 3 {
		t.Fatalf("got = %+v", got)
	}
	byName := map[string]ServerConfig{}
	for _, c := range got {
		byName[c.Name] = c
	}
	if byName["good"].WorkDir != "/tmp/work" || byName["good"].Transport != TransportStdio {
		t.Fatalf("good = %+v", byName["good"])
	}
	if byName["remote"].Transport != TransportHTTP || byName["remote"].URL == "" {
		t.Fatalf("remote = %+v", byName["remote"])
	}
	if byName["remote"].Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("headers not preserved")
	}
	if byName["infer"].Transport != TransportHTTP {
		t.Fatalf("infer = %+v", byName["infer"])
	}
}

func TestManagerDisableAndRetry(t *testing.T) {
	cmd, args, env := helperCommand(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	reg := tool.NewRegistry()
	m := NewManager()
	defer m.Close()

	cfg := ServerConfig{Name: "fake", Command: cmd, Args: args, Env: env}
	m.StartAll(ctx, []ServerConfig{cfg}, reg)
	if st := m.Statuses(); len(st) != 1 || st[0].State != "up" {
		t.Fatalf("status = %+v", m.Statuses())
	}
	if _, ok := reg.Get("mcp_fake_echo"); !ok {
		t.Fatal("echo tool missing")
	}

	if err := m.Disable("fake"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if _, ok := reg.Get("mcp_fake_echo"); ok {
		t.Fatal("tool should be unregistered")
	}
	st := m.Statuses()
	if len(st) != 1 || st[0].State != "disabled" {
		t.Fatalf("status after disable = %+v", st)
	}

	if err := m.Retry(ctx, "fake"); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	st = m.Statuses()
	if len(st) != 1 || st[0].State != "up" || st[0].ToolCount != 2 {
		t.Fatalf("status after retry = %+v", st)
	}
	if _, ok := reg.Get("mcp_fake_echo"); !ok {
		t.Fatal("echo tool not re-registered")
	}
}

func TestRedactErrSecrets(t *testing.T) {
	if got := redactErr(fmt.Errorf("Authorization: Bearer super-secret-token")); got != "start failed" {
		t.Fatalf("got %q", got)
	}
	if got := redactErr(fmt.Errorf("TOKEN=abc123")); got != "start failed" {
		t.Fatalf("got %q", got)
	}
	if got := redactErr(fmt.Errorf("connection refused")); got != "connection refused" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatStatusesHint(t *testing.T) {
	s := FormatStatuses([]Status{{Name: "a", State: "up", Transport: "stdio", ToolCount: 1}})
	if !strings.Contains(s, "/mcp retry") || !strings.Contains(s, "disable") {
		t.Fatalf("summary = %q", s)
	}
}

func TestStartMissingCommand(t *testing.T) {
	_, err := Start(context.Background(), ServerConfig{
		Name:    "x",
		Command: filepath.Join(t.TempDir(), "no-such-binary"),
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

// Ensure helper process binary path works under `go test` (same as os.Args[0]).
func TestHelperProcessBinaryExists(t *testing.T) {
	if _, err := exec.LookPath(os.Args[0]); err != nil && !fileExists(os.Args[0]) {
		t.Skip("test binary path not executable in this environment")
	}
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
