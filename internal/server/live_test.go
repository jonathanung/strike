package server

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/session"
)

func TestLiveOpsPOSTAndStatus(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("sess-live", "/tmp/work", []AgentInfo{{Name: "build"}}, ops)
	defer live.Close()

	live.Publish(protocol.ModelSelected{Provider: "echo", Model: "echo"})
	live.Publish(protocol.AgentSelected{Name: "build"})
	live.Publish(protocol.PermissionModeSelected{Mode: protocol.PermissionModeDefault})
	live.Publish(protocol.AutonomySelected{Mode: protocol.AutonomySupervised})

	dir := t.TempDir()
	srv, err := New(Options{Token: "secret", SessionDir: dir, Live: live})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Unauthorized
	res, err := http.Get(ts.URL + "/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status unauth = %d", res.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer secret")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var st StatusSnapshot
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.SessionID != "sess-live" || st.Provider != "echo" || st.Agent != "build" {
		t.Fatalf("status = %+v", st)
	}

	body := `{"type":"user.input","data":{"text":"hello"}}`
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/v1/ops", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ops status = %d", res.StatusCode)
	}
	select {
	case op := <-ops:
		ui, ok := op.(protocol.UserInput)
		if !ok || ui.Text != "hello" {
			t.Fatalf("op = %#v", op)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for op")
	}

	// Permission reply
	body = `{"type":"permission.reply","data":{"requestId":"r1","decision":"once"}}`
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/v1/ops", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("perm ops = %d", res.StatusCode)
	}
	select {
	case op := <-ops:
		pr, ok := op.(protocol.PermissionReply)
		if !ok || pr.RequestID != "r1" || pr.Decision != protocol.DecisionOnce {
			t.Fatalf("op = %#v", op)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout permission op")
	}
}

