package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestServerInitializeSessionPrompt(t *testing.T) {
	opsCh := make(chan protocol.Op, 8)
	submit := func(ctx context.Context, op protocol.Op) error {
		select {
		case opsCh <- op:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	events := make(chan protocol.Event, 16)

	pr, pw := io.Pipe()
	out := &safeBuffer{}
	srv := New(pr, out, submit, Options{
		SessionID:    "sess-acp-1",
		CWD:          "/tmp/proj",
		AgentName:    "strike",
		AgentVersion: "test",
	})

	done := make(chan error, 1)
	go func() {
		done <- srv.Run(context.Background(), events)
	}()

	// initialize
	writeLine(t, pw, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)
	initMsg := waitForID(t, out, float64(1), 2*time.Second)
	res, _ := initMsg["result"].(map[string]any)
	if res["protocolVersion"] != float64(1) {
		t.Fatalf("protocolVersion = %v", res["protocolVersion"])
	}
	caps, _ := res["agentCapabilities"].(map[string]any)
	if caps["loadSession"] != false {
		t.Fatalf("loadSession = %v", caps["loadSession"])
	}
	info, _ := res["agentInfo"].(map[string]any)
	if info["name"] != "strike" {
		t.Fatalf("agentInfo = %v", info)
	}

	// session/new
	writeLine(t, pw, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp/proj","mcpServers":[]}}`)
	newMsg := waitForID(t, out, float64(2), 2*time.Second)
	newRes, _ := newMsg["result"].(map[string]any)
	if newRes["sessionId"] != "sess-acp-1" {
		t.Fatalf("sessionId = %v", newRes["sessionId"])
	}

	// session/prompt — engine will get UserInput; we emit deltas + TurnCompleted
	writeLine(t, pw, `{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"sess-acp-1","prompt":[{"type":"text","text":"hello acp"}]}}`)

	select {
	case op := <-opsCh:
		ui, ok := op.(protocol.UserInput)
		if !ok || ui.Text != "hello acp" {
			t.Fatalf("op = %#v", op)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for UserInput")
	}

	events <- protocol.TextDelta{Text: "world"}
	waitForMethod(t, out, "session/update", 2*time.Second)

	events <- protocol.TurnCompleted{StopReason: "end_turn"}
	promptMsg := waitForID(t, out, float64(3), 3*time.Second)
	pres, _ := promptMsg["result"].(map[string]any)
	if pres["stopReason"] != StopEndTurn {
		t.Fatalf("stopReason = %v", pres["stopReason"])
	}

	// Verify session/update payload shape
	var foundUpdate bool
	for _, m := range decodeLines(t, out.Bytes()) {
		if m["method"] != "session/update" {
			continue
		}
		foundUpdate = true
		p, _ := m["params"].(map[string]any)
		if p["sessionId"] != "sess-acp-1" {
			t.Fatalf("update sessionId = %v", p["sessionId"])
		}
		u, _ := p["update"].(map[string]any)
		if u["sessionUpdate"] != "agent_message_chunk" {
			t.Fatalf("update = %#v", u)
		}
	}
	if !foundUpdate {
		t.Fatalf("no session/update in %s", out.String())
	}

	writeLine(t, pw, `{"jsonrpc":"2.0","id":99,"method":"shutdown"}`)
	_ = pw.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not finish")
	}
}

func TestServerCancel(t *testing.T) {
	opsCh := make(chan protocol.Op, 8)
	submit := func(ctx context.Context, op protocol.Op) error {
		select {
		case opsCh <- op:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	events := make(chan protocol.Event, 8)
	pr, pw := io.Pipe()
	out := &safeBuffer{}
	srv := New(pr, out, submit, Options{SessionID: "s1", CWD: "/tmp"})

	done := make(chan error, 1)
	go func() { done <- srv.Run(context.Background(), events) }()

	writeLine(t, pw, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)
	waitForID(t, out, float64(1), 2*time.Second)
	writeLine(t, pw, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp","mcpServers":[]}}`)
	waitForID(t, out, float64(2), 2*time.Second)

	writeLine(t, pw, `{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"slow"}]}}`)
	select {
	case <-opsCh: // UserInput
	case <-time.After(2 * time.Second):
		t.Fatal("no user input")
	}

	// Cancel notification (no id)
	writeLine(t, pw, `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}`)

	// Expect Interrupt
	deadline := time.After(2 * time.Second)
	var sawInterrupt bool
	for !sawInterrupt {
		select {
		case op := <-opsCh:
			if _, ok := op.(protocol.Interrupt); ok {
				sawInterrupt = true
			}
		case <-deadline:
			t.Fatal("no interrupt")
		}
	}

	events <- protocol.TurnCompleted{StopReason: "interrupted"}
	promptMsg := waitForID(t, out, float64(3), 3*time.Second)
	pres, _ := promptMsg["result"].(map[string]any)
	if pres["stopReason"] != StopCancelled {
		t.Fatalf("stopReason = %v, want cancelled", pres["stopReason"])
	}

	writeLine(t, pw, `{"jsonrpc":"2.0","id":9,"method":"shutdown"}`)
	_ = pw.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hang")
	}
}

func TestServerPermissionAsk(t *testing.T) {
	opsCh := make(chan protocol.Op, 8)
	submit := func(ctx context.Context, op protocol.Op) error {
		select {
		case opsCh <- op:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	events := make(chan protocol.Event, 8)
	pr, pw := io.Pipe()
	out := &safeBuffer{}
	srv := New(pr, out, submit, Options{
		SessionID:         "s1",
		CWD:               "/tmp",
		PermissionTimeout: 5 * time.Second,
	})

	done := make(chan error, 1)
	go func() { done <- srv.Run(context.Background(), events) }()

	writeLine(t, pw, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)
	waitForID(t, out, float64(1), 2*time.Second)
	writeLine(t, pw, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp","mcpServers":[]}}`)
	waitForID(t, out, float64(2), 2*time.Second)
	writeLine(t, pw, `{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"edit"}]}}`)

	select {
	case <-opsCh: // UserInput
	case <-time.After(2 * time.Second):
		t.Fatal("no user input")
	}

	events <- protocol.ToolCallBegin{CallID: "call-9", Name: "bash", Args: json.RawMessage(`{"command":"ls"}`)}
	waitForMethod(t, out, "session/update", 2*time.Second)

	events <- protocol.PermissionAsked{
		RequestID:  "req-1",
		Permission: "bash",
		Patterns:   []string{"ls"},
	}

	// Client receives session/request_permission
	reqMsg := waitForMethod(t, out, "session/request_permission", 2*time.Second)
	reqID := reqMsg["id"]
	params, _ := reqMsg["params"].(map[string]any)
	if params["sessionId"] != "s1" {
		t.Fatalf("params = %#v", params)
	}

	// Reply allow-once
	reply, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      reqID,
		"result": map[string]any{
			"outcome": map[string]any{
				"outcome":  "selected",
				"optionId": "allow-once",
			},
		},
	})
	if _, err := pw.Write(append(reply, '\n')); err != nil {
		t.Fatal(err)
	}

	select {
	case op := <-opsCh:
		pr, ok := op.(protocol.PermissionReply)
		if !ok || pr.RequestID != "req-1" || pr.Decision != protocol.DecisionOnce {
			t.Fatalf("permission reply = %#v", op)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no permission reply op")
	}

	events <- protocol.TurnCompleted{StopReason: "end_turn"}
	waitForID(t, out, float64(3), 3*time.Second)

	writeLine(t, pw, `{"jsonrpc":"2.0","id":9,"method":"shutdown"}`)
	_ = pw.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hang")
	}
}

func TestServerQuestionAskedDismisses(t *testing.T) {
	opsCh := make(chan protocol.Op, 8)
	submit := func(ctx context.Context, op protocol.Op) error {
		select {
		case opsCh <- op:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	events := make(chan protocol.Event, 8)
	pr, pw := io.Pipe()
	out := &safeBuffer{}
	srv := New(pr, out, submit, Options{SessionID: "s1", CWD: "/tmp"})

	done := make(chan error, 1)
	go func() { done <- srv.Run(context.Background(), events) }()

	writeLine(t, pw, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)
	waitForID(t, out, float64(1), 2*time.Second)
	writeLine(t, pw, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp","mcpServers":[]}}`)
	waitForID(t, out, float64(2), 2*time.Second)
	writeLine(t, pw, `{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"s1","prompt":[{"type":"text","text":"q"}]}}`)
	select {
	case <-opsCh: // UserInput
	case <-time.After(2 * time.Second):
		t.Fatal("no user input")
	}

	events <- protocol.QuestionAsked{RequestID: "q-1", Questions: []protocol.QuestionPrompt{{Question: "ok?"}}}
	select {
	case op := <-opsCh:
		qr, ok := op.(protocol.QuestionReply)
		if !ok || qr.RequestID != "q-1" {
			t.Fatalf("op = %#v", op)
		}
		if len(qr.Answers) != 0 {
			t.Fatalf("want empty dismiss answers, got %#v", qr.Answers)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no question reply")
	}

	events <- protocol.TurnCompleted{StopReason: "end_turn"}
	waitForID(t, out, float64(3), 3*time.Second)
	writeLine(t, pw, `{"jsonrpc":"2.0","id":9,"method":"shutdown"}`)
	_ = pw.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hang")
	}
}

func TestServerUnknownMethod(t *testing.T) {
	submit := func(ctx context.Context, op protocol.Op) error { return nil }
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"nope"}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"shutdown"}` + "\n",
	)
	var out bytes.Buffer
	srv := New(in, &out, submit, Options{SessionID: "s"})
	if err := srv.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	msg := findID(t, out.Bytes(), float64(2))
	e, _ := msg["error"].(map[string]any)
	if int(e["code"].(float64)) != CodeMethodNotFound {
		t.Fatalf("error = %#v", e)
	}
}

func TestServerRequiresSessionBeforePrompt(t *testing.T) {
	submit := func(ctx context.Context, op protocol.Op) error { return nil }
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"session/prompt","params":{"sessionId":"x","prompt":[{"type":"text","text":"hi"}]}}` + "\n" +
			`{"jsonrpc":"2.0","id":3,"method":"shutdown"}` + "\n",
	)
	var out bytes.Buffer
	srv := New(in, &out, submit, Options{SessionID: "x"})
	if err := srv.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	msg := findID(t, out.Bytes(), float64(2))
	if msg["error"] == nil {
		t.Fatalf("want error, got %v", msg)
	}
}

// --- helpers ---

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (o *safeBuffer) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.Write(p)
}

func (o *safeBuffer) Bytes() []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]byte(nil), o.b.Bytes()...)
}

func (o *safeBuffer) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b.String()
}

func writeLine(t *testing.T, w io.Writer, line string) {
	t.Helper()
	if _, err := io.WriteString(w, line+"\n"); err != nil {
		t.Fatal(err)
	}
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

func waitForID(t *testing.T, out *safeBuffer, id any, d time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, m := range decodeLines(t, out.Bytes()) {
			if equalID(m["id"], id) {
				if m["result"] != nil || m["error"] != nil {
					return m
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for id=%v in %s", id, out.String())
	return nil
}

func waitForMethod(t *testing.T, out *safeBuffer, method string, d time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(d)
	seen := 0
	for time.Now().Before(deadline) {
		msgs := decodeLines(t, out.Bytes())
		count := 0
		for _, m := range msgs {
			if m["method"] == method {
				count++
				if count > seen {
					return m
				}
			}
		}
		seen = count
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for method=%s in %s", method, out.String())
	return nil
}

func findID(t *testing.T, data []byte, id any) map[string]any {
	t.Helper()
	for _, m := range decodeLines(t, data) {
		if equalID(m["id"], id) {
			return m
		}
	}
	t.Fatalf("id %v not found in %s", id, data)
	return nil
}

func equalID(got, want any) bool {
	switch w := want.(type) {
	case float64:
		g, ok := got.(float64)
		return ok && g == w
	case string:
		g, ok := got.(string)
		return ok && g == w
	default:
		return got == want
	}
}
