package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestServerInitializeAndShutdown(t *testing.T) {
	var ops []protocol.Op
	var mu sync.Mutex
	submit := func(ctx context.Context, op protocol.Op) error {
		mu.Lock()
		ops = append(ops, op)
		mu.Unlock()
		return nil
	}

	in := strings.NewReader("" +
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"shutdown"}` + "\n")
	var out bytes.Buffer
	srv := New(in, &out, submit, Options{SessionID: "sess-1"})
	err := srv.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	msgs := decodeLines(t, out.Bytes())
	if len(msgs) < 3 {
		t.Fatalf("got %d messages, want >= 3: %s", len(msgs), out.String())
	}
	// First: rpc.ready notification
	if got := msgs[0]["method"]; got != "rpc.ready" {
		t.Fatalf("first method = %v, want rpc.ready", got)
	}
	params, _ := msgs[0]["params"].(map[string]any)
	if params["sessionId"] != "sess-1" {
		t.Fatalf("sessionId = %v", params["sessionId"])
	}
	if params["protocolVersion"] != protocol.Version {
		t.Fatalf("protocolVersion = %v, want %s", params["protocolVersion"], protocol.Version)
	}
	// initialize result
	if msgs[1]["id"] != float64(1) {
		t.Fatalf("initialize id = %v", msgs[1]["id"])
	}
	res, _ := msgs[1]["result"].(map[string]any)
	if res["sessionId"] != "sess-1" {
		t.Fatalf("initialize result = %v", res)
	}
	// shutdown result
	if msgs[2]["id"] != float64(2) {
		t.Fatalf("shutdown id = %v", msgs[2]["id"])
	}
	if len(ops) != 0 {
		t.Fatalf("unexpected ops: %#v", ops)
	}
}

func TestServerOpMethodAndEventNotification(t *testing.T) {
	opsCh := make(chan protocol.Op, 4)
	submit := func(ctx context.Context, op protocol.Op) error {
		select {
		case opsCh <- op:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	events := make(chan protocol.Event, 4)

	pr, pw := io.Pipe()
	out := &safeBuffer{}
	srv := New(pr, out, submit, Options{SessionID: "s"})

	done := make(chan error, 1)
	go func() {
		done <- srv.Run(context.Background(), events)
	}()

	// Wait for rpc.ready
	waitForMethod(t, out, "rpc.ready", time.Second)

	// Submit user.input as method name
	if _, err := io.WriteString(pw, `{"jsonrpc":"2.0","id":10,"method":"user.input","params":{"text":"hi"}}`+"\n"); err != nil {
		t.Fatal(err)
	}

	select {
	case op := <-opsCh:
		ui, ok := op.(protocol.UserInput)
		if !ok || ui.Text != "hi" {
			t.Fatalf("op = %#v", op)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for op")
	}

	// Emit an event; should appear as event notification
	events <- protocol.TextDelta{Text: "hello"}
	waitForMethod(t, out, "event", time.Second)

	// Envelope method via "op"
	if _, err := io.WriteString(pw, `{"jsonrpc":"2.0","id":11,"method":"op","params":{"type":"interrupt"}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case op := <-opsCh:
		if _, ok := op.(protocol.Interrupt); !ok {
			t.Fatalf("op = %#v, want Interrupt", op)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for interrupt")
	}

	if _, err := io.WriteString(pw, `{"jsonrpc":"2.0","id":12,"method":"shutdown"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not finish")
	}

	// Verify event envelope shape
	var foundEvent bool
	for _, m := range decodeLines(t, out.Bytes()) {
		if m["method"] != "event" {
			continue
		}
		foundEvent = true
		p, _ := m["params"].(map[string]any)
		if p["type"] != "text.delta" {
			t.Fatalf("event type = %v", p["type"])
		}
		data, _ := p["data"].(map[string]any)
		if data["text"] != "hello" {
			t.Fatalf("event data = %v", data)
		}
		if p["v"] != protocol.Version {
			t.Fatalf("event v = %v", p["v"])
		}
	}
	if !foundEvent {
		t.Fatalf("no event notification in %s", out.String())
	}
}

func TestServerUnknownMethod(t *testing.T) {
	submit := func(ctx context.Context, op protocol.Op) error { return nil }
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"nope.thing"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"shutdown"}` + "\n")
	var out bytes.Buffer
	srv := New(in, &out, submit, Options{})
	if err := srv.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	msgs := decodeLines(t, out.Bytes())
	var errMsg map[string]any
	for _, m := range msgs {
		if m["id"] == float64(1) {
			errMsg = m
			break
		}
	}
	if errMsg == nil {
		t.Fatalf("no error response: %s", out.String())
	}
	e, _ := errMsg["error"].(map[string]any)
	if int(e["code"].(float64)) != CodeMethodNotFound {
		t.Fatalf("code = %v, want %d", e["code"], CodeMethodNotFound)
	}
}

func TestServerParseError(t *testing.T) {
	submit := func(ctx context.Context, op protocol.Op) error { return nil }
	in := strings.NewReader("not-json\n" + `{"jsonrpc":"2.0","id":1,"method":"shutdown"}` + "\n")
	var out bytes.Buffer
	srv := New(in, &out, submit, Options{})
	if err := srv.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	msgs := decodeLines(t, out.Bytes())
	var found bool
	for _, m := range msgs {
		e, ok := m["error"].(map[string]any)
		if !ok {
			continue
		}
		if int(e["code"].(float64)) == CodeParseError {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected parse error in %s", out.String())
	}
}

func TestServerSubmitError(t *testing.T) {
	submit := func(ctx context.Context, op protocol.Op) error {
		return errors.New("engine ops queue full")
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"interrupt"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"shutdown"}` + "\n")
	var out bytes.Buffer
	srv := New(in, &out, submit, Options{})
	if err := srv.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	msgs := decodeLines(t, out.Bytes())
	var found bool
	for _, m := range msgs {
		if m["id"] != float64(1) {
			continue
		}
		e, _ := m["error"].(map[string]any)
		if e == nil {
			t.Fatalf("want error response: %v", m)
		}
		if !strings.Contains(e["message"].(string), "queue full") {
			t.Fatalf("message = %v", e["message"])
		}
		found = true
	}
	if !found {
		t.Fatalf("missing submit error: %s", out.String())
	}
}

func TestServerNotificationOpNoResponse(t *testing.T) {
	got := make(chan protocol.Op, 1)
	submit := func(ctx context.Context, op protocol.Op) error {
		got <- op
		return nil
	}
	// No id → notification
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"interrupt"}` + "\n" +
		`{"jsonrpc":"2.0","id":1,"method":"shutdown"}` + "\n")
	var out bytes.Buffer
	srv := New(in, &out, submit, Options{})
	if err := srv.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case op := <-got:
		if _, ok := op.(protocol.Interrupt); !ok {
			t.Fatalf("op = %#v", op)
		}
	default:
		t.Fatal("expected interrupt op")
	}
	// Responses should be ready + shutdown only (no response for notification)
	for _, m := range decodeLines(t, out.Bytes()) {
		if m["id"] == nil && m["method"] == nil {
			t.Fatalf("unexpected bare message: %v", m)
		}
		if m["method"] == "interrupt" {
			t.Fatalf("should not echo interrupt: %v", m)
		}
	}
}

