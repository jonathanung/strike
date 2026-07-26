package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/tool"
)

// fakeHTTPServer implements a minimal streamable-HTTP MCP endpoint.
func fakeHTTPServer(t *testing.T, mode string) *httptest.Server {
	t.Helper()
	var session atomic.Value
	session.Store("")
	var reqN atomic.Int64

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never echo Authorization into body/logs via response.
		if mode == "require-auth" {
			if r.Header.Get("Authorization") != "Bearer test-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
			return
		case http.MethodPost:
			// ok
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		var env struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *int64          `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			http.Error(w, "json", http.StatusBadRequest)
			return
		}
		if env.ID == nil {
			// notification
			w.WriteHeader(http.StatusAccepted)
			return
		}

		sid := session.Load().(string)
		if sid != "" && r.Header.Get(headerSessionID) != sid && env.Method != "initialize" {
			http.Error(w, "bad session", http.StatusNotFound)
			return
		}

		writeJSON := func(result any, rpcErr *rpcError) {
			n := reqN.Add(1)
			msg := map[string]any{"jsonrpc": "2.0", "id": *env.ID}
			if rpcErr != nil {
				msg["error"] = rpcErr
			} else {
				msg["result"] = result
			}
			data, _ := json.Marshal(msg)
			if env.Method == "initialize" {
				sid = "sess-" + fmt.Sprint(*env.ID)
				session.Store(sid)
			}
			if sid != "" {
				w.Header().Set(headerSessionID, sid)
			}
			if mode == "sse" || (mode == "sse-once" && n == 1) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(data)
		}

		switch env.Method {
		case "initialize":
			writeJSON(map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "fake-http", "version": "0.0.1"},
			}, nil)
		case "tools/list":
			writeJSON(map[string]any{
				"tools": []map[string]any{
					{
						"name":        "echo",
						"description": "echoes",
						"inputSchema": map[string]any{
							"type":       "object",
							"properties": map[string]any{"message": map[string]any{"type": "string"}},
						},
					},
				},
			}, nil)
		case "tools/call":
			var p callToolParams
			_ = json.Unmarshal(env.Params, &p)
			var args struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(p.Arguments, &args)
			writeJSON(map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "http-echo:" + args.Message},
				},
			}, nil)
		default:
			writeJSON(nil, &rpcError{Code: -32601, Message: "method not found"})
		}
	}))
}

func TestHTTPStartListCallJSON(t *testing.T) {
	srv := fakeHTTPServer(t, "")
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Start(ctx, ServerConfig{
		Name:      "remote",
		Transport: TransportHTTP,
		URL:       srv.URL,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer client.Close()

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}
	res, err := client.CallTool(ctx, "echo", json.RawMessage(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := formatContent(res.Content); got != "http-echo:hi" {
		t.Fatalf("content = %q", got)
	}
}

func TestHTTPStartListCallSSE(t *testing.T) {
	srv := fakeHTTPServer(t, "sse")
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := Start(ctx, ServerConfig{
		Name:      "sse",
		Transport: TransportHTTP,
		URL:       srv.URL,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer client.Close()

	res, err := client.CallTool(ctx, "echo", json.RawMessage(`{"message":"sse"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if got := formatContent(res.Content); got != "http-echo:sse" {
		t.Fatalf("content = %q", got)
	}
}

func TestHTTPAuthHeader(t *testing.T) {
	srv := fakeHTTPServer(t, "require-auth")
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := Start(ctx, ServerConfig{
		Name:      "authfail",
		Transport: TransportHTTP,
		URL:       srv.URL,
	})
	if err == nil {
		t.Fatal("expected auth failure")
	}
	if strings.Contains(err.Error(), "test-token") || strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("secret leaked in error: %v", err)
	}

	client, err := Start(ctx, ServerConfig{
		Name:      "authed",
		Transport: TransportHTTP,
		URL:       srv.URL,
		Headers:   map[string]string{"Authorization": "Bearer test-token"},
	})
	if err != nil {
		t.Fatalf("Start with auth: %v", err)
	}
	defer client.Close()
}

func TestManagerHTTPRegisters(t *testing.T) {
	srv := fakeHTTPServer(t, "")
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	reg := tool.NewRegistry()
	m := NewManager()
	defer m.Close()

	m.StartAll(ctx, []ServerConfig{{
		Name:      "remote",
		Transport: TransportHTTP,
		URL:       srv.URL,
	}}, reg)

	st := m.Statuses()
	if len(st) != 1 || st[0].State != "up" || st[0].Transport != TransportHTTP {
		t.Fatalf("status = %+v", st)
	}
	if st[0].Command != srv.URL {
		t.Fatalf("endpoint = %q", st[0].Command)
	}
	if _, ok := reg.Get("mcp_remote_echo"); !ok {
		t.Fatal("tool not registered")
	}
	// Headers must never appear in status text.
	summary := FormatStatuses(st)
	if strings.Contains(strings.ToLower(summary), "authorization") || strings.Contains(summary, "Bearer") {
		t.Fatalf("secret in summary: %q", summary)
	}
}

func TestReadSSEJSONRPC(t *testing.T) {
	body := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"ok\":true}}\n\n"
	resp, err := readSSEJSONRPC(strings.NewReader(body), 3)
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Result) != `{"ok":true}` {
		t.Fatalf("result = %s", resp.Result)
	}
}