func TestLiveAgentsAndSessions(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("L1", "/cwd", []AgentInfo{{Name: "build"}, {Name: "plan"}}, ops)
	defer live.Close()
	dir := t.TempDir()
	writeFixtureSession(t, dir, "other", protocol.UserMessage{Text: "x"})
	srv := mustServer(t, Options{Token: "t", SessionDir: dir, Live: live})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/agents", nil)
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var ar agentsResponse
	if err := json.NewDecoder(res.Body).Decode(&ar); err != nil {
		t.Fatal(err)
	}
	if len(ar.Agents) != 2 || ar.Agents[0].Name != "build" {
		t.Fatalf("agents = %+v", ar.Agents)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer t")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var sr sessionsResponse
	if err := json.NewDecoder(res.Body).Decode(&sr); err != nil {
		t.Fatal(err)
	}
	if sr.LiveID != "L1" {
		t.Fatalf("liveId = %q", sr.LiveID)
	}
	found := false
	for _, s := range sr.Sessions {
		if s.ID == "other" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sessions = %+v", sr.Sessions)
	}
}

func TestLiveEventsSSE(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("live1", "/cwd", nil, ops)
	defer live.Close()
	dir := t.TempDir()
	// Backlog on disk
	store, err := session.Open(dir, "live1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(protocol.UserMessage{Text: "backlog"}); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()

	srv := mustServer(t, Options{Token: "t", SessionDir: dir, Live: live, PollInterval: 20 * time.Millisecond})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/live/events?token=t", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}

	got := readSSEData(t, res.Body, 1, 2*time.Second)
	if got[0]["type"] != "user.message" {
		t.Fatalf("backlog = %v", got[0])
	}

	live.Publish(protocol.TextDelta{Text: "hi"})
	got2 := readSSEData(t, res.Body, 1, 2*time.Second)
	if got2[0]["type"] != "text.delta" {
		t.Fatalf("live = %v", got2[0])
	}
	cancel()
}

func TestWebSocketOpsAndEvents(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("ws1", "/cwd", []AgentInfo{{Name: "build"}}, ops)
	defer live.Close()

	dir := t.TempDir()
	srv := mustServer(t, Options{Token: "secret", SessionDir: dir, Live: live})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/ws?token=secret"
	conn, err := dialWS(t, wsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Drain hello/status (and any early frames) until we can send ops.
	deadline := time.Now().Add(2 * time.Second)
	sawStatus := false
	for time.Now().Before(deadline) && !sawStatus {
		msg, err := conn.readText(200 * time.Millisecond)
		if err != nil {
			continue
		}
		var env map[string]any
		if err := json.Unmarshal([]byte(msg), &env); err != nil {
			continue
		}
		if env["type"] == "status" {
			sawStatus = true
		}
	}
	if !sawStatus {
		t.Fatal("did not receive status hello")
	}

	// Send user.input over WS (primary acceptance path).
	if err := conn.writeText(`{"type":"user.input","data":{"text":"ping"}}`); err != nil {
		t.Fatal(err)
	}
	select {
	case op := <-ops:
		if ui, ok := op.(protocol.UserInput); !ok || ui.Text != "ping" {
			t.Fatalf("op = %#v", op)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no op from ws")
	}

	// Permission reply via WS
	if err := conn.writeText(`{"type":"permission.reply","data":{"requestId":"p9","decision":"reject"}}`); err != nil {
		t.Fatal(err)
	}
	select {
	case op := <-ops:
		pr, ok := op.(protocol.PermissionReply)
		if !ok || pr.Decision != protocol.DecisionReject {
			t.Fatalf("op = %#v", op)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no perm op")
	}

	// Event fan-out: publish after client is connected; read until text.delta.
	live.Publish(protocol.TextDelta{Text: "stream"})
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := conn.readText(200 * time.Millisecond)
		if err != nil {
			continue
		}
		var env map[string]any
		if err := json.Unmarshal([]byte(msg), &env); err != nil {
			continue
		}
		if env["type"] == "text.delta" {
			return
		}
	}
	t.Fatal("timeout waiting for text.delta")
}

func TestOpsUnavailableWithoutLive(t *testing.T) {
	srv := testServer(t, t.TempDir(), "secret")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/ops", strings.NewReader(`{"type":"interrupt"}`))
	req.Header.Set("Authorization", "Bearer secret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestAttachPageHasComposer(t *testing.T) {
	srv := testServer(t, t.TempDir(), "secret")
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	srv.Handler().ServeHTTP(res, req)
	body := res.Body.String()
	for _, want := range []string{"composer", "WebSocket", "permission", "btn-send"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing %q", want)
		}
	}
}

func mustServer(t *testing.T, opts Options) *Server {
	t.Helper()
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// --- minimal WS client for tests ---

type testWS struct {
	c    net.Conn
	bufr *bufio.Reader
}

func dialWS(t *testing.T, url string) (*testWS, error) {
	t.Helper()
	// url like ws://127.0.0.1:port/v1/ws?token=secret
	u := strings.TrimPrefix(url, "ws://")
	hostPath := u
	path := "/"
	if i := strings.Index(u, "/"); i >= 0 {
		hostPath = u[:i]
		path = u[i:]
	}
	c, err := net.Dial("tcp", hostPath)
	if err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString([]byte("dGhlIHNhbXBsZSBub25jZQ==")[:16])
	// Fixed test key
	key = "dGhlIHNhbXBsZSBub25jZQ=="
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + hostPath + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		_ = c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	status, err := br.ReadString('\n')
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	if !strings.Contains(status, "101") {
		_ = c.Close()
		return nil, io.EOF
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = c.Close()
			return nil, err
		}
		if line == "\r\n" {
			break
		}
	}
	// Verify accept optional
	_ = wsAcceptKey(key)
	_ = sha1.New
	return &testWS{c: c, bufr: br}, nil
}

func (w *testWS) Close() error { return w.c.Close() }

func (w *testWS) writeText(s string) error {
	payload := []byte(s)
	mask := [4]byte{1, 2, 3, 4}
	n := len(payload)
	var hdr []byte
	hdr = append(hdr, 0x81) // fin+text
	if n < 126 {
		hdr = append(hdr, byte(0x80|n))
	} else {
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	}
	hdr = append(hdr, mask[:]...)
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := w.c.Write(hdr); err != nil {
		return err
	}
	_, err := w.c.Write(masked)
	return err
}

func (w *testWS) readText(timeout time.Duration) (string, error) {
	_ = w.c.SetReadDeadline(time.Now().Add(timeout))
	h := make([]byte, 2)
	if _, err := io.ReadFull(w.bufr, h); err != nil {
		return "", err
	}
	n := int(h[1] & 0x7f)
	if n == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(w.bufr, ext[:]); err != nil {
			return "", err
		}
		n = int(binary.BigEndian.Uint16(ext[:]))
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(w.bufr, payload); err != nil {
		return "", err
	}
	return string(payload), nil
}
