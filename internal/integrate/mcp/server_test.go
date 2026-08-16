package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestServerInitializeListCall(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"message":"hi"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"missing","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"ping"}`,
		"",
	}, "\n"))

	srv := &Server{
		Name:    "test-server",
		Version: "0.0.1",
		Tools: []ServerTool{{
			Name:        "echo",
			Description: "echoes",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`),
			Handle: func(_ context.Context, args json.RawMessage) (string, bool, error) {
				var a struct {
					Message string `json:"message"`
				}
				if err := json.Unmarshal(args, &a); err != nil {
					return "", false, err
				}
				return "echo:" + a.Message, false, nil
			},
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Serve(ctx, in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	lines := nonEmptyLines(out.String())
	if len(lines) != 5 {
		t.Fatalf("responses = %d, want 5\n%s", len(lines), out.String())
	}

	var init struct {
		ID     int64 `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
			Capabilities map[string]any `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &init); err != nil {
		t.Fatalf("init: %v", err)
	}
	if init.ID != 1 || init.Result.ProtocolVersion != ProtocolVersion {
		t.Fatalf("init = %+v", init)
	}
	if init.Result.ServerInfo.Name != "test-server" || init.Result.ServerInfo.Version != "0.0.1" {
		t.Fatalf("serverInfo = %+v", init.Result.ServerInfo)
	}
	if _, ok := init.Result.Capabilities["tools"]; !ok {
		t.Fatalf("capabilities missing tools: %+v", init.Result.Capabilities)
	}

	var list struct {
		ID     int64 `json:"id"`
		Result struct {
			Tools []toolInfo `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if list.ID != 2 || len(list.Result.Tools) != 1 || list.Result.Tools[0].Name != "echo" {
		t.Fatalf("list = %+v", list)
	}

	var call struct {
		ID     int64          `json:"id"`
		Result callToolResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &call); err != nil {
		t.Fatalf("call: %v", err)
	}
	if call.ID != 3 || call.Result.IsError || formatContent(call.Result.Content) != "echo:hi" {
		t.Fatalf("call = %+v", call)
	}

	var missing struct {
		ID    int64 `json:"id"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[3]), &missing); err != nil {
		t.Fatalf("missing: %v", err)
	}
	if missing.ID != 4 || missing.Error == nil || missing.Error.Code != -32601 {
		t.Fatalf("missing = %+v", missing)
	}

	var ping struct {
		ID     int64          `json:"id"`
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[4]), &ping); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if ping.ID != 5 || ping.Result == nil {
		t.Fatalf("ping = %+v", ping)
	}
}

func TestServerStringIDAndToolError(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":"abc","method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":"call-1","method":"tools/call","params":{"name":"boom","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":"call-2","method":"tools/call","params":{"name":"fail","arguments":{}}}`,
		"",
	}, "\n"))

	srv := &Server{
		Tools: []ServerTool{
			{
				Name: "boom",
				Handle: func(context.Context, json.RawMessage) (string, bool, error) {
					return "boom failed", true, nil
				},
			},
			{
				Name: "fail",
				Handle: func(context.Context, json.RawMessage) (string, bool, error) {
					return "", false, io.ErrUnexpectedEOF
				},
			},
		},
	}
	if err := srv.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	lines := nonEmptyLines(out.String())
	if len(lines) != 3 {
		t.Fatalf("responses = %d\n%s", len(lines), out.String())
	}

	var init struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &init); err != nil || init.ID != "abc" {
		t.Fatalf("init id: %v %q", err, init.ID)
	}

	var boom struct {
		ID     string         `json:"id"`
		Result callToolResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &boom); err != nil {
		t.Fatalf("boom: %v", err)
	}
	if boom.ID != "call-1" || !boom.Result.IsError || formatContent(boom.Result.Content) != "boom failed" {
		t.Fatalf("boom = %+v", boom)
	}

	var fail struct {
		ID    string `json:"id"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &fail); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if fail.ID != "call-2" || fail.Error == nil || fail.Error.Code != -32000 {
		t.Fatalf("fail = %+v", fail)
	}
	if !strings.Contains(fail.Error.Message, "unexpected EOF") {
		t.Fatalf("fail message = %q", fail.Error.Message)
	}
}

func TestServerUnknownMethod(t *testing.T) {
	var out bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"resources/list"}` + "\n")
	srv := &Server{}
	if err := srv.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var resp struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("resp = %s", out.String())
	}
}

func TestServerContextCancel(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- (&Server{}).Serve(ctx, r, io.Discard)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	_ = w.Close()
	select {
	case <-done:
		// canceled, EOF, or nil after close — any return is success
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