func TestServerStatusIncludesCustomSnapshot(t *testing.T) {
	submit := func(ctx context.Context, op protocol.Op) error { return nil }
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"status"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"shutdown"}` + "\n")
	var out bytes.Buffer
	srv := New(in, &out, submit, Options{
		SessionID: "x",
		Status: func() map[string]any {
			return map[string]any{"busy": false, "model": "echo"}
		},
	})
	if err := srv.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, m := range decodeLines(t, out.Bytes()) {
		if m["id"] != float64(1) {
			continue
		}
		res, _ := m["result"].(map[string]any)
		st, _ := res["status"].(map[string]any)
		if st["model"] != "echo" {
			t.Fatalf("status = %v", res)
		}
		return
	}
	t.Fatalf("no status result: %s", out.String())
}

func TestDecodeOpMethodEmptyParams(t *testing.T) {
	op, err := decodeOpMethod("interrupt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := op.(protocol.Interrupt); !ok {
		t.Fatalf("got %#v", op)
	}
}

// safeBuffer is a mutex-guarded bytes.Buffer for concurrent rpc.Server writes
// and test readers.
type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.b.Bytes()...)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func decodeLines(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func waitForMethod(t *testing.T, out *safeBuffer, method string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, m := range decodeLines(t, out.Bytes()) {
			if m["method"] == method {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for method %q in %s", method, out.String())
}
